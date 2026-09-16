package copyappliance

import (
	"context"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/controller/base"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/kubev2v/forklift/pkg/lib/logging"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/fault"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
	core "k8s.io/api/core/v1"
)

// ApplianceContext is the appliance's connection to the source vCenter. It
// holds the CopyAppliance itself, so the runners that drive it read and write
// their state through the same object the reconciler persists.
type ApplianceContext struct {
	Appliance  *api.CopyAppliance
	Secret     *core.Secret
	VCenter    *govmomi.Client
	Log        logging.LevelLogger
	finder     *find.Finder
	folder     *object.Folder
	datacenter *object.Datacenter
}

// NewApplianceContext connects to the source vCenter and resolves the
// appliance's datacenter and inventory folder. The caller owns the returned
// context and must Close it.
func NewApplianceContext(ctx context.Context, appliance *api.CopyAppliance, provider *api.Provider, secret *core.Secret, log logging.LevelLogger) (ac *ApplianceContext, err error) {
	ac = &ApplianceContext{
		Appliance: appliance,
		Secret:    secret,
		Log:       log,
	}
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
// not be trusted unless it was recorded against this same instance. A closed
// context is connected to nothing and reports no instance.
func (r *ApplianceContext) InstanceUUID() (uuid string) {
	if r.VCenter == nil {
		return
	}
	uuid = r.VCenter.Client.ServiceContent.About.InstanceUuid
	return
}

// CheckInstance verifies that the recorded moRef was written against the
// vCenter we are connected to. A moRef is only unique within one vCenter, so
// acting on one recorded elsewhere would mean destroying a stranger's VM. An
// appliance that recorded no instance predates the field and is adopted.
func (r *ApplianceContext) CheckInstance() (err error) {
	recorded := r.Appliance.Status.VCenterInstanceUUID
	connected := r.InstanceUUID()
	if recorded == "" || connected == "" || recorded == connected {
		return
	}
	err = liberr.New(
		"vcenter instance uuid mismatch",
		"recorded", recorded,
		"connected", connected)
	return
}

// CloneVM clones the template into the appliance VM and returns the task that
// will do it. If the name is already taken in the target folder, vCenter fails
// the task with DuplicateName.
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
			Annotation:   applianceAnnotation,
		},
		PowerOn:  true,
		Template: false,
	}
	task, err = template.Clone(ctx, r.folder, r.Appliance.Name, cloneSpec)
	if err != nil {
		err = liberr.Wrap(err, "template", r.Appliance.Spec.Template)
		return
	}
	r.Log.Info("Cloning appliance VM.", "name", r.Appliance.Name)
	return
}

// SetTask records the vSphere task the current phase is waiting on. A step that
// found nothing to do records no task, and its wait step completes immediately.
func (r *ApplianceContext) SetTask(task *object.Task) {
	if task == nil {
		r.Appliance.Status.TaskRef = ""
		return
	}
	r.Appliance.Status.TaskRef = task.Reference().Value
}

// WaitForTask reports whether the recorded vSphere task has finished, and
// clears the record when it has. An appliance with no recorded task had nothing
// to wait for: the step that would have started one found nothing to do.
func (r *ApplianceContext) WaitForTask(ctx context.Context) (done bool, result any, err error) {
	if r.Appliance.Status.TaskRef == "" {
		done = true
		return
	}
	info, err := r.GetTaskInfo(ctx, r.Appliance.Status.TaskRef)
	if err != nil {
		return
	}
	done, result, err = r.TaskResult(info)
	if err != nil {
		return
	}
	if !done {
		return
	}
	r.Appliance.Status.TaskRef = ""
	return
}

// GetTaskInfo resolves a recorded task moRef back into its vCenter task info.
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

// TaskResult reports whether a vSphere task has settled, and with what. A task
// that failed settles as an error; one still running has neither.
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

