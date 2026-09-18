package copyappliance

import (
	"slices"
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
)

// The pipeline is the order the pass walks in, so it has to agree with the
// order execute implements. The failure phase is in no pipeline: it is not a
// step the walk arrives at, it is where the walk ends when a step errors.
func TestTeardownItinerary(t *testing.T) {
	// Every step but the last acts on a VM, so an appliance that never recorded
	// one has nothing to walk and is already torn down.
	t.Run("an appliance with no VM has only the completed step", func(t *testing.T) {
		runner := teardownFor(testAppliance())

		got := phaseNames(t, runner.Itinerary())

		if want := []string{PhaseTeardownCompleted}; !slices.Equal(got, want) {
			t.Errorf("pipeline = %v, want %v", got, want)
		}
	})

	t.Run("an appliance with a VM walks every step", func(t *testing.T) {
		appliance := testAppliance()
		appliance.Status.MoRef = "vm-42"
		runner := teardownFor(appliance)

		got := phaseNames(t, runner.Itinerary())

		want := []string{
			PhasePowerOff,
			PhaseWaitForPowerOff,
			PhaseDetachDisks,
			PhaseWaitForDetachDisks,
			PhaseDestroyVM,
			PhaseWaitForDestroyVM,
			PhaseTeardownCompleted,
		}
		if !slices.Equal(got, want) {
			t.Errorf("pipeline = %v, want %v", got, want)
		}
	})

	t.Run("the failure phase is not a step", func(t *testing.T) {
		appliance := testAppliance()
		appliance.Status.MoRef = "vm-42"

		got := phaseNames(t, teardownFor(appliance).Itinerary())

		if slices.Contains(got, PhaseTeardownFailed) {
			t.Errorf("pipeline %v walks to %q", got, PhaseTeardownFailed)
		}
	})
}

func teardownFor(appliance *api.CopyAppliance) *TeardownRunner {
	return &TeardownRunner{context: &ApplianceContext{Appliance: appliance}}
}

// An appliance whose teardown never reaches TeardownCompleted keeps its
// finalizer forever, so Begin has to have an answer for every state a deploy
// can have left behind.
func TestTeardownBegin(t *testing.T) {
	tests := []struct {
		name      string
		moRef     string
		taskRef   string
		phase     string
		wantPhase string
	}{
		{
			name:      "teardown is already complete when no VM was recorded",
			phase:     PhaseDeployFailed,
			wantPhase: PhaseTeardownCompleted,
		},
		{
			name:      "teardown starts at power off when a VM was recorded",
			moRef:     "vm-42",
			phase:     PhaseWaitForClone,
			wantPhase: PhasePowerOff,
		},
		{
			name:      "a task left behind by the deploy is not waited on",
			moRef:     "vm-42",
			taskRef:   "task-7",
			phase:     PhaseWaitForClone,
			wantPhase: PhasePowerOff,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			appliance := testAppliance()
			appliance.Status.MoRef = tc.moRef
			appliance.Status.TaskRef = tc.taskRef
			appliance.Status.Phase = tc.phase

			runner := teardownFor(appliance)
			if err := runner.Begin(); err != nil {
				t.Fatalf("Begin: %v", err)
			}

			if appliance.Status.Phase != tc.wantPhase {
				t.Errorf("phase = %q, want %q", appliance.Status.Phase, tc.wantPhase)
			}
			if appliance.Status.TaskRef != "" {
				t.Errorf("task reference = %q, want it cleared", appliance.Status.TaskRef)
			}
		})
	}
}
