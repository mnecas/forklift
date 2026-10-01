package copyappliance

import (
	"context"
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
)

// Configure used to run before LoadImage and now runs after it. An appliance
// that an older controller left in the configure phase has no loaded image, and
// nothing in the itinerary walks backwards, so without the shim it would
// install a supervisor with no image to run and then wait forever for exports
// that cannot appear.
func TestRunSendsBackAnApplianceThatSkippedTheLoad(t *testing.T) {
	ac := sshContext(t, nil, closedAddr(t))
	ac.Appliance.Status.Phase = api.PhaseConfigure
	runner := DeployRunner{context: ac}

	_, err := runner.Run(context.TODO())

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ac.Appliance.Status.Phase != api.PhaseLoadImage {
		t.Errorf("phase = %q, want %q", ac.Appliance.Status.Phase, api.PhaseLoadImage)
	}
}

func TestDeployBegin(t *testing.T) {
	t.Run("deploy starts at clone", func(t *testing.T) {
		appliance := testAppliance()
		appliance.Status.TaskRef = "task-7"

		runner := DeployRunner{context: testContext(appliance, "uuid-a")}
		if err := runner.begin(); err != nil {
			t.Fatalf("begin: %v", err)
		}

		if appliance.Status.Phase != api.PhaseCloneVM {
			t.Errorf("phase = %q, want %q", appliance.Status.Phase, api.PhaseCloneVM)
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
		if err := runner.begin(); err != nil {
			t.Fatalf("begin: %v", err)
		}

		if appliance.Status.VCenterInstanceUUID != "uuid-a" {
			t.Errorf("recorded vCenter = %q, want %q", appliance.Status.VCenterInstanceUUID, "uuid-a")
		}
	})
}

func TestWaitForCloneAcceptsAdoptedMoRef(t *testing.T) {
	appliance := testAppliance()
	appliance.Status.MoRef = "vm-42"
	runner := DeployRunner{context: testContext(appliance, "uuid-a")}

	done, err := runner.WaitForClone(context.TODO())
	if err != nil {
		t.Fatalf("WaitForClone: %v", err)
	}
	if !done {
		t.Error("WaitForClone = false, want true when MoRef is set with no task")
	}
}
