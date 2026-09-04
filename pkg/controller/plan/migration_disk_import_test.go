package plan

import (
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/settings"
)

func TestUsesMigrationDiskImport(t *testing.T) {
	orig := settings.Settings.Features.MigrationDiskImport
	defer func() { settings.Settings.Features.MigrationDiskImport = orig }()

	settings.Settings.Features.MigrationDiskImport = true
	p := &api.Plan{
		Spec: api.PlanSpec{
			Type: api.MigrationWarm,
		},
	}
	vsphereType := api.VSphere
	p.Provider.Source = &api.Provider{
		Spec: api.ProviderSpec{
			Type: &vsphereType,
		},
	}
	if !UsesMigrationDiskImport(p) {
		t.Fatal("expected warm vSphere plan to use MigrationDiskImport when gate is on")
	}

	settings.Settings.Features.MigrationDiskImport = false
	if UsesMigrationDiskImport(p) {
		t.Fatal("expected gate off to disable MigrationDiskImport path")
	}
}
