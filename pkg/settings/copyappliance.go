package settings

import (
	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/ref"
)

// Environment variables.
const (
	CopyApplianceSSHUser        = "COPY_APPLIANCE_SSH_USER"
	CopyApplianceContainerImage = "COPY_APPLIANCE_CONTAINER_IMAGE"
	CopyApplianceTLSSecret      = "COPY_APPLIANCE_TLS_SECRET"
	CopyApplianceResourcePool   = "COPY_APPLIANCE_RESOURCE_POOL"
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
	// Default vCenter resource pool inventory path for appliance VM clones.
	ResourcePool string
}

// Load settings.
func (r *CopyAppliance) Load() error {
	r.SSHUser = Lookup(CopyApplianceSSHUser, DefaultCopyApplianceSSHUser)
	r.ContainerImage = Lookup(CopyApplianceContainerImage, "")
	r.TLSSecret = Lookup(CopyApplianceTLSSecret, "")
	r.ResourcePool = Lookup(CopyApplianceResourcePool, "")

	return nil
}

// EnabledForPlan reports whether disk transfer for vmRef should use a copy
// appliance exporting source disks over NBD.
func (r *CopyAppliance) EnabledForPlan(p *api.Plan, vmRef ref.Ref) (bool, error) {
	if !Settings.Features.Toehold {
		return false, nil
	}
	if r.ContainerImage == "" || r.TLSSecret == "" {
		return false, nil
	}
	if !p.IsSourceProviderVSphere() {
		return false, nil
	}
	if p.IsUsingOffloadPlugin() {
		return false, nil
	}
	useV2v, err := p.ShouldUseV2vForTransfer(vmRef)
	if err != nil {
		return false, err
	}
	return !useV2v, nil
}
