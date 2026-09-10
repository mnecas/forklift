package vsphere

import (
	"context"
	"errors"
	"fmt"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/ref"
	"github.com/kubev2v/forklift/pkg/controller/base"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/kubev2v/forklift/pkg/lib/logging"
	"github.com/kubev2v/forklift/pkg/controller/provider/web"
	model "github.com/kubev2v/forklift/pkg/controller/provider/web/vsphere"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/fault"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
	core "k8s.io/api/core/v1"
)

const (
	// Maximum number of disks addressable on a single SCSI controller
	// (units 0-15, excluding unit 7 which is reserved for the controller).
	disksPerController = 15
	// SCSI unit number reserved for the controller itself.
	scsiControllerUnit = 7
	// Maximum number of SCSI controllers vSphere permits on a single VM
	// (bus numbers 0-3).
	maxControllers = 4
	// Maximum number of disks the appliance VM can address.
	maxDisks = maxControllers * disksPerController
	// Maximum number of ethernet cards vSphere permits on a single VM.
	maxNICs = 10
	// Base (negative, temporary) device keys used while building the config
	// spec. vCenter reassigns real positive keys on creation. The ranges are
	// spaced so the controller, disk, and NIC blocks cannot overlap at
	// maxDisks and maxNICs; TestApplianceDeviceKeysUnique guards that.
	scsiControllerBaseKey = int32(-100)
	diskBaseKey           = int32(-200)
	nicBaseKey            = int32(-300)
)

// ApplianceVMSpec describes a copy-appliance VM to be created in a vSphere
// source provider. It is a plain struct so the vsphere adapter package does
// not depend on the CopyAppliance CRD types.
type ApplianceVMSpec struct {
	// Name of the appliance VM. Also how the VM is found when VMID is empty.
	Name string
	// Guest OS identifier (e.g. "otherGuest64").
	GuestId string
	// Number of virtual CPUs.
	NumCPUs int32
	// Memory, in MiB.
	MemoryMB int64
	// Datacenter (empty selects the default).
	Datacenter string
	// Datastore holding the VM home directory.
	Datastore string
	// Resource pool.
	ResourcePool string
	// Host (optional; empty leaves placement to DRS).
	Host string
	// Inventory folder.
	Folder string
	// Networks (standard or distributed portgroups) to attach the VM to, one
	// NIC each, in order.
	Networks []string
	// Datastore path of the existing root disk image vmdk.
	RootDiskPath string
	// Datastore paths of existing vmdks (belonging to other VMs) to attach.
	AttachDiskPaths []string
	// Known managed object reference ID of a previously created VM, used for
	// idempotent lookups. May be empty.
	VMID string
	// Note recorded on the VM for operators browsing the vSphere UI. Purely
	// informational; nothing keys off it.
	Annotation string
}

// ApplianceClient manages a copy-appliance VM lifecycle on a vSphere provider
// without requiring a plan context.
type ApplianceClient struct {
	client    *govmomi.Client
	inventory web.Client
	log       logging.LevelLogger
}

// NewApplianceClient connects to the source provider and returns a client that
// can create, inspect, and delete the copy-appliance VM. Call Close when done.
func NewApplianceClient(ctx context.Context, provider *api.Provider, secret *core.Secret, log logging.LevelLogger) (*ApplianceClient, error) {
	client, err := base.ConnectGovmomi(
		ctx,
		provider.Spec.URL,
		string(secret.Data["user"]),
		string(secret.Data["password"]),
		provider.Status.Fingerprint,
		secret)
	if err != nil {
		return nil, liberr.Wrap(err)
	}
	inventory, err := web.NewClient(provider)
	if err != nil {
		_ = client.Logout(ctx)
		client.CloseIdleConnections()
		return nil, liberr.Wrap(err)
	}
	return &ApplianceClient{
		client:    client,
		inventory: inventory,
		log:       log,
	}, nil
}

