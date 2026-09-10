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
	if applied.ManagementNetwork != DefaultCopyApplianceManagementNetwork {
		t.Errorf("ManagementNetwork = %q, want %q",
			applied.ManagementNetwork, DefaultCopyApplianceManagementNetwork)
	}
	// There is no default: the path names a file in the deployment's own
	// datastore, and the builder refuses to run until it is set.
	if applied.RootDiskPath != "" {
		t.Errorf("RootDiskPath = %q, want it unset", applied.RootDiskPath)
	}
	// Nor here: a deployment without a dedicated transfer network gets an
	// appliance with only its management NIC.
	if applied.TransferNetwork != "" {
		t.Errorf("TransferNetwork = %q, want it unset", applied.TransferNetwork)
	}
}

func TestCopyApplianceFromEnvironment(t *testing.T) {
	t.Setenv(CopyApplianceGuestId, "rhel9_64Guest")
	t.Setenv(CopyApplianceNumCPUs, "4")
	t.Setenv(CopyApplianceMemoryMB, "8192")
	t.Setenv(CopyApplianceRootDiskPath, "[datastore1] images/appliance-root.vmdk")
	t.Setenv(CopyApplianceManagementNetwork, "/DC0/network/Management")
	t.Setenv(CopyApplianceTransferNetwork, "/DC0/network/Transfer")

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
	if applied.ManagementNetwork != "/DC0/network/Management" {
		t.Errorf("ManagementNetwork = %q, want the configured network", applied.ManagementNetwork)
	}
	if applied.TransferNetwork != "/DC0/network/Transfer" {
		t.Errorf("TransferNetwork = %q, want the configured network", applied.TransferNetwork)
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
