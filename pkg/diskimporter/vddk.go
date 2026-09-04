package diskimporter

import (
	"context"
	"fmt"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/diskimporter/vddkimport"
)

// VDDKBackend runs warm migration disk import using CDI's VDDK importer logic in-process.
type VDDKBackend struct{}

func (b *VDDKBackend) Type() api.DiskTransferType {
	return api.TransferVDDK
}

func (b *VDDKBackend) Run(ctx context.Context, cfg Config) error {
	if cfg.VDDK.URL == "" || cfg.VDDK.UUID == "" || cfg.VDDK.BackingFile == "" {
		return fmt.Errorf("vddk transfer requires url, uuid, and backing file")
	}
	if cfg.VDDK.AccessKey == "" || cfg.VDDK.SecretKey == "" {
		return fmt.Errorf("vddk transfer requires vsphere credentials")
	}
	return vddkimport.Run(ctx, vddkimport.Params{
		Endpoint:           cfg.VDDK.URL,
		AccessKey:          cfg.VDDK.AccessKey,
		SecretKey:          cfg.VDDK.SecretKey,
		Thumbprint:         cfg.VDDK.Thumbprint,
		UUID:               cfg.VDDK.UUID,
		BackingFile:        cfg.VDDK.BackingFile,
		CurrentCheckpoint:  cfg.CheckpointCurrent,
		PreviousCheckpoint: cfg.CheckpointPrevious,
		FinalCheckpoint:    cfg.FinalCheckpoint,
		ImageSize:          cfg.ImageSize,
		Preallocation:      cfg.Preallocation,
		FilesystemOverhead: cfg.FilesystemOverhead,
		CertDir:            cfg.CertDir,
		InsecureTLS:        cfg.InsecureTLS,
	})
}
