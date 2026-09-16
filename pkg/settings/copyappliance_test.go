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
	// There is no default: the secret exists only in the deployment's own
	// cluster, and the builder refuses to run until it is named.
	if applied.SSHKeySecret != "" {
		t.Errorf("SSHKeySecret = %q, want it unset", applied.SSHKeySecret)
	}
	// Nor here: the image stream exists only in the deployment's own cluster,
	// and the builder refuses to run until it is named.
	if applied.ContainerImage != "" {
		t.Errorf("ContainerImage = %q, want it unset", applied.ContainerImage)
	}
}

func TestCopyApplianceFromEnvironment(t *testing.T) {
	t.Setenv(CopyApplianceSSHKeySecret, "copy-appliance-ssh-key")
	t.Setenv(CopyApplianceSSHUser, "appliance")
	t.Setenv(CopyApplianceContainerImage, "copy-appliance:latest")

	applied := CopyAppliance{}
	if err := applied.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if applied.SSHKeySecret != "copy-appliance-ssh-key" {
		t.Errorf("SSHKeySecret = %q, want the configured secret", applied.SSHKeySecret)
	}
	if applied.SSHUser != "appliance" {
		t.Errorf("SSHUser = %q, want the configured user", applied.SSHUser)
	}
	if applied.ContainerImage != "copy-appliance:latest" {
		t.Errorf("ContainerImage = %q, want the configured image", applied.ContainerImage)
	}
}
