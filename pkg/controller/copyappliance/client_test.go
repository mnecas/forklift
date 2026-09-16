package copyappliance

import (
	"reflect"
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

// What the guest reports is not what the appliance can be reached at. An
// adapter is described before it holds an address, and it gives itself a
// link-local one on the way to holding a real one.
func TestCollectAddresses(t *testing.T) {
	tests := []struct {
		name string
		nics []types.GuestNicInfo
		want []api.ApplianceAddress
	}{
		{
			name: "a guest that has reported nothing yields no addresses",
			nics: nil,
			want: nil,
		},
		{
			name: "an adapter with no IP configuration is skipped",
			nics: []types.GuestNicInfo{guestNIC("VM Network", "00:50:56:01:02:03")},
			want: nil,
		},
		{
			name: "the portgroup name and MAC come from the adapter",
			nics: []types.GuestNicInfo{
				guestNIC("VM Network", "00:50:56:01:02:03", "192.0.2.10"),
			},
			want: []api.ApplianceAddress{
				{Network: "VM Network", MAC: "00:50:56:01:02:03", IP: "192.0.2.10"},
			},
		},
		{
			name: "a MAC is reported in lower case",
			nics: []types.GuestNicInfo{
				guestNIC("VM Network", "00:50:56:AB:CD:EF", "192.0.2.10"),
			},
			want: []api.ApplianceAddress{
				{Network: "VM Network", MAC: "00:50:56:ab:cd:ef", IP: "192.0.2.10"},
			},
		},
		{
			name: "every address on an adapter is reported",
			nics: []types.GuestNicInfo{
				guestNIC("VM Network", "00:50:56:01:02:03", "192.0.2.10", "2001:db8::1"),
			},
			want: []api.ApplianceAddress{
				{Network: "VM Network", MAC: "00:50:56:01:02:03", IP: "192.0.2.10"},
				{Network: "VM Network", MAC: "00:50:56:01:02:03", IP: "2001:db8::1"},
			},
		},
		{
			name: "every adapter is reported",
			nics: []types.GuestNicInfo{
				guestNIC("VM Network", "00:50:56:01:02:03", "192.0.2.10"),
				guestNIC("Transfer Network", "00:50:56:04:05:06", "198.51.100.10"),
			},
			want: []api.ApplianceAddress{
				{Network: "VM Network", MAC: "00:50:56:01:02:03", IP: "192.0.2.10"},
				{Network: "Transfer Network", MAC: "00:50:56:04:05:06", IP: "198.51.100.10"},
			},
		},
		{
			name: "a link-local address is not reported",
			nics: []types.GuestNicInfo{
				guestNIC("VM Network", "00:50:56:01:02:03", "169.254.1.1", "fe80::1", "192.0.2.10"),
			},
			want: []api.ApplianceAddress{
				{Network: "VM Network", MAC: "00:50:56:01:02:03", IP: "192.0.2.10"},
			},
		},
		{
			name: "a loopback address is not reported",
			nics: []types.GuestNicInfo{
				guestNIC("VM Network", "00:50:56:01:02:03", "127.0.0.1", "::1"),
			},
			want: nil,
		},
		{
			name: "an address the guest garbled is not reported",
			nics: []types.GuestNicInfo{
				guestNIC("VM Network", "00:50:56:01:02:03", "", "not-an-address"),
			},
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := collectAddresses(tc.nics)

			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("collectAddresses() = %+v, want %+v", got, tc.want)
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
