package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kubev2v/forklift/pkg/toehold/annotations"
	toeholdvsphere "github.com/kubev2v/forklift/pkg/toehold/vsphere"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "toehold-uploader: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	vmdk, err := findVMDK("/work/output")
	if err != nil {
		return err
	}
	log.Printf("toehold-uploader: using vmdk %s", vmdk)
	opts := toeholdvsphere.ConnectOptions{
		URL:        os.Getenv("VCENTER_URL"),
		Username:   os.Getenv("VCENTER_USER"),
		Password:   os.Getenv("VCENTER_PASSWORD"),
		Thumbprint: os.Getenv("VCENTER_THUMBPRINT"),
		Insecure:   os.Getenv("VCENTER_INSECURE") == "true" || os.Getenv("VCENTER_INSECURE") == "1",
	}
	log.Printf("toehold-uploader: connecting to %s", opts.URL)
	client, err := toeholdvsphere.Connect(ctx, opts)
	if err != nil {
		return err
	}
	defer client.Close(ctx)

	name := env("TOEHOLD_TEMPLATE_NAME", "nbdkit-toehold")
	log.Printf("toehold-uploader: removing existing template %q if present", name)
	_ = client.DestroyVMIfExists(ctx, env("TOEHOLD_FOLDER", ""), name)

	cpus := int32(envInt("TOEHOLD_CPUS", 2))
	mem := int32(envInt("TOEHOLD_MEMORY_MIB", 4096))
	importOpts := toeholdvsphere.ImportOptions{
		FolderPath: env("TOEHOLD_FOLDER", ""),
		Datastore:  env("TOEHOLD_DATASTORE", ""),
		Network:    env("TOEHOLD_NETWORK", "VM Network"),
		Name:       name,
		VMDKPath:   vmdk,
		CPUs:       cpus,
		MemoryMiB:  mem,
	}
	var ref *toeholdvsphere.VMRef
	if env("TOEHOLD_IMPORT_METHOD", "govc") == "govc" {
		ref, err = client.ImportOVFViaGovc(ctx, importOpts, opts)
	} else {
		ref, err = client.ImportOVF(ctx, importOpts)
	}
	if err != nil {
		return err
	}
	log.Printf("toehold-uploader: import complete moref=%s", ref.Moref)
	return client.SetAnnotationMap(ctx, ref.VM, map[string]string{
		annotations.ContentHash:  os.Getenv("TOEHOLD_CONTENT_HASH"),
		annotations.BootcImage:   os.Getenv("TOEHOLD_BOOTC_IMAGE"),
		annotations.BootcImageID: os.Getenv("TOEHOLD_BOOTC_IMAGE_ID"),
		annotations.ImportedAt:   time.Now().UTC().Format(time.RFC3339),
	})
}

func findVMDK(dir string) (string, error) {
	var matches []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(d.Name()), ".vmdk") {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no vmdk under %s", dir)
	}
	return matches[0], nil
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
