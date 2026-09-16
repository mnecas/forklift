package settings

// Environment variables.
const (
	CopyApplianceSSHKeySecret   = "COPY_APPLIANCE_SSH_KEY_SECRET"
	CopyApplianceSSHUser        = "COPY_APPLIANCE_SSH_USER"
	CopyApplianceContainerImage = "COPY_APPLIANCE_CONTAINER_IMAGE"
	CopyApplianceTLSSecret      = "COPY_APPLIANCE_TLS_SECRET"
)

// Defaults.
const (
	// DefaultCopyApplianceSSHUser is the account the appliance image installs
	// the public key for.
	DefaultCopyApplianceSSHUser = "root"
)

// CopyAppliance settings. These describe the appliance image itself, which is
// the same for every copy appliance in a deployment. Placement is derived per
// appliance from the source VM and is not configured here, and the shape of the
// VM comes from the template it is cloned from.
type CopyAppliance struct {
	// Name of the secret holding the SSH key pair the controller logs in to
	// the appliance with. There is no default: it names a secret that only
	// exists in the deployment's own cluster.
	SSHKeySecret string
	// Account the controller logs in to the appliance as.
	SSHUser string
	// ImageStreamTag naming the container image the appliance serves exports
	// from (e.g. "copy-appliance:latest"). There is no default: it names an
	// image stream that only exists in the deployment's own cluster.
	ContainerImage string
	// Name of the secret holding the mutual-TLS material the appliance serves
	// its exports with. There is no default, for the same reason as the SSH
	// key: the certificates are the deployment's own.
	TLSSecret string
}

// Load settings.
func (r *CopyAppliance) Load() error {
	r.SSHKeySecret = Lookup(CopyApplianceSSHKeySecret, "")
	r.SSHUser = Lookup(CopyApplianceSSHUser, DefaultCopyApplianceSSHUser)
	r.ContainerImage = Lookup(CopyApplianceContainerImage, "")
	r.TLSSecret = Lookup(CopyApplianceTLSSecret, "")

	return nil
}