// finderFor returns a finder scoped to the spec's datacenter along with the
// target inventory folder.
func (r *ApplianceClient) finderFor(ctx context.Context, spec ApplianceVMSpec) (finder *find.Finder, folder *object.Folder, err error) {
	finder = find.NewFinder(r.client.Client, false)
	dc, err := finder.DatacenterOrDefault(ctx, spec.Datacenter)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	finder.SetDatacenter(dc)
	folder, err = finder.FolderOrDefault(ctx, spec.Folder)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	return
}

// Close the connection to the vSphere API.
func (r *ApplianceClient) Close() {
	if r.client != nil {
		_ = r.client.Logout(context.TODO())
		r.client.CloseIdleConnections()
		r.client = nil
	}
}

// InstanceUUID returns the instance UUID of the connected vCenter. A managed
// object reference is only unique within one vCenter, so a recorded moRef must
// not be trusted unless it was recorded against this same instance.
func (r *ApplianceClient) InstanceUUID() string {
	return r.client.Client.ServiceContent.About.InstanceUuid
}

// CreateVM creates the appliance VM and returns its moRef ID. If the name is
// already taken in the target folder, vCenter fails the task with
// DuplicateName.
func (r *ApplianceClient) CreateVM(ctx context.Context, spec ApplianceVMSpec) (vmID string, err error) {
	finder, folder, err := r.finderFor(ctx, spec)
	if err != nil {
		return
	}
	dsCache := newDatastoreCache(finder)

	pool, err := finder.ResourcePool(ctx, spec.ResourcePool)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	var host *object.HostSystem
	if spec.Host != "" {
		host, err = finder.HostSystem(ctx, spec.Host)
		if err != nil {
			err = liberr.Wrap(err)
			return
		}
	}
	ds, err := dsCache.get(ctx, spec.Datastore)
	if err != nil {
		return
	}

	deviceChanges, err := r.buildDeviceChanges(ctx, finder, dsCache, spec)
	if err != nil {
		return
	}

	configSpec := types.VirtualMachineConfigSpec{
		Name:     spec.Name,
		GuestId:  spec.GuestId,
		NumCPUs:  spec.NumCPUs,
		MemoryMB: spec.MemoryMB,
		Files: &types.VirtualMachineFileInfo{
			VmPathName: fmt.Sprintf("[%s]", ds.Name()),
		},
		DeviceChange: deviceChanges,
		Annotation:   spec.Annotation,
	}

	task, err := folder.CreateVM(ctx, configSpec, pool, host)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	info, err := task.WaitForResult(ctx, nil)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	moRef, ok := info.Result.(types.ManagedObjectReference)
	if !ok {
		err = liberr.New("unexpected CreateVM task result type")
		return
	}
	vmID = moRef.Value
	r.log.Info("Created appliance VM.", "vm", vmID, "name", spec.Name)
	return
}

