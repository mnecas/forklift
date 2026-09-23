package copyappliance

import (
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/types"
)

// testContext is an ApplianceContext that reports the given vCenter instance
// UUID without connecting to anything.
func testContext(appliance *api.CopyAppliance, instanceUUID string) *ApplianceContext {
	vcenter := &govmomi.Client{Client: new(vim25.Client)}
	vcenter.ServiceContent.About.InstanceUuid = instanceUUID
	return &ApplianceContext{
		Appliance: appliance,
		VCenter:   vcenter,
		Log:       testLog(),
	}
}

// guestNIC is one adapter as VMware Tools reports it. A nil ips means the
// guest has told us about the adapter but not about any address on it.
func guestNIC(network, mac string, ips ...string) types.GuestNicInfo {
	nic := types.GuestNicInfo{
		Network:    network,
		MacAddress: mac,
	}
	if ips == nil {
		return nic
	}
	nic.IpConfig = &types.NetIpConfigInfo{}
	for _, ip := range ips {
		nic.IpConfig.IpAddress = append(
			nic.IpConfig.IpAddress,
			types.NetIpConfigInfoIpAddress{IpAddress: ip})
	}
	return nic
}

// Both the wait and the login go through this, so an appliance that reports
// nothing must read as "not yet" and not as an empty address to dial.
func TestApplianceAddress(t *testing.T) {
	tests := []struct {
		name      string
		addresses []api.ApplianceAddress
		want      string
		wantOK    bool
	}{
		{"the address the guest reports",
			[]api.ApplianceAddress{
				{Network: "VM Network", MAC: "00:50:56:01:02:03", IP: "192.0.2.10"},
			},
			"192.0.2.10", true},
		// One adapter is reported once per address it holds, and the appliance
		// answers on any of them.
		{"an adapter with more than one address gives the first",
			[]api.ApplianceAddress{
				{Network: "VM Network", MAC: "00:50:56:01:02:03", IP: "192.0.2.10"},
				{Network: "VM Network", MAC: "00:50:56:01:02:03", IP: "2001:db8::1"},
			},
			"192.0.2.10", true},
		{"no addresses at all", nil, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := applianceAddress(tc.addresses)

			if got != tc.want || ok != tc.wantOK {
				t.Errorf("applianceAddress(%+v) = (%q, %v), want (%q, %v)",
					tc.addresses, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// A runner that acted on a moRef recorded against another vCenter would be
// operating on a stranger's VM.
func TestCheckInstance(t *testing.T) {
	tests := []struct {
		name      string
		recorded  string
		connected string
		wantErr   bool
	}{
		{
			name:      "the same vCenter is accepted",
			recorded:  "uuid-a",
			connected: "uuid-a",
			wantErr:   false,
		},
		{
			name:      "a different vCenter is refused",
			recorded:  "uuid-a",
			connected: "uuid-b",
			wantErr:   true,
		},
		{
			name:      "an appliance recorded before the UUID was tracked is adopted",
			recorded:  "",
			connected: "uuid-b",
			wantErr:   false,
		},
		{
			name:      "an unreadable connection UUID is not evidence of a different vCenter",
			recorded:  "uuid-a",
			connected: "",
			wantErr:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			appliance := testAppliance()
			appliance.Status.VCenterInstanceUUID = tc.recorded

			err := testContext(appliance, tc.connected).CheckInstance()

			if (err != nil) != tc.wantErr {
				t.Errorf("CheckInstance = %v, want error: %v", err, tc.wantErr)
			}
		})
	}
}
