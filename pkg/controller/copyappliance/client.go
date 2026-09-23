package copyappliance

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
	"time"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/controller/base"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/kubev2v/forklift/pkg/lib/logging"
	"github.com/kubev2v/forklift/pkg/nbd-container/announce"
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
	Appliance *api.CopyAppliance
	Secret    *core.Secret
	// ApplianceSecret holds the key pair the controller logs in to the appliance
	// with and the mutual-TLS material the appliance serves its exports with.
	// Nil when the CopyAppliance names a secret that is not there, which only
	// the configure and export steps care about.
	ApplianceSecret *core.Secret
	VCenter         *govmomi.Client
	Log             logging.LevelLogger
	// NbdSsl requires mutual TLS on nbdkit exports (provider toeholdNbdSsl).
	NbdSsl bool
	// sshPort is the port the appliance's sshd answers on. Empty means the
	// standard port, which is the only one an appliance image is built with;
	// a test appliance is on whatever it was given.
	sshPort string
	// announcePortOverride and orchestratorPath are the same idea for the
	// export endpoint and for the binary shipped to the appliance: empty means
	// the real one, and a test supplies its own.
	announcePortOverride string
	orchestratorPath     string
	finder               *find.Finder
	folder               *object.Folder
	datacenter           *object.Datacenter
}

