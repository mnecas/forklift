package migrationdiskimport

import (
	"fmt"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
)

func validateSpec(spec *api.MigrationDiskImportSpec) error {
	if spec.Target.Name == "" {
		return fmt.Errorf("spec.target.name is required")
	}
	switch spec.Transfer.Type {
	case api.TransferVDDK:
		if spec.Transfer.VDDK == nil {
			return fmt.Errorf("spec.transfer.vddk is required when type is VDDK")
		}
		v := spec.Transfer.VDDK
		if v.URL == "" || v.UUID == "" || v.BackingFile == "" || v.SecretRef == "" {
			return fmt.Errorf("vddk transfer requires url, uuid, backingFile, and secretRef")
		}
	case api.TransferNFC:
		return fmt.Errorf("NFC transfer is not implemented yet")
	case api.TransferToehold:
		return fmt.Errorf("Toehold transfer is not implemented yet")
	default:
		return fmt.Errorf("unknown transfer type %q", spec.Transfer.Type)
	}
	return nil
}
