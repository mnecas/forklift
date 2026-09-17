package copyappliance

import (
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

func TestExportRequestObserved(t *testing.T) {
	appliance := &api.CopyAppliance{
		Spec: api.CopyApplianceSpec{
			ExportRequest: &api.ExportRequest{
				Target:     api.ExportTargetExport,
				Generation: 3,
			},
		},
		Status: api.CopyApplianceStatus{
			ObservedExportRequest: &api.ExportRequest{
				Target:     api.ExportTargetExport,
				Generation: 3,
			},
		},
	}
	if !ExportRequestObserved(appliance) {
		t.Fatal("expected export request to be observed")
	}
}

func TestIsExportPhase(t *testing.T) {
	if !IsExportPhase(PhaseReleaseDisks) {
		t.Fatal("expected release disks to be an export phase")
	}
	if IsExportPhase(PhaseWaitForExports) {
		t.Fatal("deploy and export both use WaitForExports; routing is separate")
	}
	if IsExportPhase(PhaseDeployCompleted) {
		t.Fatal("did not expect deploy completed to be an export phase")
	}
}

func TestRoutesToExportRunner(t *testing.T) {
	deployWait := &api.CopyAppliance{Status: api.CopyApplianceStatus{Phase: PhaseWaitForExports}}
	if RoutesToExportRunner(deployWait) {
		t.Fatal("deploy WaitForExports should stay on DeployRunner")
	}

	exportWait := &api.CopyAppliance{
		Spec: api.CopyApplianceSpec{
			ExportRequest: &api.ExportRequest{Target: api.ExportTargetExport, Generation: 1},
		},
		Status: api.CopyApplianceStatus{Phase: PhaseWaitForExports},
	}
	if !RoutesToExportRunner(exportWait) {
		t.Fatal("export WaitForExports should use ExportRunner")
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
	runner.Begin()
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
	runner.Begin()
	if appliance.Status.Phase != PhaseAttachDisks {
		t.Fatalf("phase = %q, want %q", appliance.Status.Phase, PhaseAttachDisks)
	}
}
