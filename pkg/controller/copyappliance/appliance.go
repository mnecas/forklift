package copyappliance

import (
	"context"
	"fmt"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/controller/base"
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	libitr "github.com/kubev2v/forklift/pkg/lib/itinerary"
	"github.com/kubev2v/forklift/pkg/lib/logging"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/fault"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
	core "k8s.io/api/core/v1"
)

type ApplianceContext struct {
	Appliance  *api.CopyAppliance
	Secret     *core.Secret
	VCenter    *govmomi.Client
	Log        logging.LevelLogger
	finder     *find.Finder
	folder     *object.Folder
	datacenter *object.Datacenter
}

func NewApplianceContext(ctx context.Context, appliance *api.CopyAppliance, provider *api.Provider, secret *core.Secret, log logging.LevelLogger) (ac *ApplianceContext, err error) {
	ac = &ApplianceContext{
		Appliance: appliance,
		Secret:    secret,
		Log:       log,
	}
	//ac.Inventory, err = web.NewClient(ac.Provider)
	//if err != nil {
	//	err = liberr.Wrap(err)
	//	return
	//}
	ac.VCenter, err = base.ConnectGovmomi(
		ctx,
		provider.Spec.URL,
		string(secret.Data["user"]),
		string(secret.Data["password"]),
		provider.Status.Fingerprint,
		secret)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	ac.finder = find.NewFinder(ac.VCenter.Client, false)
	ac.datacenter, err = ac.finder.DatacenterOrDefault(ctx, ac.Appliance.Spec.Datacenter)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	ac.finder.SetDatacenter(ac.datacenter)
	ac.folder, err = ac.finder.FolderOrDefault(ctx, ac.Appliance.Spec.Folder)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	return
}

// Close the connection to the vSphere API.
func (r *ApplianceContext) Close() {
	if r.VCenter != nil {
		_ = r.VCenter.Logout(context.TODO())
		r.VCenter.CloseIdleConnections()
		r.VCenter = nil
	}
}

// InstanceUUID returns the instance UUID of the connected vCenter. A managed
// object reference is only unique within one vCenter, so a recorded moRef must
// not be trusted unless it was recorded against this same instance.
func (r *ApplianceContext) InstanceUUID() string {
	return r.VCenter.Client.ServiceContent.About.InstanceUuid
}

// CloneVM creates the appliance VM and returns its moRef ID. If the name is
// already taken in the target folder, vCenter fails the task with
// DuplicateName.
func (r *ApplianceContext) CloneVM(ctx context.Context) (task *object.Task, err error) {
	pool, err := r.finder.ResourcePool(ctx, r.Appliance.Spec.ResourcePool)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	ds, err := r.finder.Datastore(ctx, r.Appliance.Spec.Datastore)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	template, err := r.finder.VirtualMachine(ctx, r.Appliance.Spec.Template)
	if err != nil {
		err = liberr.Wrap(err, "template", r.Appliance.Spec.Template)
		return
	}
	changes, err := r.diskChanges(ctx, template)
	if err != nil {
		return
	}
	nicChanges, err := r.nicChanges(ctx, template)
	if err != nil {
		return
	}
	changes = append(changes, nicChanges...)
	poolRef := pool.Reference()
	dsRef := ds.Reference()
	cloneSpec := types.VirtualMachineCloneSpec{
		Location: types.VirtualMachineRelocateSpec{
			Pool:      &poolRef,
			Datastore: &dsRef,
		},
		Config: &types.VirtualMachineConfigSpec{
			DeviceChange: changes,
			Annotation:   fmt.Sprintf("Forklift Copy Appliance"),
		},
		PowerOn:  true,
		Template: false,
	}
	task, err = template.Clone(ctx, r.folder, r.Appliance.Name, cloneSpec)
	if err != nil {
		err = liberr.Wrap(err, "template", r.Appliance.Spec.Template)
		return
	}
	r.Log.Info("Cloning appliance VM", "name", r.Appliance.Name)
	return
}

