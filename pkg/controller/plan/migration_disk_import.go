package plan

import (
	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/settings"
)

// UsesMigrationDiskImport reports whether warm disk population uses MigrationDiskImport CRs.
func UsesMigrationDiskImport(p *api.Plan) bool {
	return settings.Settings.Features.MigrationDiskImport &&
		p.IsWarm() &&
		p.IsSourceProviderVSphere()
}
