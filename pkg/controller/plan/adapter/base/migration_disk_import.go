package base

import (
	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	planapi "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/plan"
	"github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/ref"
	"github.com/kubev2v/forklift/pkg/controller/plan/util"
	core "k8s.io/api/core/v1"
)

// MigrationDiskImportBuilder builds MigrationDiskImport CRs for warm migration.
type MigrationDiskImportBuilder interface {
	MigrationDiskImports(vmRef ref.Ref, secret *core.Secret, template *api.MigrationDiskImport, vddkConfigMap *core.ConfigMap) ([]api.MigrationDiskImport, error)
	ResolveMigrationDiskImportIdentifier(mdi *api.MigrationDiskImport) string
}

// MigrationDiskImportClient updates checkpoint state on MigrationDiskImport CRs.
type MigrationDiskImportClient interface {
	SetMigrationDiskImportCheckpoints(vmRef ref.Ref, precopies []planapi.Precopy, imports []api.MigrationDiskImport, final bool, hostsFunc util.HostsFunc) error
}

// AnnProvisionedByBlankDV marks PVCs provisioned by a blank CDI DataVolume.
const AnnProvisionedByBlankDV = "forklift.konveyor.io/provisioned-by"

// ProvisionedByBlankDVValue is the annotation value for blank-DV provisioned PVCs.
const ProvisionedByBlankDVValue = "blank-dv"

// LabelMigrationDiskImport labels MigrationDiskImport resources.
const LabelMigrationDiskImport = "forklift.konveyor.io/migration-disk-import"