func (r *ApplianceContext) GetTaskInfo(ctx context.Context, task string) (info *types.TaskInfo, err error) {
	moRef := types.ManagedObjectReference{
		Type:  "Task",
		Value: task,
	}
	t := object.NewTask(r.VCenter.Client, moRef)
	var managedTask mo.Task
	err = t.Properties(
		ctx,
		t.Reference(),
		[]string{"info"},
		&managedTask,
	)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	info = &managedTask.Info
	return
}

func (r *ApplianceContext) TaskResult(info *types.TaskInfo) (done bool, result any, err error) {
	done = info.State == types.TaskInfoStateSuccess || info.State == types.TaskInfoStateError
	if !done {
		return
	}
	if info.State == types.TaskInfoStateError {
		err = liberr.New("task failed", "task", info.Task, "fault", info.Error.LocalizedMessage)
		return
	}
	result = info.Result
	return
}

//
//// EnsureVM idempotently ensures the appliance VM exists. A VM already
//// answering to the appliance's name in the target folder is adopted.
//func (r *ApplianceClient) EnsureVM(ctx context.Context, spec ApplianceVMSpec) (vmID string, err error) {
//	_, folder, err := r.finderFor(ctx, spec)
//	if err != nil {
//		return
//	}
//	existing, err := r.findVM(ctx, folder, spec)
//	if err != nil {
//		return
//	}
//	if existing != nil {
//		vmID = existing.Reference().Value
//		r.log.V(1).Info("Adopting existing appliance VM.", "vm", vmID, "name", spec.Name)
//		return
//	}
//	return r.CreateVM(ctx, spec)
//}

func (r *ApplianceContext) VM(moRef string) (vm *object.VirtualMachine) {
	ref := types.ManagedObjectReference{
		Type:  "VirtualMachine",
		Value: moRef,
	}
	vm = object.NewVirtualMachine(r.VCenter.Client, ref)
	return
}

// DeleteVM detaches every disk from the appliance VM (leaving the backing vmdk
// files intact, since they belong to other VMs) and then destroys the VM shell.
// It takes a moRef rather than a spec so that it stays correct even when the
// placement fields of the spec have since changed. It is idempotent: a missing
// VM is treated as success.
func (r *ApplianceContext) DeleteVM(ctx context.Context, moRef string) (err error) {
	vm := r.VM(moRef)
	err = r.powerOff(ctx, vm)
	if err != nil {
		return
	}
	err = r.detachDisks(ctx, vm)
	if err != nil {
		return
	}
	err = r.destroyVM(ctx, vm)
	if err != nil {
		return
	}
	r.Log.Info("Deleted appliance VM.", "vm", moRef)
	return
}

func (r *ApplianceContext) powerOff(ctx context.Context, vm *object.VirtualMachine) (err error) {
	state, err := vm.PowerState(ctx)
	if err != nil {
		err = liberr.Wrap(err, "vm", vm)
		return
	}
	if state == types.VirtualMachinePowerStatePoweredOff {
		return
	}
	task, err := vm.PowerOff(ctx)
	if err != nil {
		err = liberr.Wrap(err, "vm", vm)
		return
	}
	err = task.Wait(ctx)
	if err != nil {
		err = liberr.Wrap(err, "vm", vm)
		return
	}
	return
}

func (r *ApplianceContext) destroyVM(ctx context.Context, vm *object.VirtualMachine) (err error) {
	task, err := vm.Destroy(ctx)
	if err != nil {
		if fault.Is(err, &types.ManagedObjectNotFound{}) {
			err = nil
			return
		}
		err = liberr.Wrap(err, "vm", vm)

	}
	if err = task.Wait(ctx); err != nil {
		if fault.Is(err, &types.ManagedObjectNotFound{}) {
			err = nil
			return
		}
		err = liberr.Wrap(err, "vm", vm)
	}
	return
}

