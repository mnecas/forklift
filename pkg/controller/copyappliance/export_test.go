package copyappliance

import (
	"slices"
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
)

func TestNeedsExportConvergence(t *testing.T) {
	appliance := &api.CopyAppliance{
		Spec: api.CopyApplianceSpec{
			ExportRequest: &api.ExportRequest{
				Target:     api.ExportTargetRelease,
				Generation: 2,
			},
		},
	}
	if !NeedsExportConvergence(appliance) {
		t.Fatal("expected convergence when observed export request is missing")
	}

	appliance.Status.ObservedExportRequest = &api.ExportRequest{
		Target:     api.ExportTargetExport,
		Generation: 2,
	}
	if !NeedsExportConvergence(appliance) {
		t.Fatal("expected convergence when target differs")
	}

	appliance.Status.ObservedExportRequest.Target = api.ExportTargetRelease
	if NeedsExportConvergence(appliance) {
		t.Fatal("did not expect convergence when request is observed")
	}
}

func TestExportRunner_BeginSetsReleasePhase(t *testing.T) {
	appliance := &api.CopyAppliance{
		Spec: api.CopyApplianceSpec{
			ExportRequest: &api.ExportRequest{
				Target:     api.ExportTargetRelease,
				Generation: 2,
			},
		},
		Status: api.CopyApplianceStatus{
			Phase: PhaseDeployCompleted,
		},
	}
	runner := ExportRunner{context: &ApplianceContext{Appliance: appliance}}
	if err := runner.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if appliance.Status.Phase != PhaseReleaseDisks {
		t.Fatalf("phase = %q, want %q", appliance.Status.Phase, PhaseReleaseDisks)
	}
}

func TestExportRunner_BeginSetsAttachPhase(t *testing.T) {
	appliance := &api.CopyAppliance{
		Spec: api.CopyApplianceSpec{
			ExportRequest: &api.ExportRequest{
				Target:     api.ExportTargetExport,
				Generation: 3,
			},
		},
		Status: api.CopyApplianceStatus{
			Phase: PhaseReleased,
		},
	}
	runner := ExportRunner{context: &ApplianceContext{Appliance: appliance}}
	if err := runner.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if appliance.Status.Phase != PhaseAttachDisks {
		t.Fatalf("phase = %q, want %q", appliance.Status.Phase, PhaseAttachDisks)
	}
}

// The two targets are disjoint routes rather than variations on one, so each
// gets its own table and the target alone picks between them. Both end on the
// phase Run treats as complete for that target, and neither walks to the
// failure phase, which is only ever reached by a step returning an error.
func TestExportItinerary(t *testing.T) {
	tests := []struct {
		name          string
		target        string
		want          []string
		wantCompleted string
	}{
		{
			name:   "releasing the disks",
			target: api.ExportTargetRelease,
			want: []string{
				PhaseReleaseDisks,
				PhaseWaitForReleaseDisks,
				PhaseReleased,
			},
			wantCompleted: PhaseReleased,
		},
		{
			name:   "exporting them again",
			target: api.ExportTargetExport,
			want: []string{
				PhaseAttachDisks,
				PhaseWaitForAttachDisks,
				PhaseRestartOrchestrator,
				PhaseWaitForExports,
				PhaseDeployCompleted,
			},
			wantCompleted: PhaseDeployCompleted,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			appliance := &api.CopyAppliance{
				Spec: api.CopyApplianceSpec{
					ExportRequest: &api.ExportRequest{Target: tc.target},
				},
			}
			runner := ExportRunner{context: &ApplianceContext{Appliance: appliance}}

			itinerary, completed, err := runner.itinerary()

			if err != nil {
				t.Fatalf("itinerary: %v", err)
			}
			got := phaseNames(t, itinerary)
			if !slices.Equal(got, tc.want) {
				t.Errorf("pipeline = %v, want %v", got, tc.want)
			}
			if completed != tc.wantCompleted {
				t.Errorf("completed phase = %q, want %q", completed, tc.wantCompleted)
			}
			if got[len(got)-1] != completed {
				t.Errorf("pipeline ends on %q, not on the completed phase %q",
					got[len(got)-1], completed)
			}
			if slices.Contains(got, PhaseDeployFailed) {
				t.Errorf("pipeline %v walks to %q", got, PhaseDeployFailed)
			}
		})
	}

	// Begin and Run both need the table before they can do anything, and there
	// is no table without a target to pick one with.
	t.Run("an unknown target has no itinerary", func(t *testing.T) {
		appliance := &api.CopyAppliance{
			Spec: api.CopyApplianceSpec{
				ExportRequest: &api.ExportRequest{Target: "Sideways"},
			},
		}
		runner := ExportRunner{context: &ApplianceContext{Appliance: appliance}}

		_, _, err := runner.itinerary()

		if err == nil {
			t.Fatal("itinerary accepted a target it cannot walk")
		}
	})
}
