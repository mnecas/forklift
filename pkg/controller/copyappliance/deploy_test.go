package copyappliance

import (
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
)

// The spec names the appliance's networks the way the vSphere finder takes
// them; the guest names them the way it was told. Deploy completes on the two
// agreeing, so a mismatch here parks the appliance forever.
func TestReportsNetwork(t *testing.T) {
	addresses := []api.ApplianceAddress{
		{Network: "VM Network", MAC: "00:50:56:01:02:03", IP: "192.0.2.10"},
		{Network: "Transfer Network", MAC: "00:50:56:04:05:06", IP: "198.51.100.10"},
	}
	tests := []struct {
		name      string
		addresses []api.ApplianceAddress
		network   string
		want      bool
	}{
		{"an address on the network is found", addresses, "VM Network", true},
		{"a network named by its inventory path matches the name the guest reports",
			addresses, "/DC0/network/Transfer Network", true},
		{"a network with no address is not found", addresses, "Storage Network", false},
		{"no addresses at all", nil, "VM Network", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := reportsNetwork(tc.addresses, tc.network)

			if got != tc.want {
				t.Errorf("reportsNetwork(%q) = %v, want %v", tc.network, got, tc.want)
			}
		})
	}
}

func TestDeployBegin(t *testing.T) {
	t.Run("deploy starts at clone", func(t *testing.T) {
		appliance := testAppliance()
		appliance.Status.TaskRef = "task-7"

		runner := DeployRunner{context: testContext(appliance, "uuid-a")}
		runner.Begin()

		if appliance.Status.Phase != PhaseCloneVM {
			t.Errorf("phase = %q, want %q", appliance.Status.Phase, PhaseCloneVM)
		}
		if appliance.Status.TaskRef != "" {
			t.Errorf("task reference = %q, want it cleared", appliance.Status.TaskRef)
		}
	})

	// A moRef means nothing without the vCenter it was recorded against, and
	// the clone that follows is what produces one.
	t.Run("the connected vCenter is recorded", func(t *testing.T) {
		appliance := testAppliance()

		runner := DeployRunner{context: testContext(appliance, "uuid-a")}
		runner.Begin()

		if appliance.Status.VCenterInstanceUUID != "uuid-a" {
			t.Errorf("recorded vCenter = %q, want %q", appliance.Status.VCenterInstanceUUID, "uuid-a")
		}
	})
}
