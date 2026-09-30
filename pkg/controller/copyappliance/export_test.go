package copyappliance

import (
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
)

func TestPendingExportRequest(t *testing.T) {
	appliance := &api.CopyAppliance{
		Spec: api.CopyApplianceSpec{
			Target: api.ExportTargetRelease,
		},
		Status: api.CopyApplianceStatus{
			Phase: PhaseDeployCompleted,
		},
	}
	if !PendingExportRequest(appliance) {
		t.Fatal("expected pending when DeployCompleted and Release requested")
	}

	appliance.Spec.Target = api.ExportTargetExport
	if PendingExportRequest(appliance) {
		t.Fatal("did not expect pending when DeployCompleted and Export requested")
	}

	appliance.Status.Phase = PhaseReleased
	if !PendingExportRequest(appliance) {
		t.Fatal("expected pending when Released and Export requested")
	}

	appliance.Spec.Target = api.ExportTargetRelease
	if PendingExportRequest(appliance) {
		t.Fatal("did not expect pending when Released and Release requested")
	}

	appliance.Status.Phase = PhaseAttachDisks
	appliance.Spec.Target = api.ExportTargetExport
	if PendingExportRequest(appliance) {
		t.Fatal("did not expect pending while already in the export pipeline")
	}
}

func TestExportRunner_BeginSetsReleasePhase(t *testing.T) {
	appliance := &api.CopyAppliance{
		Spec: api.CopyApplianceSpec{
			Target: api.ExportTargetRelease,
		},
		Status: api.CopyApplianceStatus{
			Phase: PhaseDeployCompleted,
		},
	}
	runner := ExportRunner{context: &ApplianceContext{Appliance: appliance}}
	if err := runner.begin(); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if appliance.Status.Phase != PhaseReleaseDisks {
		t.Fatalf("phase = %q, want %q", appliance.Status.Phase, PhaseReleaseDisks)
	}
}

func TestExportRunner_BeginSetsAttachPhase(t *testing.T) {
	appliance := &api.CopyAppliance{
		Spec: api.CopyApplianceSpec{
			Target: api.ExportTargetExport,
		},
		Status: api.CopyApplianceStatus{
			Phase: PhaseReleased,
		},
	}
	runner := ExportRunner{context: &ApplianceContext{Appliance: appliance}}
	if err := runner.begin(); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if appliance.Status.Phase != PhaseAttachDisks {
		t.Fatalf("phase = %q, want %q", appliance.Status.Phase, PhaseAttachDisks)
	}
}

func TestExportRunner_BeginUnknownTarget(t *testing.T) {
	appliance := &api.CopyAppliance{
		Spec: api.CopyApplianceSpec{
			Target: "Sideways",
		},
	}
	runner := ExportRunner{context: &ApplianceContext{Appliance: appliance}}
	if err := runner.begin(); err == nil {
		t.Fatal("begin accepted an unknown target")
	}
	if appliance.Status.Phase != PhaseDeployFailed {
		t.Fatalf("phase = %q, want %q", appliance.Status.Phase, PhaseDeployFailed)
	}
}