// Detach all disks without deleting their backing vmdk files.
func (r *ApplianceContext) detachDisks(ctx context.Context, vm *object.VirtualMachine) (err error) {
	devices, err := vm.Device(ctx)
	if err != nil {
		if fault.Is(err, &types.ManagedObjectNotFound{}) {
			err = nil
			return
		}
		err = liberr.Wrap(err, "vm", vm)
		return
	}
	var detach []types.BaseVirtualDeviceConfigSpec
	for _, device := range devices.SelectByType((*types.VirtualDisk)(nil)) {
		detach = append(detach, &types.VirtualDeviceConfigSpec{
			Operation:     types.VirtualDeviceConfigSpecOperationRemove,
			FileOperation: "",
			Device:        device,
		})
	}
	if len(detach) == 0 {
		return
	}
	task, err := vm.Reconfigure(ctx, types.VirtualMachineConfigSpec{DeviceChange: detach})
	if err != nil {
		err = liberr.Wrap(err, "vm", vm)
		return
	}
	err = task.Wait(ctx)
	if err != nil {
		err = liberr.Wrap(err, "vm", vm)
		return
	}
	return
}

func (r *ApplianceContext) diskChanges(ctx context.Context, template *object.VirtualMachine) (changes []types.BaseVirtualDeviceConfigSpec, err error) {
	datastore, err := r.finder.Datastore(ctx, r.Appliance.Spec.Datastore)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	devices, err := template.Device(ctx)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	for _, path := range r.Appliance.Spec.AttachDiskPaths {
		controller := devices.PickController((*types.VirtualSCSIController)(nil))
		if controller == nil {
			err = fmt.Errorf("no free SCSI slots; add another controller")
			return
		}
		disk := devices.CreateDisk(
			controller,
			datastore.Reference(),
			path,
		)
		// Leave CapacityInKB and CapacityInBytes at zero.
		backing := disk.Backing.(*types.VirtualDiskFlatVer2BackingInfo)
		backing.DiskMode = string(types.VirtualDiskModeIndependent_nonpersistent)
		change := &types.VirtualDeviceConfigSpec{
			Operation:     types.VirtualDeviceConfigSpecOperationAdd,
			FileOperation: "",
			Device:        disk,
		}
		changes = append(changes, change)
	}
	return
}

func (r *ApplianceContext) nicChanges(ctx context.Context, template *object.VirtualMachine) (changes []types.BaseVirtualDeviceConfigSpec, err error) {
	networks := []string{r.Appliance.Spec.ManagementNetwork}
	if r.Appliance.Spec.TransferNetwork != "" {
		networks = append(networks, r.Appliance.Spec.TransferNetwork)
	}

	for _, network := range networks {
		var net object.NetworkReference
		net, err = r.finder.Network(ctx, "VM Network")
		if err != nil {
			err = liberr.Wrap(err, "network", network)
			return
		}
		backing, bErr := net.EthernetCardBackingInfo(ctx)
		if bErr != nil {
			err = liberr.Wrap(err, "network", network)
			return
		}
		devices, dErr := template.Device(ctx)
		if dErr != nil {
			err = liberr.Wrap(err, "network", network)
			return
		}

		nic, nErr := devices.CreateEthernetCard("vmxnet3", backing)
		if nErr != nil {
			err = liberr.Wrap(err, "network", network)
			return
		}
		nic.GetVirtualDevice().Connectable = &types.VirtualDeviceConnectInfo{
			StartConnected:    true,
			Connected:         true,
			AllowGuestControl: true,
		}
		change := &types.VirtualDeviceConfigSpec{
			Device:    nic,
			Operation: types.VirtualDeviceConfigSpecOperationAdd,
		}
		changes = append(changes, change)
	}
	return
}

type TeardownRunner struct {
	context *ApplianceContext
}

func (r *TeardownRunner) Begin() {
	r.context.Appliance.Status.TaskRef = ""
	r.context.Appliance.Status.Phase = PhasePowerOff
}

