package copyappliance

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
	"time"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/kubev2v/forklift/pkg/lib/logging"
	libvsphere "github.com/kubev2v/forklift/pkg/lib/vsphere"
	"github.com/kubev2v/forklift/pkg/nbd-container/announce"
	"github.com/kubev2v/forklift/pkg/nbd-container/runner"
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
	NbdSsl     bool
	finder     *find.Finder
	folder     *object.Folder
	datacenter *object.Datacenter
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
	ac.VCenter, err = libvsphere.ConnectProvider(
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
	s, err := libvsphere.NewSession(ctx, ac.VCenter, false)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	ac.finder = s.Finder
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
	uuid = r.VCenter.ServiceContent.About.InstanceUuid
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
	devices, err := template.Device(ctx)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	changes, err := r.buildAttachDiskChanges(devices)
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
			Annotation:   api.CopyApplianceAnnotation,
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
	info, err := libvsphere.GetTaskInfo(ctx, r.VCenter.Client, r.Appliance.Status.TaskRef)
	if err != nil {
		return
	}
	done = info.State == types.TaskInfoStateSuccess || info.State == types.TaskInfoStateError
	if !done {
		return
	}
	if info.State == types.TaskInfoStateError {
		err = liberr.New("task failed", "task", info.Task, "fault", info.Error.LocalizedMessage)
		return
	}
	result = info.Result
	r.Appliance.Status.TaskRef = ""
	return
}

// GuestAddresses returns any IP addresses the guest tools are reporting.
func (r *ApplianceContext) GuestAddresses(ctx context.Context, vm *object.VirtualMachine) (addresses []api.ApplianceAddress, err error) {
	ips, err := libvsphere.GuestAddresses(ctx, vm)
	if err != nil {
		return
	}
	for _, ip := range ips {
		addresses = append(addresses, api.ApplianceAddress{
			Network: ip.Network,
			MAC:     ip.MAC,
			IP:      ip.IP,
		})
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
		info, err := libvsphere.GetTaskInfo(ctx, r.VCenter.Client, taskRef.Value)
		if err != nil || info == nil || info.State != types.TaskInfoStateError || info.Error == nil {
			continue
		}
		return info.Error.LocalizedMessage
	}
	return ""
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
	changes, err := r.buildAttachDiskChanges(devices)
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

// WaitForExports reports whether the appliance has published the disk exports
// the migration reads from, and records them.
func (r *ApplianceContext) WaitForExports(ctx context.Context) (done bool, err error) {
	address, ok := r.Appliance.Address()
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

	exports, err := client.Disks(ctx, net.JoinHostPort(address, Settings.AnnouncePort))
	if err != nil {
		var timeout interface{ Timeout() bool }
		if isStarting(err) ||
			errors.Is(err, syscall.ECONNREFUSED) ||
			(errors.As(err, &timeout) && timeout.Timeout()) {
			r.Log.Info("The appliance is not announcing its exports yet.",
				"address", address)
			err = nil
			return
		}
		err = liberr.Wrap(err, "address", address)
		return
	}

	attached := r.Appliance.Spec.AttachDisks
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

// matchExports finds the announced NBD export for each attached disk by serial.
// Extra announced exports are ignored; WaitForExports already waits until the
// guest has at least as many exports as attached disks.
func matchExports(attached []api.AttachedDisk, announced []runner.Export) ([]api.ApplianceExport, error) {
	byID := make(map[string]runner.Export, len(announced))
	for _, export := range announced {
		if id := normalizeDiskID(export.WWID); id != "" {
			byID[id] = export
		}
	}

	matched := make([]api.ApplianceExport, 0, len(attached))
	for _, disk := range attached {
		id := normalizeDiskID(disk.Serial)
		if id == "" {
			return nil, liberr.New(
				"attached disk has no serial to match exports with",
				"vmdk", disk.VMDKPath)
		}
		export, ok := byID[id]
		if !ok {
			return nil, liberr.New(
				"no appliance export matches the attached disk serial",
				"vmdk", disk.VMDKPath,
				"serial", disk.Serial)
		}
		matched = append(matched, api.ApplianceExport{
			WWID:         export.WWID,
			Port:         int32(export.Port), // #nosec G115
			Device:       export.Device,
			DiskKey:      disk.DiskKey,
			VMDKPath:     disk.VMDKPath,
			SourceSerial: disk.Serial,
		})
	}
	return matched, nil
}

// normalizeDiskID strips formatting so a VMware backing.Uuid matches a guest scsi_id WWID.
func normalizeDiskID(id string) string {
	s := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(id), "-", ""))
	// scsi_id prefixes NAA identifiers with "3".
	if len(s) > 1 && s[0] == '3' && strings.HasPrefix(s[1:], "6000c29") {
		s = s[1:]
	}
	return s
}

// buildAttachDiskChanges returns the device changes that attach the spec's
// disks to a VM with the given devices.
//
// No datastore is put on the backing. Spec.Datastore is where the appliance
// itself lives, which is the template's datastore and generally not the source
// VM's, and the datastore a disk is on is already the "[name]" prefix of its
// own path.
func (r *ApplianceContext) buildAttachDiskChanges(devices object.VirtualDeviceList) (changes []types.BaseVirtualDeviceConfigSpec, err error) {
	present := diskPathsOnVM(devices)
	for _, attached := range r.Appliance.Spec.AttachDisks {
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
			types.ManagedObjectReference{},
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
	address, ok := r.Appliance.Address()
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
		Settings.CopyAppliance.SSHUser,
		address,
		Settings.CopyAppliance.SSHPort,
		r.ApplianceSecret)
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

// ServerTLS is the half of the TLS material the appliance needs: the CA to
// verify clients against, and the certificate and key it serves with. The
// client half stays in the cluster. Keyed by the file name each lands under in
// applianceCertsDir.
func (r *ApplianceContext) ServerTLS() (files map[string][]byte, err error) {
	return r.tlsData(announce.CACert, announce.ServerCert, announce.ServerKey)
}

// ClientTLS is the half the controller keeps, to query the appliance's export
// list with.
func (r *ApplianceContext) ClientTLS() (ca, certificate, key []byte, err error) {
	files, err := r.tlsData(announce.CACert, announce.ClientCert, announce.ClientKey)
	if err != nil {
		return
	}
	return files[announce.CACert], files[announce.ClientCert], files[announce.ClientKey], nil
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
	paths := make(map[string]bool, len(spec.AttachDisks))
	for _, disk := range spec.AttachDisks {
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