// NewApplianceContext connects to the source vCenter and resolves the
// appliance's datacenter and inventory folder. The caller owns the returned
// context and must Close it.
func NewApplianceContext(ctx context.Context, appliance *api.CopyAppliance, provider *api.Provider, secret, applianceSecret *core.Secret, log logging.LevelLogger) (ac *ApplianceContext, err error) {
	ac = &ApplianceContext{
		Appliance:       appliance,
		Secret:          secret,
		ApplianceSecret: applianceSecret,
		Log:             log,
		NbdSsl:          provider.ToeholdNbdSsl(),
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
	poolPath := r.Appliance.Spec.ResourcePool
	if poolPath == "" {
		err = liberr.New(
			"resource pool is not configured; set copyAppliance.spec.resourcePool or Provider.spec.settings." +
				api.CopyApplianceResourcePool)
		return
	}
	pool, err := r.finder.ResourcePool(ctx, poolPath)
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

// GuestAddresses returns any IP addresses the guest tools are reporting.
func (r *ApplianceContext) GuestAddresses(ctx context.Context, vm *object.VirtualMachine) (addresses []api.ApplianceAddress, err error) {
	var managedVM mo.VirtualMachine
	err = vm.Properties(
		ctx,
		vm.Reference(),
		[]string{"guest.net"},
		&managedVM,
	)
	if err != nil {
		err = liberr.Wrap(err, "vm", vm.Reference().Value)
		return
	}
	if managedVM.Guest == nil {
		return
	}

	for _, nic := range managedVM.Guest.Net {
		if nic.IpConfig == nil {
			continue
		}
		for _, ip := range nic.IpConfig.IpAddress {
			if !isRoutable(ip.IpAddress) {
				continue
			}
			addresses = append(addresses, api.ApplianceAddress{
				Network: nic.Network,
				MAC:     strings.ToLower(nic.MacAddress),
				IP:      ip.IpAddress,
			})
		}
	}
	return
}

// recentTaskFault returns the LocalizedMessage of the most recent failed task
// on the VM, if any.
func (r *ApplianceContext) recentTaskFault(ctx context.Context, vm *object.VirtualMachine) string {
	var managedVM mo.VirtualMachine
	if err := vm.Properties(ctx, vm.Reference(), []string{"recentTask"}, &managedVM); err != nil {
		return ""
	}
	for _, taskRef := range managedVM.RecentTask {
		info, err := r.GetTaskInfo(ctx, taskRef.Value)
		if err != nil || info == nil || info.State != types.TaskInfoStateError || info.Error == nil {
			continue
		}
		return info.Error.LocalizedMessage
	}
	return ""
}

func isRoutable(address string) (ok bool) {
	ip := net.ParseIP(address)
	if ip == nil {
		return
	}
	ok = !ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() &&
		!ip.IsLoopback() &&
		!ip.IsUnspecified()
	return
}

// applianceAddress returns the address to reach the appliance at. The appliance
// has one network, so the choice is only between the addresses the guest holds
// on it; the first is the one the guest listed first, and it answers on any of
// them.
func applianceAddress(addresses []api.ApplianceAddress) (address string, ok bool) {
	if len(addresses) == 0 {
		return
	}
	address = addresses[0].IP
	ok = true
	return
}

// VM creates a VM object from a moRef.
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

// AttachDisks hot-attaches every spec disk that is not already on the appliance
// VM and returns the reconfigure task that will do it.
func (r *ApplianceContext) AttachDisks(ctx context.Context, vm *object.VirtualMachine) (task *object.Task, err error) {
	devices, err := vm.Device(ctx)
	if err != nil {
		if fault.Is(err, &types.ManagedObjectNotFound{}) {
			err = nil
			return
		}
		err = liberr.Wrap(err, "vm", vm.Reference().Value)
		return
	}
	changes, err := r.buildAttachDiskChanges(ctx, devices)
	if err != nil {
		return
	}
	if len(changes) == 0 {
		return
	}
	task, err = vm.Reconfigure(ctx, types.VirtualMachineConfigSpec{DeviceChange: changes})
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

// DetachAttachedDisks removes only the source VMDKs listed in the spec from the
// appliance VM. The template root disk is left in place.
func (r *ApplianceContext) DetachAttachedDisks(ctx context.Context, vm *object.VirtualMachine) (task *object.Task, err error) {
	attachPaths := attachedDiskPathSet(r.Appliance.Spec)
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
		path := diskBackingFile(device)
		if path == "" || !attachPaths[path] {
			continue
		}
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

// WaitForExports reports whether the appliance has published the disk exports
// the migration reads from, and records them.
func (r *ApplianceContext) WaitForExports(ctx context.Context) (done bool, err error) {
	address, ok := applianceAddress(r.Appliance.Status.Addresses)
	if !ok {
		err = liberr.New(
			"the appliance reports no address to reach it on",
			"appliance", r.Appliance.Name)
		return
	}
	ca, certificate, key, err := r.ClientTLS()
	if err != nil {
		return
	}
	client, err := announce.NewClient(ca, certificate, key)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}

	exports, err := client.Disks(ctx, r.announceAddr(address))
	if err != nil {
		if r.announceStarting(err) {
			r.Log.Info("The appliance is not announcing its exports yet.",
				"address", address)
			err = nil
			return
		}
		err = liberr.Wrap(err, "address", address)
		return
	}

	attached := r.Appliance.Spec.AttachedDisks()
	if len(exports) < len(attached) {
		r.Log.Info("The appliance has not exported every disk yet.",
			"address", address,
			"exported", len(exports),
			"attached", len(attached))
		return
	}
	matched, err := matchExports(attached, exports)
	if err != nil {
		err = liberr.Wrap(err, "address", address)
		return
	}
	r.Appliance.Status.Exports = matched
	r.Log.Info("The appliance is exporting its disks.",
		"address", address, "exports", len(matched))
	done = true
	return
}

// announceStarting reports whether the query failed because the announce
// endpoint is not up yet: nothing listening, a connection dropped before the
// reply, or a timeout.
func (r *ApplianceContext) announceStarting(err error) (ok bool) {
	var timeout interface{ Timeout() bool }
	ok = isStarting(err) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		(errors.As(err, &timeout) && timeout.Timeout())
	return
}

// observeExportRequest records the export request the controller has converged.
func (r *ApplianceContext) observeExportRequest() {
	if r.Appliance.Spec.ExportRequest != nil {
		r.Appliance.Status.ObservedExportRequest = r.Appliance.Spec.ExportRequest.DeepCopy()
	}
}

func (r *ApplianceContext) diskChanges(ctx context.Context, template *object.VirtualMachine) (changes []types.BaseVirtualDeviceConfigSpec, err error) {
	devices, err := template.Device(ctx)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	return r.buildAttachDiskChanges(ctx, devices)
}

func (r *ApplianceContext) buildAttachDiskChanges(ctx context.Context, devices object.VirtualDeviceList) (changes []types.BaseVirtualDeviceConfigSpec, err error) {
	datastore, err := r.finder.Datastore(ctx, r.Appliance.Spec.Datastore)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	present := diskPathsOnVM(devices)
	for _, attached := range r.Appliance.Spec.AttachedDisks() {
		path := attached.VMDKPath
		if present[path] {
			continue
		}
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

// SSHClient logs in to the appliance, and reports whether it answered. The
// caller owns the returned client and must Close it.
func (r *ApplianceContext) SSHClient(ctx context.Context, timeout time.Duration) (client *SSHClient, ready bool, err error) {
	address, ok := applianceAddress(r.Appliance.Status.Addresses)
	if !ok {
		err = liberr.New(
			"the appliance reports no address to reach it on",
			"appliance", r.Appliance.Name)
		return
	}
	if r.ApplianceSecret == nil {
		// Named from the spec rather than the secret, which is the whole
		// problem: this is where an operator finds out which one to create.
		ref := r.Appliance.Spec.Secret
		err = liberr.New(
			"the appliance secret is missing",
			"namespace", ref.Namespace,
			"name", ref.Name)
		return
	}
	client, err = NewSSHClient(
		Settings.CopyAppliance.SSHUser, address, r.sshLoginPort(), r.ApplianceSecret)
	if err != nil {
		return
	}
	ready, err = client.Connect(ctx)
	if err != nil || !ready {
		client = nil
		return
	}
	err = client.SetTimeout(timeout)
	if err != nil {
		_ = client.Close()
		client = nil
		ready = false
	}
	return
}

// sshLoginPort is the port the appliance's sshd answers on. Empty means the
// standard port, which is the only one an appliance image is built with; a test
// appliance is on whatever it was given.
func (r *ApplianceContext) sshLoginPort() (port string) {
	port = r.sshPort
	if port == "" {
		port = ApplianceSSHPort
	}
	return
}

// announceAddr is the address to query the appliance's export list at.
func (r *ApplianceContext) announceAddr(address string) (addr string) {
	return net.JoinHostPort(address, r.announcePort())
}

// announcePort is the port the appliance announces its exports on. Empty means
// the port the orchestrator defaults to, which is the only one an appliance is
// installed with; a test appliance is on whatever it was given.
func (r *ApplianceContext) announcePort() (port string) {
	port = r.announcePortOverride
	if port == "" {
		port = applianceAnnouncePort
	}
	return
}

// ServerTLS is the half of the TLS material the appliance needs: the CA to
// verify clients against, and the certificate and key it serves with. The
// client half stays in the cluster. Keyed by the file name each lands under in
// applianceCertsDir.
func (r *ApplianceContext) ServerTLS() (files map[string][]byte, err error) {
	return r.tlsData(tlsCACert, tlsServerCert, tlsServerKey)
}

// ClientTLS is the half the controller keeps, to query the appliance's export
// list with.
func (r *ApplianceContext) ClientTLS() (ca, certificate, key []byte, err error) {
	files, err := r.tlsData(tlsCACert, tlsClientCert, tlsClientKey)
	if err != nil {
		return
	}
	return files[tlsCACert], files[tlsClientCert], files[tlsClientKey], nil
}

// tlsData reads the named keys out of the appliance's secret, and reports which
// one is missing rather than that something is.
func (r *ApplianceContext) tlsData(keys ...string) (files map[string][]byte, err error) {
	ref := r.Appliance.Spec.Secret
	if r.ApplianceSecret == nil {
		err = liberr.New(
			"the appliance secret is missing",
			"namespace", ref.Namespace,
			"name", ref.Name)
		return
	}
	files = make(map[string][]byte, len(keys))
	for _, key := range keys {
		value, found := r.ApplianceSecret.Data[key]
		if !found || len(value) == 0 {
			files = nil
			err = liberr.New(
				"the appliance secret has no "+key,
				"namespace", r.ApplianceSecret.Namespace,
				"name", r.ApplianceSecret.Name)
			return
		}
		files[key] = value
	}
	return
}

func attachedDiskPathSet(spec api.CopyApplianceSpec) map[string]bool {
	paths := make(map[string]bool, len(spec.AttachedDisks()))
	for _, disk := range spec.AttachedDisks() {
		paths[disk.VMDKPath] = true
	}
	return paths
}

func diskPathsOnVM(devices object.VirtualDeviceList) map[string]bool {
	paths := make(map[string]bool)
	for _, device := range devices.SelectByType((*types.VirtualDisk)(nil)) {
		path := diskBackingFile(device)
		if path != "" {
			paths[path] = true
		}
	}
	return paths
}

func diskBackingFile(device types.BaseVirtualDevice) string {
	disk, ok := device.(*types.VirtualDisk)
	if !ok {
		return ""
	}
	backing, ok := disk.Backing.(*types.VirtualDiskFlatVer2BackingInfo)
	if !ok {
		return ""
	}
	return backing.FileName
}

// applianceAnnotation marks the appliance VM in the vSphere inventory so an
// operator browsing it can tell what created the VM.
const applianceAnnotation = "Forklift Copy Appliance"