func (r *TeardownRunner) Run(ctx context.Context) (err error) {
	if r.context.Appliance.Status.VCenterInstanceUUID != r.context.InstanceUUID() {
		err = liberr.New("vcenter instance uuid mismatch")
		return
	}

	next, err := r.ExecutePhase(ctx)
	if err != nil {
		r.context.Log.Error(err, "phase", r.context.Appliance.Status.Phase)
	}
	r.context.Appliance.Status.Phase = next
	return
}

func (r *TeardownRunner) ExecutePhase(ctx context.Context) (next string, err error) {
	phase := r.context.Appliance.Status.Phase
	switch phase {
	case PhasePowerOff:
		err = r.PowerOff(ctx)
		if err != nil {
			next = PhaseTeardownFailed
			break
		}
		next = PhaseWaitForPowerOff
		fallthrough
	case PhaseWaitForPowerOff:
		var done bool
		done, err = r.WaitForPowerOff(ctx)
		if err != nil {
			next = PhaseTeardownFailed
			break
		}
		if !done {
			next = phase
			return
		}
		next = PhaseDetachDisks
		fallthrough
	case PhaseDetachDisks:
		var done bool
		done, err = r.DetachDisks(ctx)
		if err != nil {
			next = PhaseTeardownFailed
			break
		}
		if !done {
			next = phase
			return
		}
		next = PhaseWaitForDetachDisks
		fallthrough
	case PhaseWaitForDetachDisks:
		var done bool
		done, err = r.WaitForDetachDisks(ctx)
		if err != nil {
			next = PhaseTeardownFailed
			break
		}
		if !done {
			next = phase
			return
		}
		next = PhaseDestroyVM
		fallthrough
	case PhaseDestroyVM:
		var done bool
		done, err = r.DestroyVM(ctx)
		if err != nil {
			next = PhaseTeardownFailed
			break
		}
		if !done {
			next = phase
			return
		}
		next = PhaseWaitForDestroyVM
		fallthrough
	case PhaseWaitForDestroyVM:
		var done bool
		done, err = r.WaitForDestroyVM(ctx)
		if err != nil {
			next = PhaseTeardownFailed
			break
		}
		if !done {
			next = phase
			return
		}
		next = PhaseTeardownCompleted
		fallthrough
	case PhaseTeardownCompleted:
		r.context.Appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Ready,
			Status:   libcnd.True,
			Category: libcnd.Required,
			Message:  "Tearing down the copy appliance has succeeded.",
		})
		next = PhaseTeardownCompleted
	case PhaseTeardownFailed:
		r.context.Appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Error,
			Status:   libcnd.False,
			Category: libcnd.Critical,
			Message:  "Tearing down the copy appliance has failed.",
		})
		next = PhaseTeardownFailed
	default:
		err = liberr.New("unknown phase", "phase", phase)
		next = PhaseTeardownFailed
	}
	return
}

func (r *TeardownRunner) PowerOff(ctx context.Context) (err error) {
	vm := r.context.VM(r.context.Appliance.Status.MoRef)
	r.context.powerOff(ctx, vm)
	return
}

type DeployRunner struct {
	context *ApplianceContext
}

func (r *DeployRunner) Begin() {
	r.context.Appliance.Status.TaskRef = ""
	r.context.Appliance.Status.Phase = PhaseCloneVM
	r.context.Appliance.Status.VCenterInstanceUUID = r.context.InstanceUUID()
}

func (r *DeployRunner) Run(ctx context.Context) (err error) {
	if r.context.Appliance.Status.VCenterInstanceUUID != r.context.InstanceUUID() {
		err = liberr.New("vcenter instance uuid mismatch")
		return
	}

	next, err := r.ExecutePhase(ctx)
	if err != nil {
		r.context.Log.Error(err, "phase", r.context.Appliance.Status.Phase)
	}
	r.context.Appliance.Status.Phase = next
	return
}

