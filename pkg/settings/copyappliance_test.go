package settings

import (
	"testing"
)

func TestCopyApplianceDefaults(t *testing.T) {
	applied := CopyAppliance{}
	if err := applied.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if applied.SSHUser != DefaultCopyApplianceSSHUser {
		t.Errorf("SSHUser = %q, want %q", applied.SSHUser, DefaultCopyApplianceSSHUser)
	}
	// Nor here: the image stream exists only in the deployment's own cluster,
	// and the builder refuses to run until it is named.
	if applied.ContainerImage != "" {
		t.Errorf("ContainerImage = %q, want it unset", applied.ContainerImage)
	}
	// Nor here: the certificates are the deployment's own, and nothing the
	// controller could invent would be trusted by anything.
	if applied.TLSSecret != "" {
		t.Errorf("TLSSecret = %q, want it unset", applied.TLSSecret)
	}
}

func TestCopyApplianceFromEnvironment(t *testing.T) {
	t.Setenv(CopyApplianceSSHUser, "appliance")
	t.Setenv(CopyApplianceContainerImage, "copy-appliance:latest")
	t.Setenv(CopyApplianceTLSSecret, "copy-appliance-tls")
	t.Setenv(CopyApplianceResourcePool, "/Datacenter/host/cluster/Resources")

	applied := CopyAppliance{}
	if err := applied.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if applied.SSHUser != "appliance" {
		t.Errorf("SSHUser = %q, want the configured user", applied.SSHUser)
	}
	if applied.ContainerImage != "copy-appliance:latest" {
		t.Errorf("ContainerImage = %q, want the configured image", applied.ContainerImage)
	}
	if applied.TLSSecret != "copy-appliance-tls" {
		t.Errorf("TLSSecret = %q, want the configured secret", applied.TLSSecret)
	}
	if applied.ResourcePool != "/Datacenter/host/cluster/Resources" {
		t.Errorf("ResourcePool = %q, want the configured pool", applied.ResourcePool)
	}
}
