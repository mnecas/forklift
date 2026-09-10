package settings

// Environment variables.
const (
	CopyApplianceGuestId           = "COPY_APPLIANCE_GUEST_ID"
	CopyApplianceNumCPUs           = "COPY_APPLIANCE_NUM_CPUS"
	CopyApplianceMemoryMB          = "COPY_APPLIANCE_MEMORY_MB"
	CopyApplianceRootDiskPath      = "COPY_APPLIANCE_ROOT_DISK_PATH"
	CopyApplianceManagementNetwork = "COPY_APPLIANCE_MANAGEMENT_NETWORK"
	CopyApplianceTransferNetwork   = "COPY_APPLIANCE_TRANSFER_NETWORK"
)

// Defaults.
const (
	DefaultCopyApplianceGuestId  = "otherGuest64"
	DefaultCopyApplianceNumCPUs  = 2
	DefaultCopyApplianceMemoryMB = 2048
	// DefaultCopyApplianceManagementNetwork is the name vSphere gives the
	// portgroup created with a standard switch, so it is the one most likely
	// to reach the appliance.
	DefaultCopyApplianceManagementNetwork = "VM Network"
)

// CopyAppliance settings. These describe the appliance image itself, which is
// the same for every copy appliance in a deployment. Placement is derived per
// appliance from the source VM and is not configured here.
type CopyAppliance struct {
	// Guest OS identifier for the appliance VM.
	GuestId string
	// Number of virtual CPUs for the appliance VM.
	NumCPUs int32
	// Memory for the appliance VM, in MiB.
	MemoryMB int64
	// Datastore path of the appliance root disk vmdk. There is no default:
	// it names a file that only exists in the deployment's own datastore.
	RootDiskPath string
	// Network the appliance is reached on.
	ManagementNetwork string
	// Network the appliance moves disk data over. Empty leaves the appliance
	// with only its management NIC.
	TransferNetwork string
}

// Load settings.
func (r *CopyAppliance) Load() error {
	r.GuestId = Lookup(CopyApplianceGuestId, DefaultCopyApplianceGuestId)
	r.RootDiskPath = Lookup(CopyApplianceRootDiskPath, "")
	r.ManagementNetwork = Lookup(CopyApplianceManagementNetwork, DefaultCopyApplianceManagementNetwork)
	r.TransferNetwork = Lookup(CopyApplianceTransferNetwork, "")
	numCPUs, err := getPositiveEnvLimit(CopyApplianceNumCPUs, DefaultCopyApplianceNumCPUs)
	if err != nil {
		return err
	}
	r.NumCPUs = int32(numCPUs)
	memoryMB, err := getPositiveEnvLimit(CopyApplianceMemoryMB, DefaultCopyApplianceMemoryMB)
	if err != nil {
		return err
	}
	r.MemoryMB = int64(memoryMB)

	return nil
}
