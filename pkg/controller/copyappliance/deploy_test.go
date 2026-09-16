package copyappliance

import (
	"context"
	"slices"
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
)

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

// A pass falls through as many steps as it can, so the phase it records is not
// the phase it started on. Recording the entry phase instead would send the
// next pass back to a step that is already done, and would tell an operator the
// appliance is waiting on something it is not.
func TestExecutePhaseRecordsTheStepItStoppedIn(t *testing.T) {
	private, public := testKeyPair(t)
	server := startSSHServer(t, public)
	// The appliance takes the configure login and then stops listening, which
	// leaves the load with nothing to answer it.
	server.stopAfterOne()
	ac := sshContext(t, private, server.addr)
	ac.Appliance.Status.Phase = PhaseConfigure
	runner := DeployRunner{context: ac}

	next, err := runner.ExecutePhase(context.TODO())
	if err != nil {
		t.Fatalf("ExecutePhase: %v", err)
	}
	if !slices.Equal(server.Ran(), applianceConfigCommands) {
		t.Fatalf("ran %v, want the appliance configured and nothing more", server.Ran())
	}
	if next != PhaseLoadImage {
		t.Errorf("phase = %q, want %q: the pass configured the appliance and stopped in the load",
			next, PhaseLoadImage)
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