// EnsureVM idempotently ensures the appliance VM exists, and does nothing else.
// A VM already answering to the appliance's name in the target folder is
// adopted. Returns the VM's moRef ID.
func (r *ApplianceClient) EnsureVM(ctx context.Context, spec ApplianceVMSpec) (vmID string, err error) {
	finder, folder, err := r.finderFor(ctx, spec)
	if err != nil {
		return
	}
	dsCache := newDatastoreCache(finder)

	// Idempotency: adopt an existing VM if one is ours.
	existing, err := r.findVM(ctx, folder, spec)
	if err != nil {
		return
	}
	if existing != nil {
		vmID = existing.Reference().Value
		r.log.V(1).Info("Adopting existing appliance VM.", "vm", vmID, "name", spec.Name)
		return
	}

	pool, err := finder.ResourcePool(ctx, spec.ResourcePool)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	var host *object.HostSystem
	if spec.Host != "" {
		host, err = finder.HostSystem(ctx, spec.Host)
		if err != nil {
			err = liberr.Wrap(err)
			return
		}
	}
	ds, err := dsCache.get(ctx, spec.Datastore)
	if err != nil {
		return
	}

	deviceChanges, err := r.buildDeviceChanges(ctx, finder, dsCache, spec)
	if err != nil {
		return
	}

	configSpec := types.VirtualMachineConfigSpec{
		Name:     spec.Name,
		GuestId:  spec.GuestId,
		NumCPUs:  spec.NumCPUs,
		MemoryMB: spec.MemoryMB,
		Files: &types.VirtualMachineFileInfo{
			VmPathName: fmt.Sprintf("[%s]", ds.Name()),
		},
		DeviceChange: deviceChanges,
		Annotation:   spec.Annotation,
	}

	task, err := folder.CreateVM(ctx, configSpec, pool, host)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	info, err := task.WaitForResult(ctx, nil)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	moRef, ok := info.Result.(types.ManagedObjectReference)
	if !ok {
		err = liberr.New("unexpected CreateVM task result type")
		return
	}
	// A freshly created VM is always powered off; the caller drives power on
	// separately, so there is nothing to read back here.
	vmID = moRef.Value
	r.log.Info("Created appliance VM.", "vm", vmID, "name", spec.Name)
	return
}

// ErrVMNotFound reports that a moRef no longer resolves to a VM. The caller
// should treat the VM as gone and rebuild rather than fail permanently.
var ErrVMNotFound = errors.New("the appliance VM no longer exists")

// PoweredOn reports whether the appliance VM is running.
func (r *ApplianceClient) PoweredOn(ctx context.Context, vmID string) (poweredOn bool, err error) {
	state, err := r.vmRef(vmID).PowerState(ctx)
	if err != nil {
		if isNotFound(err) {
			err = liberr.Wrap(ErrVMNotFound, "vm", vmID)
			return
		}
		err = liberr.Wrap(err, "vm", vmID)
		return
	}
	poweredOn = state == types.VirtualMachinePowerStatePoweredOn
	return
}

// PowerOn starts the appliance VM and waits for the power-on task to finish, so
// that a subsequent reconcile does not issue a second overlapping task. A VM
// that is already on is treated as success.
func (r *ApplianceClient) PowerOn(ctx context.Context, vmID string) (err error) {
	task, err := r.vmRef(vmID).PowerOn(ctx)
	if err != nil {
		if isAlreadyInState(err) {
			return nil
		}
		return liberr.Wrap(err, "vm", vmID)
	}
	if err = task.Wait(ctx); err != nil {
		if isAlreadyInState(err) {
			return nil
		}
		return liberr.Wrap(err, "vm", vmID)
	}
	r.log.Info("Powered on appliance VM.", "vm", vmID)
	return nil
}