// VM wraps a recorded moRef as a virtual machine. It costs no round trip: the
// object is a reference, not a fetch.
func (r *ApplianceContext) VM(moRef string) (vm *object.VirtualMachine) {
	ref := types.ManagedObjectReference{
		Type:  "VirtualMachine",
		Value: moRef,
	}
	vm = object.NewVirtualMachine(r.VCenter.Client, ref)
	return
}

// PowerOff asks the appliance VM to power off and returns the task that will do
// it. A VM that is already off, or that no longer exists, has no work to do and
// returns no task.
func (r *ApplianceContext) PowerOff(ctx context.Context, vm *object.VirtualMachine) (task *object.Task, err error) {
	state, err := vm.PowerState(ctx)
	if err != nil {
		if fault.Is(err, &types.ManagedObjectNotFound{}) {
			err = nil
			return
		}
		err = liberr.Wrap(err, "vm", vm.Reference().Value)
		return
	}
	if state == types.VirtualMachinePowerStatePoweredOff {
		return
	}
	task, err = vm.PowerOff(ctx)
	if err != nil {
		task = nil
		// The VM may have gone, or powered itself off, between reading the
		// power state above and asking it to stop.
		if fault.Is(err, &types.ManagedObjectNotFound{}) || fault.Is(err, &types.InvalidPowerState{}) {
			err = nil
			return
		}
		err = liberr.Wrap(err, "vm", vm.Reference().Value)
		return
	}
	return
}

// DetachDisks removes every virtual disk from the appliance VM and returns the
// task that will do it. The backing vmdk files are left where they are: they
// belong to other VMs. A VM with no disks left, or that no longer exists, has
// no work to do and returns no task.
func (r *ApplianceContext) DetachDisks(ctx context.Context, vm *object.VirtualMachine) (task *object.Task, err error) {
	devices, err := vm.Device(ctx)
	if err != nil {
		if fault.Is(err, &types.ManagedObjectNotFound{}) {
			err = nil
			return
		}
		err = liberr.Wrap(err, "vm", vm.Reference().Value)
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
	task, err = vm.Reconfigure(ctx, types.VirtualMachineConfigSpec{DeviceChange: detach})
	if err != nil {
		task = nil
		if fault.Is(err, &types.ManagedObjectNotFound{}) {
			err = nil
			return
		}
		err = liberr.Wrap(err, "vm", vm.Reference().Value)
		return
	}
	return
}

// DestroyVM destroys the appliance VM shell and returns the task that will do
// it. A VM that no longer exists has no work to do and returns no task.
func (r *ApplianceContext) DestroyVM(ctx context.Context, vm *object.VirtualMachine) (task *object.Task, err error) {
	task, err = vm.Destroy(ctx)
	if err != nil {
		task = nil
		if fault.Is(err, &types.ManagedObjectNotFound{}) {
			err = nil
			return
		}
		err = liberr.Wrap(err, "vm", vm.Reference().Value)
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
			err = liberr.New("no free SCSI slots; add another controller", "disk", path)
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
		// The disk joins the list it was assigned from. CreateDisk picks a unit
		// number by scanning that list, so a disk left out of it means the next
		// disk is given the unit this one is already on.
		devices = append(devices, disk)
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

	devices, err := template.Device(ctx)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	for _, network := range networks {
		net, fErr := r.finder.Network(ctx, network)
		if fErr != nil {
			err = liberr.Wrap(fErr, "network", network)
			return
		}
		backing, bErr := net.EthernetCardBackingInfo(ctx)
		if bErr != nil {
			err = liberr.Wrap(bErr, "network", network)
			return
		}
		nic, nErr := devices.CreateEthernetCard("vmxnet3", backing)
		if nErr != nil {
			err = liberr.Wrap(nErr, "network", network)
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

// applianceAnnotation marks the appliance VM in the vSphere inventory so an
// operator browsing it can tell what created the VM.
const applianceAnnotation = "Forklift Copy Appliance"
