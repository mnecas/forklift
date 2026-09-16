package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/kubev2v/forklift/pkg/controller/conversion"
	"github.com/kubev2v/forklift/pkg/lib/logging"
	"github.com/kubev2v/forklift/pkg/settings"
	toeholdvsphere "github.com/kubev2v/forklift/pkg/toehold/vsphere"
	core "k8s.io/api/core/v1"
)

var log = logging.WithName("toehold-uploader")

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "toehold-uploader: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	creds := &core.Secret{Data: map[string][]byte{
		"user":     []byte(os.Getenv(settings.VCenterUser)),
		"password": []byte(os.Getenv(settings.VCenterPassword)),
	}}
	if settings.LookupBool(settings.VCenterInsecure, false) {
		creds.Data["insecureSkipVerify"] = []byte("true")
	}
	conn := conversion.VsphereConnectionSecret(
		os.Getenv(settings.VCenterURL),
		creds,
		os.Getenv(settings.VCenterThumbprint),
	)

	log.Info("Connecting to vCenter", "url", os.Getenv(settings.VCenterURL))
	gc, err := conversion.GovmomiClientFromSecret(ctx, conn)
	if err != nil {
		return err
	}
	client, err := toeholdvsphere.NewClient(gc)
	if err != nil {
		return err
	}
	defer client.Close(ctx)

	importOpts := toeholdvsphere.ImportOptions{
		FolderPath:         settings.Lookup(settings.ToeholdFolder, ""),
		Datastore:          settings.Lookup(settings.ToeholdDatastore, ""),
		Network:            settings.Lookup(settings.ToeholdNetwork, settings.DefaultToeholdNetwork),
		Name:               os.Getenv(settings.ToeholdTemplateName),
		VMDKPath:           settings.Lookup(settings.ToeholdVMDKPath, settings.DefaultBuildPodVMDKPath),
		CPUs:               int32(settings.LookupInt(settings.ToeholdBuildPodCPUs, 2)),
		MemoryMiB:          int32(settings.LookupInt(settings.ToeholdBuildPodMemoryMiB, 4096)),
		TemplateDiskHash:   os.Getenv(settings.ToeholdTemplateContentHash),
		TemplateConfigHash: os.Getenv(settings.ToeholdTemplateConfigHash),
		BaseContainerImage: os.Getenv(settings.ToeholdBaseContainerImage),
	}
	log.Info("Removing existing template if present", "name", importOpts.Name)
	_ = client.DestroyVMIfExists(ctx, importOpts.FolderPath, importOpts.Name)

	st, err := os.Stat(importOpts.VMDKPath)
	if err != nil {
		return fmt.Errorf("vmdk %s: %w", importOpts.VMDKPath, err)
	}
	log.Info("Using VMDK",
		"path", importOpts.VMDKPath,
		"bytes", st.Size(),
		"template", importOpts.Name,
		"folder", importOpts.FolderPath,
		"datastore", importOpts.Datastore,
		"network", importOpts.Network,
	)

	ref, err := client.ImportOVF(ctx, importOpts)
	if err != nil {
		return err
	}
	log.Info("Upload complete", "moref", ref.Moref)
	return nil
}
