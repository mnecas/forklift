package settings

import (
	"testing"
)

func TestCopyApplianceDefaults(t *testing.T) {
	applied := CopyAppliance{}
	if err := applied.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if applied.GuestId != DefaultCopyApplianceGuestId {
		t.Errorf("GuestId = %q, want %q", applied.GuestId, DefaultCopyApplianceGuestId)
	}
	if applied.NumCPUs != DefaultCopyApplianceNumCPUs {
		t.Errorf("NumCPUs = %d, want %d", applied.NumCPUs, DefaultCopyApplianceNumCPUs)
	}
	if applied.MemoryMB != DefaultCopyApplianceMemoryMB {
		t.Errorf("MemoryMB = %d, want %d", applied.MemoryMB, DefaultCopyApplianceMemoryMB)
	}
	if applied.SSHUser != DefaultCopyApplianceSSHUser {
		t.Errorf("SSHUser = %q, want %q", applied.SSHUser, DefaultCopyApplianceSSHUser)
	}
	// There is no default: the path names a file in the deployment's own
	// datastore, and the builder refuses to run until it is set.
	if applied.RootDiskPath != "" {
		t.Errorf("RootDiskPath = %q, want it unset", applied.RootDiskPath)
	}
	// Nor here: the secret exists only in the deployment's own cluster, and
	// the builder refuses to run until it is named.
	if applied.SSHKeySecret != "" {
		t.Errorf("SSHKeySecret = %q, want it unset", applied.SSHKeySecret)
	}
}

func TestCopyApplianceFromEnvironment(t *testing.T) {
	t.Setenv(CopyApplianceGuestId, "rhel9_64Guest")
	t.Setenv(CopyApplianceNumCPUs, "4")
	t.Setenv(CopyApplianceMemoryMB, "8192")
	t.Setenv(CopyApplianceRootDiskPath, "[datastore1] images/appliance-root.vmdk")
	t.Setenv(CopyApplianceSSHKeySecret, "copy-appliance-ssh-key")
	t.Setenv(CopyApplianceSSHUser, "appliance")

	applied := CopyAppliance{}
	if err := applied.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if applied.GuestId != "rhel9_64Guest" {
		t.Errorf("GuestId = %q, want rhel9_64Guest", applied.GuestId)
	}
	if applied.NumCPUs != 4 {
		t.Errorf("NumCPUs = %d, want 4", applied.NumCPUs)
	}
	if applied.MemoryMB != 8192 {
		t.Errorf("MemoryMB = %d, want 8192", applied.MemoryMB)
	}
	if applied.RootDiskPath != "[datastore1] images/appliance-root.vmdk" {
		t.Errorf("RootDiskPath = %q, want the configured path", applied.RootDiskPath)
	}
	if applied.SSHKeySecret != "copy-appliance-ssh-key" {
		t.Errorf("SSHKeySecret = %q, want the configured secret", applied.SSHKeySecret)
	}
	if applied.SSHUser != "appliance" {
		t.Errorf("SSHUser = %q, want the configured user", applied.SSHUser)
	}
}

// A CPU or memory value the CRD would reject has to fail at startup, where the
// operator sees it, rather than at admission on every appliance.
func TestCopyApplianceRejectsBadSizes(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		value string
	}{
		{"non-integer CPUs", CopyApplianceNumCPUs, "two"},
		{"zero CPUs", CopyApplianceNumCPUs, "0"},
		{"negative CPUs", CopyApplianceNumCPUs, "-1"},
		{"non-integer memory", CopyApplianceMemoryMB, "8Gi"},
		{"zero memory", CopyApplianceMemoryMB, "0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.env, tc.value)
			applied := CopyAppliance{}
			if err := applied.Load(); err == nil {
				t.Fatalf("Load accepted %s=%q", tc.env, tc.value)
			}
		})
	}
}
