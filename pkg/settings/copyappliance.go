package settings

// Environment variables.
const (
	CopyApplianceGuestId      = "COPY_APPLIANCE_GUEST_ID"
	CopyApplianceNumCPUs      = "COPY_APPLIANCE_NUM_CPUS"
	CopyApplianceMemoryMB     = "COPY_APPLIANCE_MEMORY_MB"
	CopyApplianceRootDiskPath = "COPY_APPLIANCE_ROOT_DISK_PATH"
	CopyApplianceSSHKeySecret = "COPY_APPLIANCE_SSH_KEY_SECRET"
	CopyApplianceSSHUser      = "COPY_APPLIANCE_SSH_USER"
)

// Defaults.
const (
	DefaultCopyApplianceGuestId  = "otherGuest64"
	DefaultCopyApplianceNumCPUs  = 2
	DefaultCopyApplianceMemoryMB = 2048
	// DefaultCopyApplianceSSHUser is the account the appliance image installs
	// the public key for.
	DefaultCopyApplianceSSHUser = "root"
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
	// Name of the secret holding the SSH key pair the controller logs in to
	// the appliance with. There is no default: it names a secret that only
	// exists in the deployment's own cluster.
	SSHKeySecret string
	// Account the controller logs in to the appliance as.
	SSHUser string
}

// Load settings.
func (r *CopyAppliance) Load() error {
	r.GuestId = Lookup(CopyApplianceGuestId, DefaultCopyApplianceGuestId)
	r.RootDiskPath = Lookup(CopyApplianceRootDiskPath, "")
	r.SSHKeySecret = Lookup(CopyApplianceSSHKeySecret, "")
	r.SSHUser = Lookup(CopyApplianceSSHUser, DefaultCopyApplianceSSHUser)
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
