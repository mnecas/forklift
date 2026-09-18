package settings

import (
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/plan"
	"github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/ref"
)

func TestCopyApplianceEnabledForPlan(t *testing.T) {
	Settings.Features.Toehold = true
	Settings.CopyAppliance.ContainerImage = "copy-appliance:latest"

	vsphere, openshift := api.VSphere, api.OpenShift
	p := &api.Plan{
		Spec: api.PlanSpec{
			Type:              api.MigrationCold,
			MigrateSharedDisks: true,
			VMs: []plan.VM{{
				Ref: ref.Ref{ID: "vm-1", Name: "vm-1"},
			}},
		},
		Referenced: api.Referenced{
			Provider: struct {
				Source, Destination *api.Provider
			}{
				Source:      &api.Provider{Spec: api.ProviderSpec{Type: &vsphere}},
				Destination: &api.Provider{Spec: api.ProviderSpec{Type: &openshift, URL: "https://remote.example.com"}},
			},
		},
	}

	if !Settings.CopyAppliance.EnabledForPlan(p) {
		t.Fatal("expected copy appliance path for vSphere cold migration with toehold enabled")
	}

	Settings.Features.Toehold = false
	if Settings.CopyAppliance.EnabledForPlan(p) {
		t.Fatal("expected copy appliance disabled when toehold feature is off")
	}
}
