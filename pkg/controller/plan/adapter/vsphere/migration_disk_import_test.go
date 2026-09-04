package vsphere

import (
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	planapi "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/plan"
	"github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/ref"
	planbase "github.com/kubev2v/forklift/pkg/controller/plan/adapter/base"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSetMigrationDiskImportCheckpoints(t *testing.T) {
	client := &Client{}
	imports := []api.MigrationDiskImport{{
		ObjectMeta: meta.ObjectMeta{
			Annotations: map[string]string{planbase.AnnDiskSource: "[ds] vm/disk.vmdk"},
		},
	}}
	first := planapi.Precopy{Snapshot: "snap-0"}
	first.WithDeltas(map[string]string{"[ds] vm/disk.vmdk": "change-0"})
	precopies := []planapi.Precopy{
		first,
		{Snapshot: "snap-1"},
	}
	err := client.SetMigrationDiskImportCheckpoints(ref.Ref{}, precopies, imports, false, nil)
	if err != nil {
		t.Fatalf("SetMigrationDiskImportCheckpoints failed: %v", err)
	}
	if len(imports[0].Spec.Checkpoints) != 1 {
		t.Fatalf("expected one checkpoint, got %d", len(imports[0].Spec.Checkpoints))
	}
	checkpoint := imports[0].Spec.Checkpoints[0]
	if checkpoint.Current != "snap-1" || checkpoint.Previous != "change-0" {
		t.Fatalf("unexpected checkpoint %+v", checkpoint)
	}
}
