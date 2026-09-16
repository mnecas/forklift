package copyappliance

import (
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/vim25"
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
