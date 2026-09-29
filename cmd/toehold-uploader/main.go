package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/kubev2v/forklift/pkg/controller/base"
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
	creds := &core.Secret{Data: map[string][]byte{}}
	if settings.LookupBool(settings.VCenterInsecure, false) {
		creds.Data["insecureSkipVerify"] = []byte("true")
	}

	url := os.Getenv(settings.VCenterURL)
	log.Info("Connecting to vCenter", "url", url)
	gc, err := base.ConnectGovmomi(
		ctx,
		url,
		os.Getenv(settings.VCenterUser),
		os.Getenv(settings.VCenterPassword),
		os.Getenv(settings.VCenterThumbprint),
		creds,
	)
	if err != nil {
		return err
	}
	client, err := toeholdvsphere.NewClient(gc)
	if err != nil {
		return err
	}
	defer client.Close(ctx)

	opts := toeholdvsphere.ImportOptions{
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
	_ = client.DestroyVMIfExists(ctx, opts.FolderPath, opts.Name)
	ref, err := client.ImportOVF(ctx, opts)
	if err != nil {
		return err
	}
	log.Info("Upload complete", "moref", ref.Moref)
	return nil
}