// DeleteVM detaches every disk from the appliance VM (leaving the backing vmdk
// files intact, since they belong to other VMs) and then destroys the VM shell.
// It takes a moRef rather than a spec so that it stays correct even when the
// placement fields of the spec have since changed. It is idempotent: a missing
// VM is treated as success.
func (r *ApplianceClient) DeleteVM(ctx context.Context, vmID string) (err error) {
	if vmID == "" {
		return nil
	}
	vm := r.vmRef(vmID)

	// Power off (hard) so reconfigure/destroy are permitted.
	state, err := vm.PowerState(ctx)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return liberr.Wrap(err, "vm", vmID)
	}
	if state == types.VirtualMachinePowerStatePoweredOn {
		task, powerErr := vm.PowerOff(ctx)
		if powerErr != nil && !isAlreadyInState(powerErr) {
			return liberr.Wrap(powerErr, "vm", vmID)
		}
		if powerErr == nil {
			if err = task.Wait(ctx); err != nil && !isAlreadyInState(err) {
				return liberr.Wrap(err, "vm", vmID)
			}
		}
	}

	// Detach all disks WITHOUT deleting their backing vmdk files. An empty
	// FileOperation detaches only; FileOperationDestroy would delete the vmdk,
	// which we must never do for disks that belong to other VMs.
	devices, err := vm.Device(ctx)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return liberr.Wrap(err, "vm", vmID)
	}
	var detach []types.BaseVirtualDeviceConfigSpec
	for _, device := range devices.SelectByType((*types.VirtualDisk)(nil)) {
		detach = append(detach, &types.VirtualDeviceConfigSpec{
			Operation:     types.VirtualDeviceConfigSpecOperationRemove,
			FileOperation: "",
			Device:        device,
		})
	}
	if len(detach) > 0 {
		task, reconfigErr := vm.Reconfigure(ctx, types.VirtualMachineConfigSpec{DeviceChange: detach})
		if reconfigErr != nil {
			return liberr.Wrap(reconfigErr, "vm", vmID)
		}
		if err = task.Wait(ctx); err != nil {
			return liberr.Wrap(err, "vm", vmID)
		}
	}

	// Destroy_Task deletes the entire VM home directory, so a disk that
	// survived the detach would take another VM's vmdk with it. Re-read the
	// device list and refuse to destroy unless it is genuinely diskless.
	devices, err = vm.Device(ctx)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return liberr.Wrap(err, "vm", vmID)
	}
	if remaining := len(devices.SelectByType((*types.VirtualDisk)(nil))); remaining > 0 {
		return liberr.New(
			"refusing to destroy appliance VM with disks still attached",
			"vm", vmID,
			"disks", remaining)
	}

	// Destroy the now-diskless VM shell.
	task, err := vm.Destroy(ctx)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return liberr.Wrap(err, "vm", vmID)
	}
	if err = task.Wait(ctx); err != nil {
		if isNotFound(err) {
			return nil
		}
		return liberr.Wrap(err, "vm", vmID)
	}
	r.log.Info("Deleted appliance VM.", "vm", vmID)
	return nil
}

// FindByName searches the inventory for a VM with the appliance's name. It
// recovers an appliance VM whose moRef was never recorded in status — one that
// would otherwise survive invisibly, holding locks on the source vmdks.
// Returns an empty ID (no error) when no such VM exists.
func (r *ApplianceClient) FindByName(ctx context.Context, spec ApplianceVMSpec) (vmID string, err error) {
	if spec.Name == "" {
		return
	}
	vm := &model.VM{}
	err = r.inventory.Find(vm, ref.Ref{Name: spec.Name})
	if err != nil {
		if errors.As(err, &web.NotFoundError{}) {
			return "", nil
		}
		err = liberr.Wrap(err)
		return
	}
	vmID = vm.ID
	r.log.Info("Recovered appliance VM by name.", "vm", vmID, "name", spec.Name)
	return
}

// findVM locates the appliance VM by moRef ID (preferred) or by its name in the
// target folder. The name is the CopyAppliance's own, so a VM answering to it
// is this appliance's. Returns nil (no error) when no VM exists under either.
func (r *ApplianceClient) findVM(ctx context.Context, folder *object.Folder, spec ApplianceVMSpec) (vm *object.VirtualMachine, err error) {
	if spec.VMID != "" {
		candidate := r.vmRef(spec.VMID)
		var mvm mo.VirtualMachine
		err = candidate.Properties(ctx, candidate.Reference(), []string{"name"}, &mvm)
		switch {
		case err == nil:
			if mvm.Name == spec.Name {
				return candidate, nil
			}
			// The moRef resolves, but not to this appliance. vCenter can hand
			// a deleted VM's ID to a new one, and destroying whatever answers
			// to a remembered ID is not recoverable.
			r.log.Info("Recorded appliance moRef resolves to a different VM; ignoring it.",
				"vm", spec.VMID, "found", mvm.Name, "want", spec.Name)
		case isNotFound(err):
			// Stale moRef; fall through to the name lookup.
			err = nil
		default:
			err = liberr.Wrap(err, "vm", spec.VMID)
			return
		}
	}

	if spec.Name == "" {
		return
	}
	// A lost status write leaves a VM that only its name can find.
	ref, err := object.NewSearchIndex(r.client.Client).FindChild(ctx, folder.Reference(), spec.Name)
	if err != nil {
		err = liberr.Wrap(err, "name", spec.Name)
		return
	}
	if ref == nil {
		return
	}
	return object.NewVirtualMachine(r.client.Client, ref.Reference()), nil
}