func (r *DeployRunner) ExecutePhase(ctx context.Context) (next string, err error) {
	phase := r.context.Appliance.Status.Phase
	switch phase {
	case PhaseCloneVM:
		err = r.CloneVM(ctx)
		if err != nil {
			next = PhaseDeployFailed
			break
		}
		next = PhaseWaitForClone
		fallthrough
	case PhaseWaitForClone:
		var done bool
		done, err = r.WaitForClone(ctx)
		if err != nil {
			next = PhaseDeployFailed
			break
		}
		if !done {
			next = phase
			return
		}
		next = PhaseWaitForExports
		fallthrough
	case PhaseWaitForExports:
		var done bool
		done, err = r.WaitForExports(ctx)
		if err != nil {
			next = PhaseDeployFailed
			break
		}
		if !done {
			next = phase
			return
		}
		next = PhaseDeployCompleted
		fallthrough
	case PhaseDeployCompleted:
		r.context.Appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Ready,
			Status:   libcnd.True,
			Category: libcnd.Required,
			Message:  "Deploying the copy appliance has succeeded.",
		})
		next = PhaseDeployCompleted
	case PhaseDeployFailed:
		r.context.Appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Error,
			Status:   libcnd.False,
			Category: libcnd.Critical,
			Message:  "Deploying the copy appliance has failed.",
		})
		next = PhaseDeployFailed
	default:
		err = liberr.New("unknown phase", "phase", phase)
		next = PhaseDeployFailed
	}
	return
}

func (r *DeployRunner) CloneVM(ctx context.Context) (err error) {
	task, err := r.context.CloneVM(ctx)
	if err != nil {
		return
	}
	r.context.Appliance.Status.TaskRef = task.Reference().Value
	return
}

func (r *DeployRunner) WaitForClone(ctx context.Context) (done bool, err error) {
	info, err := r.context.GetTaskInfo(ctx, r.context.Appliance.Status.TaskRef)
	if err != nil {
		return
	}
	done, result, err := r.context.TaskResult(info)
	if err != nil {
		return
	}
	if !done {
		return
	}
	moRef, ok := result.(types.ManagedObjectReference)
	if !ok {
		err = liberr.New("task result is not a ManagedObjectRef")
		return
	}
	r.context.Appliance.Status.MoRef = moRef.Reference().Value
	r.context.Appliance.Status.TaskRef = ""
	return
}

func (r *DeployRunner) WaitForExports(ctx context.Context) (done bool, err error) {
	done = true
	return
}

func (r *DeployRunner) Itinerary() *libitr.Itinerary {
	return &libitr.Itinerary{
		Name: "Deploy",
		Pipeline: libitr.Pipeline{
			{Name: PhaseCloneVM},
			{Name: PhaseWaitForClone},
			{Name: PhaseWaitForExports},
			{Name: PhaseDeployCompleted},
			{Name: PhaseDeployFailed},
		},
	}
}

func TeardownItinerary() *libitr.Itinerary {
	return &libitr.Itinerary{
		Name: "Teardown",
		Pipeline: libitr.Pipeline{
			{Name: PhasePowerOff},
			{Name: PhaseWaitForPowerOff},
			{Name: PhaseDetachDisks},
			{Name: PhaseWaitForDetachDisks},
			{Name: PhaseDestroyVM},
			{Name: PhaseWaitForDestroyVM},
			{Name: PhaseTeardownCompleted},
		},
	}
}

const (
	PhaseDeployFailed       = "DeployFailed"
	PhaseCloneVM            = "CloneVM"
	PhaseWaitForClone       = "WaitForClone"
	PhaseWaitForExports     = "WaitForExports"
	PhasePowerOff           = "PowerOff"
	PhaseWaitForPowerOff    = "WaitForPowerOff"
	PhaseDetachDisks        = "DetachDisks"
	PhaseWaitForDetachDisks = "WaitForDetachDisks"
	PhaseDestroyVM          = "DestroyVM"
	PhaseWaitForDestroyVM   = "WaitForDestroyVM"
	PhaseDeployCompleted    = "DeployCompleted"
	PhaseTeardownCompleted  = "TeardownCompleted"
	PhaseTeardownFailed     = "TeardownFailed"
)
