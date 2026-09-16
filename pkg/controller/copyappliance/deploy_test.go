package copyappliance

import (
	"testing"
)

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