// vmRef wraps a managed object reference ID as a VM object without contacting
// vCenter.
func (r *ApplianceClient) vmRef(vmID string) *object.VirtualMachine {
	return object.NewVirtualMachine(
		r.client.Client,
		types.ManagedObjectReference{Type: "VirtualMachine", Value: vmID})
}

// isNotFound reports whether the error means the managed object is gone, which
// every delete path treats as success.
func isNotFound(err error) bool {
	return fault.Is(err, &types.ManagedObjectNotFound{})
}

// isAlreadyInState reports whether a power operation failed because the VM was
// already in the requested state, which is success for our purposes.
func isAlreadyInState(err error) bool {
	return fault.Is(err, &types.InvalidPowerState{}) || fault.Is(err, &types.InvalidState{})
}

// datastoreCache resolves datastores by name, memoizing lookups so a datastore
// referenced by several disks (or by both the VM home and a disk) costs a
// single round trip.
type datastoreCache struct {
	finder *find.Finder
	cache  map[string]*object.Datastore
}

func newDatastoreCache(finder *find.Finder) *datastoreCache {
	return &datastoreCache{
		finder: finder,
		cache:  map[string]*object.Datastore{},
	}
}

func (r *datastoreCache) get(ctx context.Context, name string) (*object.Datastore, error) {
	if ds, ok := r.cache[name]; ok {
		return ds, nil
	}
	ds, err := r.finder.Datastore(ctx, name)
	if err != nil {
		return nil, liberr.Wrap(err, "datastore", name)
	}
	r.cache[name] = ds
	return ds, nil
}

// controllerCount returns the number of SCSI controllers needed to host the
// given number of disks.
func controllerCount(disks int) int {
	return (disks + disksPerController - 1) / disksPerController
}

// diskPlacement maps a disk's ordinal position to the SCSI bus it lands on and
// its unit number on that bus, skipping the unit reserved for the controller.
func diskPlacement(i int) (bus int32, unit int32) {
	bus = int32(i / disksPerController)
	unit = int32(i % disksPerController)
	if unit >= scsiControllerUnit {
		unit++
	}
	return
}

