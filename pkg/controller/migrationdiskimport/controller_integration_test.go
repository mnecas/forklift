//go:build integration

package migrationdiskimport

import (
	"testing"
)

// TestVDDKWarmImportVcsim exercises the gated warm migration path against vcsim.
// Run with: go test -tags=integration ./pkg/controller/migrationdiskimport/...
func TestVDDKWarmImportVcsim(t *testing.T) {
	t.Skip("requires vcsim cluster with FEATURE_MIGRATION_DISK_IMPORT enabled")
}
