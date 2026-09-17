package main

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/kubev2v/forklift/pkg/controller/conversion"
	"github.com/kubev2v/forklift/pkg/settings"
	toeholdvsphere "github.com/kubev2v/forklift/pkg/toehold/vsphere"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/vim25/mo"
	core "k8s.io/api/core/v1"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "cleanup" {
		cleanup()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "cleanup-appliances" {
		cleanupAppliances()
		return
	}
	listDatastores()
}

func loadCreds() (*core.Secret, string, error) {
	creds := &core.Secret{Data: map[string][]byte{
		"user":     []byte(os.Getenv(settings.VCenterUser)),
		"password": []byte(os.Getenv(settings.VCenterPassword)),
	}}
	if settings.LookupBool(settings.VCenterInsecure, false) {
		creds.Data["insecureSkipVerify"] = []byte("true")
	}
	url := os.Getenv(settings.VCenterURL)
	if url == "" || len(creds.Data["user"]) == 0 || len(creds.Data["password"]) == 0 {
		return nil, "", fmt.Errorf("VCENTER_URL, VCENTER_USER, VCENTER_PASSWORD required")
	}
	return creds, url, nil
}

func listDatastores() {
	ctx := context.Background()
	creds, url, err := loadCreds()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	conn := conversion.VsphereConnectionSecret(url, creds, os.Getenv(settings.VCenterThumbprint))
	gc, err := conversion.GovmomiClientFromSecret(ctx, conn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer gc.Logout(ctx)

	finder := find.NewFinder(gc.Client, true)
	dc, err := finder.DefaultDatacenter(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	finder.SetDatacenter(dc)
	datastores, err := finder.DatastoreList(ctx, "*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	type row struct {
		name, typ   string
		free, total float64
		ok          bool
	}
	var rows []row
	for _, ds := range datastores {
		var props mo.Datastore
		if err := ds.Properties(ctx, ds.Reference(), []string{"summary"}, &props); err != nil {
			continue
		}
		rows = append(rows, row{
			name:  ds.Name(),
			typ:   string(props.Summary.Type),
			free:  float64(props.Summary.FreeSpace) / (1024 * 1024 * 1024),
			total: float64(props.Summary.Capacity) / (1024 * 1024 * 1024),
			ok:    props.Summary.Accessible,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].free > rows[j].free })
	for _, r := range rows {
		acc := "accessible"
		if !r.ok {
			acc = "NOT-accessible"
		}
		fmt.Printf("%s type=%s %s free=%.1fGiB total=%.1fGiB\n", r.name, r.typ, acc, r.free, r.total)
	}
}

func cleanup() {
	ctx := context.Background()
	creds, url, err := loadCreds()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	conn := conversion.VsphereConnectionSecret(url, creds, os.Getenv(settings.VCenterThumbprint))
	gc, err := conversion.GovmomiClientFromSecret(ctx, conn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	client, err := toeholdvsphere.NewClient(gc)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer client.Close(ctx)

	folder := "/Datacenter/vm"
	for _, name := range []string{"nbdkit-toehold", "nbdkit-toehold-02"} {
		if err := client.DestroyVMIfExists(ctx, folder, name); err != nil {
			fmt.Fprintf(os.Stderr, "destroy %s: %v\n", name, err)
			os.Exit(1)
		}
		fmt.Printf("destroyed %s if present\n", name)
	}
	cleanupAppliancesOn(ctx, client)
}

func cleanupAppliances() {
	ctx := context.Background()
	creds, url, err := loadCreds()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	conn := conversion.VsphereConnectionSecret(url, creds, os.Getenv(settings.VCenterThumbprint))
	gc, err := conversion.GovmomiClientFromSecret(ctx, conn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	client, err := toeholdvsphere.NewClient(gc)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer client.Close(ctx)
	cleanupAppliancesOn(ctx, client)
}

func cleanupAppliancesOn(ctx context.Context, client *toeholdvsphere.Client) {
	folder := "/Datacenter/vm"
	for _, name := range []string{
		"e2e-copy-appliance",
		"e2e-copy-appliance-2",
		"e2e-appliance-v3",
		"diskid-e2e-gwen",
	} {
		if err := client.DestroyVMIfExists(ctx, folder, name); err != nil {
			fmt.Fprintf(os.Stderr, "destroy %s: %v\n", name, err)
			os.Exit(1)
		}
		fmt.Printf("destroyed %s if present\n", name)
	}
}
