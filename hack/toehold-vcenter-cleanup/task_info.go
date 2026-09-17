//go:build ignore

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/kubev2v/forklift/pkg/controller/conversion"
	"github.com/kubev2v/forklift/pkg/settings"
	toeholdvsphere "github.com/kubev2v/forklift/pkg/toehold/vsphere"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
	core "k8s.io/api/core/v1"
)

func deleteVM(name string) {
	if name == "" {
		fmt.Fprintln(os.Stderr, "usage: delete-vm <name>")
		os.Exit(1)
	}
	ctx := context.Background()
	creds := &core.Secret{Data: map[string][]byte{
		"user":               []byte(os.Getenv(settings.VCenterUser)),
		"password":           []byte(os.Getenv(settings.VCenterPassword)),
		"insecureSkipVerify": []byte("true"),
	}}
	url := os.Getenv(settings.VCenterURL)
	conn := conversion.VsphereConnectionSecret(url, creds, "")
	gc, err := conversion.GovmomiClientFromSecret(ctx, conn)
	if err != nil {
		panic(err)
	}
	defer gc.Logout(ctx)
	client, err := toeholdvsphere.NewClient(gc)
	if err != nil {
		panic(err)
	}
	defer client.Close(ctx)
	if err := client.DestroyVMIfExists(ctx, "/Datacenter/vm", name); err != nil {
		panic(err)
	}
	fmt.Printf("destroyed %s if present\n", name)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "delete-vm" {
		deleteVM(os.Args[2])
		return
	}
	taskID := "task-7311"
	if len(os.Args) > 1 {
		taskID = os.Args[1]
	}
	ctx := context.Background()
	creds := &core.Secret{Data: map[string][]byte{
		"user":               []byte(os.Getenv(settings.VCenterUser)),
		"password":           []byte(os.Getenv(settings.VCenterPassword)),
		"insecureSkipVerify": []byte("true"),
	}}
	url := os.Getenv(settings.VCenterURL)
	conn := conversion.VsphereConnectionSecret(url, creds, "")
	gc, err := conversion.GovmomiClientFromSecret(ctx, conn)
	if err != nil {
		panic(err)
	}
	defer gc.Logout(ctx)
	ref := types.ManagedObjectReference{Type: "Task", Value: taskID}
	t := object.NewTask(gc.Client, ref)
	var task mo.Task
	if err := t.Properties(ctx, t.Reference(), []string{"info"}, &task); err != nil {
		panic(err)
	}
	fmt.Printf("task=%s state=%s name=%q\n", taskID, task.Info.State, task.Info.Name)
	if task.Info.Error != nil {
		fmt.Printf("error: %s\n", task.Info.Error.LocalizedMessage)
	}
	finder := find.NewFinder(gc.Client, true)
	dc, _ := finder.DefaultDatacenter(ctx)
	finder.SetDatacenter(dc)
	vm, err := finder.VirtualMachine(ctx, "e2e-copy-appliance")
	if err != nil {
		fmt.Println("vm e2e-copy-appliance:", err)
	} else {
		fmt.Println("vm exists:", vm.InventoryPath)
	}
	tmpl, err := finder.VirtualMachine(ctx, "/Datacenter/vm/nbdkit-toehold")
	if err != nil {
		fmt.Println("template:", err)
	} else {
		isT, _ := tmpl.IsTemplate(ctx)
		fmt.Println("template ok:", tmpl.InventoryPath, "isTemplate=", isT)
	}
}
