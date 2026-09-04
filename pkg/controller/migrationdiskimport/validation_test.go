package migrationdiskimport

import (
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	core "k8s.io/api/core/v1"
)

func TestValidateSpecVDDK(t *testing.T) {
	spec := &api.MigrationDiskImportSpec{
		Target: core.ObjectReference{Name: "target-pvc"},
		Transfer: api.DiskTransfer{
			Type: api.TransferVDDK,
			VDDK: &api.VDDKTransfer{
				URL:         "https://vcenter.example",
				UUID:        "vm-uuid",
				BackingFile: "[datastore] vm/disk.vmdk",
				SecretRef:   "vddk-secret",
			},
		},
	}
	if err := validateSpec(spec); err != nil {
		t.Fatalf("expected valid VDDK spec, got %v", err)
	}
}

func TestValidateSpecRejectsUnimplementedBackends(t *testing.T) {
	for _, transferType := range []api.DiskTransferType{api.TransferNFC, api.TransferToehold} {
		spec := &api.MigrationDiskImportSpec{
			Target: core.ObjectReference{Name: "target-pvc"},
			Transfer: api.DiskTransfer{
				Type: transferType,
			},
		}
		if err := validateSpec(spec); err == nil {
			t.Fatalf("expected error for transfer type %s", transferType)
		}
	}
}

func TestValidateSpecRequiresMatchingVDDKBlock(t *testing.T) {
	spec := &api.MigrationDiskImportSpec{
		Target: core.ObjectReference{Name: "target-pvc"},
		Transfer: api.DiskTransfer{
			Type: api.TransferVDDK,
		},
	}
	if err := validateSpec(spec); err == nil {
		t.Fatal("expected error when VDDK block is missing")
	}
}
