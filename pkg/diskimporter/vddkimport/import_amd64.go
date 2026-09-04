//go:build amd64

package vddkimport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	"kubevirt.io/containerized-data-importer/pkg/common"
	"kubevirt.io/containerized-data-importer/pkg/importer"
)

// Run performs a VDDK import using CDI's importer packages (same logic as cdi-importer).
func Run(ctx context.Context, p Params) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}

	volumeMode := corev1.PersistentVolumeBlock
	if _, statErr := os.Stat(common.WriteBlockPath); os.IsNotExist(statErr) {
		volumeMode = corev1.PersistentVolumeFilesystem
	}

	defer fsyncDataFile(volumeMode)

	dest := common.ImporterWritePath
	if volumeMode == corev1.PersistentVolumeBlock {
		dest = common.WriteBlockPath
	}

	ds, err := importer.NewVDDKDataSource(
		p.Endpoint,
		p.AccessKey,
		p.SecretKey,
		p.Thumbprint,
		p.UUID,
		p.BackingFile,
		p.CurrentCheckpoint,
		p.PreviousCheckpoint,
		fmt.Sprintf("%t", p.FinalCheckpoint),
		volumeMode,
	)
	if err != nil {
		return fmt.Errorf("unable to connect to vddk data source: %w", err)
	}
	defer ds.Close()

	processor := importer.NewDataProcessor(
		ds,
		dest,
		common.ImporterDataDir,
		common.ScratchDataDir,
		p.ImageSize,
		p.FilesystemOverhead,
		p.Preallocation,
		os.Getenv(common.CacheMode),
	)

	if err := processor.ProcessData(); err != nil {
		if errors.Is(err, importer.ErrRequiresScratchSpace) {
			return fmt.Errorf("import requires scratch space (unexpected for vddk): %w", err)
		}
		return fmt.Errorf("unable to process data: %w", err)
	}

	return nil
}

func fsyncDataFile(volumeMode corev1.PersistentVolumeMode) {
	if volumeMode != corev1.PersistentVolumeFilesystem {
		return
	}
	f, err := os.Open(common.ImporterWritePath)
	if err != nil {
		klog.Warningf("Failed to open %s for fsync: %v", common.ImporterWritePath, err)
		return
	}
	defer f.Close()
	if err := syscall.Fsync(int(f.Fd())); err != nil {
		klog.Warningf("Failed to fsync %s: %v", common.ImporterWritePath, err)
	}
}