// buildDeviceChanges builds the device-change list for VM creation: one or more
// SCSI controllers plus the root disk followed by the attached disks, then one
// NIC per network. Every disk references an existing vmdk backing (Operation=add,
// FileOperation empty), so no new vmdk is ever provisioned.
func (r *ApplianceClient) buildDeviceChanges(ctx context.Context, finder *find.Finder, dsCache *datastoreCache, spec ApplianceVMSpec) ([]types.BaseVirtualDeviceConfigSpec, error) {
	allDisks := append([]string{spec.RootDiskPath}, spec.AttachDiskPaths...)
	if len(allDisks) > maxDisks {
		return nil, liberr.New(
			"too many disks for a vSphere VM",
			"disks", len(allDisks),
			"max", maxDisks)
	}
	if len(spec.Networks) > maxNICs {
		return nil, liberr.New(
			"too many networks for a vSphere VM",
			"networks", len(spec.Networks),
			"max", maxNICs)
	}
	controllers := controllerCount(len(allDisks))

	changes := make([]types.BaseVirtualDeviceConfigSpec, 0, len(allDisks)+controllers+len(spec.Networks))
	for c := 0; c < controllers; c++ {
		changes = append(changes, &types.VirtualDeviceConfigSpec{
			Operation: types.VirtualDeviceConfigSpecOperationAdd,
			Device:    scsiController(c),
		})
	}

	for i, diskPath := range allDisks {
		dsName := parseDatastoreName(diskPath)
		if dsName == "" {
			return nil, liberr.New("invalid vSphere datastore path", "path", diskPath)
		}
		dstore, err := dsCache.get(ctx, dsName)
		if err != nil {
			return nil, err
		}
		dsRef := dstore.Reference()

		controllerIndex, unit := diskPlacement(i)

		disk := &types.VirtualDisk{
			VirtualDevice: types.VirtualDevice{
				Key:           diskBaseKey - int32(i),
				ControllerKey: scsiControllerBaseKey - controllerIndex,
				UnitNumber:    &unit,
				Backing: &types.VirtualDiskFlatVer2BackingInfo{
					// The appliance only ever reads these disks. An
					// independent non-persistent mode opens the backing
					// read-only and diverts writes to a redo log that is
					// discarded on power off, so a disk already attached
					// elsewhere (or a root image shared by several
					// appliances) does not fail to lock.
					DiskMode: string(types.VirtualDiskModeIndependent_nonpersistent),
					VirtualDeviceFileBackingInfo: types.VirtualDeviceFileBackingInfo{
						FileName:  diskPath,
						Datastore: &dsRef,
					},
				},
			},
			// CapacityInKB / CapacityInBytes intentionally left 0 so the
			// existing backing is attached rather than a new disk created.
		}
		changes = append(changes, &types.VirtualDeviceConfigSpec{
			Operation:     types.VirtualDeviceConfigSpecOperationAdd,
			FileOperation: "",
			Device:        disk,
		})
	}

	for i, network := range spec.Networks {
		nic, err := buildNetworkDevice(ctx, finder, network, i)
		if err != nil {
			return nil, err
		}
		changes = append(changes, nic)
	}

	return changes, nil
}

// buildNetworkDevice resolves the named network and builds a vmxnet3 adapter
// backed by it. Standard and distributed portgroups are both handled, since
// EthernetCardBackingInfo picks the right backing type for each. The index is
// the card's position in the spec's network list.
func buildNetworkDevice(ctx context.Context, finder *find.Finder, network string, index int) (types.BaseVirtualDeviceConfigSpec, error) {
	if network == "" {
		// The finder reads an empty name as "the default", which is some other
		// network in some other place.
		return nil, liberr.New("appliance network is empty", "index", index)
	}
	netObj, err := finder.Network(ctx, network)
	if err != nil {
		return nil, liberr.Wrap(err, "network", network)
	}
	backing, err := netObj.EthernetCardBackingInfo(ctx)
	if err != nil {
		return nil, liberr.Wrap(err, "network", network)
	}
	nic, err := object.VirtualDeviceList{}.CreateEthernetCard("vmxnet3", backing)
	if err != nil {
		return nil, liberr.Wrap(err, "network", network)
	}
	// The card comes back with a zero key. Give it a temporary negative one
	// for consistency with the other devices, but leave ControllerKey at zero:
	// there is no PCI controller in this device list, so vCenter must place
	// the card on the implicit one.
	nic.GetVirtualDevice().Key = nicBaseKey - int32(index)
	return &types.VirtualDeviceConfigSpec{
		Operation: types.VirtualDeviceConfigSpecOperationAdd,
		Device:    nic,
	}, nil
}

// scsiController builds a paravirtual SCSI controller with a temporary negative
// key for the given controller index (bus number).
func scsiController(index int) *types.ParaVirtualSCSIController {
	return &types.ParaVirtualSCSIController{
		VirtualSCSIController: types.VirtualSCSIController{
			VirtualController: types.VirtualController{
				VirtualDevice: types.VirtualDevice{
					Key: scsiControllerBaseKey - int32(index),
				},
				BusNumber: int32(index),
			},
			HotAddRemove:       types.NewBool(true),
			SharedBus:          types.VirtualSCSISharingNoSharing,
			ScsiCtlrUnitNumber: scsiControllerUnit,
		},
	}
}
