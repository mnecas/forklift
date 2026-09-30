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
	t.Run("the pipeline walks every VM step then completed", func(t *testing.T) {
		got := phaseNames(t, teardownFor(testAppliance()).Itinerary())

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
		got := phaseNames(t, teardownFor(testAppliance()).Itinerary())

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
