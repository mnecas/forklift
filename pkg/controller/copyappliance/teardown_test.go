package copyappliance

import (
	"testing"
)

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

			runner := TeardownRunner{context: &ApplianceContext{Appliance: appliance}}
			runner.Begin()

			if appliance.Status.Phase != tc.wantPhase {
				t.Errorf("phase = %q, want %q", appliance.Status.Phase, tc.wantPhase)
			}
			if appliance.Status.TaskRef != "" {
				t.Errorf("task reference = %q, want it cleared", appliance.Status.TaskRef)
			}
		})
	}
}
