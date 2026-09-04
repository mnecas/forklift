package diskimporter

import (
	"context"
	"fmt"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
)

// TransferBackend copies data from a source into the target disk image.
type TransferBackend interface {
	Type() api.DiskTransferType
	Run(ctx context.Context, cfg Config) error
}

// NewBackend returns the transfer backend for the configured type.
func NewBackend(transferType string) (TransferBackend, error) {
	switch api.DiskTransferType(transferType) {
	case api.TransferVDDK:
		return &VDDKBackend{}, nil
	case api.TransferNFC:
		return nil, fmt.Errorf("NFC transfer is not implemented yet")
	case api.TransferToehold:
		return nil, fmt.Errorf("Toehold transfer is not implemented yet")
	default:
		return nil, fmt.Errorf("unknown transfer type %q", transferType)
	}
}
