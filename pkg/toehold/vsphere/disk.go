package vsphere

import (
	"context"
	"fmt"
	"strings"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/types"
)

// AttachedDisk describes a disk linked to a VM.
type AttachedDisk struct {
	VMDKPath      string
	DiskKey       int32
	ControllerKey int32
	UnitNumber    int32
}

// FindAttachedDisk returns a disk already linked to the VM with the given VMDK path.
func (c *Client) FindAttachedDisk(ctx context.Context, vm *object.VirtualMachine, vmdkPath string) (*AttachedDisk, error) {
	devices, err := vm.Device(ctx)
	if err != nil {
		return nil, err
	}
	normalized := normalizeVMDKPath(vmdkPath)
	for _, dev := range devices {
		disk, ok := dev.(*types.VirtualDisk)
		if !ok {
			continue
		}
		backing, ok := disk.Backing.(*types.VirtualDiskFlatVer2BackingInfo)
		if !ok {
			continue
		}
		if normalizeVMDKPath(backing.FileName) != normalized {
			continue
		}
		unit := int32(-1)
		if disk.UnitNumber != nil {
			unit = *disk.UnitNumber
		}
		return &AttachedDisk{
			VMDKPath:      backing.FileName,
			DiskKey:       disk.Key,
			ControllerKey: disk.ControllerKey,
			UnitNumber:    unit,
		}, nil
	}
	return nil, nil
}

// AttachLinkedDisk links an existing VMDK file to a VM.
func (c *Client) AttachLinkedDisk(ctx context.Context, vm *object.VirtualMachine, spec api.ToeholdSpec, disk api.ToeholdDiskSpec) (*AttachedDisk, error) {
	vmdkPath := strings.TrimSpace(disk.VMDKPath)
	if vmdkPath == "" {
		return nil, fmt.Errorf("vmdk path is required")
	}
	diskMode := disk.DiskModeOrDefault()
	existing, err := c.FindAttachedDisk(ctx, vm, vmdkPath)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	devices, err := vm.Device(ctx)
	if err != nil {
		return nil, err
	}
	controller, err := devices.FindDiskController("scsi")
	if err != nil {
		return nil, err
	}
	capacityKB, dsRef, err := c.diskCapacityKB(ctx, spec, disk)
	if err != nil {
		return nil, err
	}
	backing := &types.VirtualDiskFlatVer2BackingInfo{
		VirtualDeviceFileBackingInfo: types.VirtualDeviceFileBackingInfo{
			FileName:  vmdkPath,
			Datastore: dsRef,
		},
		DiskMode: diskMode,
	}
	diskDev := &types.VirtualDisk{
		CapacityInKB: capacityKB,
		VirtualDevice: types.VirtualDevice{
			Key:     -1,
			Backing: backing,
		},
	}
	devices.AssignController(diskDev, controller)
	if err = vm.AddDevice(ctx, diskDev); err != nil {
		return nil, err
	}
	attached, err := c.FindAttachedDisk(ctx, vm, vmdkPath)
	if err != nil {
		return nil, err
	}
	if attached == nil {
		return nil, fmt.Errorf("disk %q was not found after attach", vmdkPath)
	}
	return attached, nil
}

// DetachDiskByBacking removes a linked disk from a VM.
func (c *Client) DetachDiskByBacking(ctx context.Context, vm *object.VirtualMachine, vmdkPath string) error {
	devices, err := vm.Device(ctx)
	if err != nil {
		return err
	}
	normalized := normalizeVMDKPath(vmdkPath)
	for _, dev := range devices {
		disk, ok := dev.(*types.VirtualDisk)
		if !ok {
			continue
		}
		backing, ok := disk.Backing.(*types.VirtualDiskFlatVer2BackingInfo)
		if !ok {
			continue
		}
		if normalizeVMDKPath(backing.FileName) != normalized {
			continue
		}
		return vm.RemoveDevice(ctx, false, disk)
	}
	return nil
}

func (c *Client) diskCapacityKB(ctx context.Context, spec api.ToeholdSpec, disk api.ToeholdDiskSpec) (int64, *types.ManagedObjectReference, error) {
	if disk.CapacityGiB > 0 {
		dsRef, err := c.datastoreRefFromPath(ctx, disk.VMDKPath)
		if err != nil {
			return 0, nil, err
		}
		return disk.CapacityGiB * 1024 * 1024, dsRef, nil
	}
	folder := spec.Folder
	if disk.SourceFolder != "" {
		folder = disk.SourceFolder
	}
	if disk.SourceVM != "" {
		capacityKB, dsRef, err := c.diskCapacityFromSourceVM(ctx, folder, disk.SourceVM, disk.VMDKPath)
		if err == nil {
			return capacityKB, dsRef, nil
		}
	}
	dsRef, err := c.datastoreRefFromPath(ctx, disk.VMDKPath)
	if err != nil {
		return 0, nil, err
	}
	return 0, dsRef, fmt.Errorf("could not determine capacity for %q; set spec.disks[].capacityGiB", disk.VMDKPath)
}

func (c *Client) diskCapacityFromSourceVM(ctx context.Context, folder, vmName, vmdkPath string) (int64, *types.ManagedObjectReference, error) {
	ref, err := c.FindVM(ctx, folder, vmName)
	if err != nil {
		return 0, nil, err
	}
	devices, err := ref.VM.Device(ctx)
	if err != nil {
		return 0, nil, err
	}
	normalized := normalizeVMDKPath(vmdkPath)
	for _, dev := range devices {
		disk, ok := dev.(*types.VirtualDisk)
		if !ok {
			continue
		}
		backing, ok := disk.Backing.(*types.VirtualDiskFlatVer2BackingInfo)
		if !ok {
			continue
		}
		if normalizeVMDKPath(backing.FileName) == normalized {
			return disk.CapacityInKB, backing.Datastore, nil
		}
	}
	return 0, nil, fmt.Errorf("disk %q not found on source VM %q", vmdkPath, vmName)
}

func (c *Client) datastoreRefFromPath(ctx context.Context, vmdkPath string) (*types.ManagedObjectReference, error) {
	var dsPath object.DatastorePath
	if !dsPath.FromString(vmdkPath) {
		return nil, fmt.Errorf("invalid vmdk path %q", vmdkPath)
	}
	ds, err := c.findDatastore(ctx, dsPath.Datastore)
	if err != nil {
		return nil, err
	}
	ref := ds.Reference()
	return &ref, nil
}

func normalizeVMDKPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.ReplaceAll(path, "\\", "/")
	return strings.ToLower(path)
}
