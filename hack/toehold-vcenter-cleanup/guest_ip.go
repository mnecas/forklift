//go:build ignore

package main

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/vim25/mo"
)

func main() {
	name := "e2e-appliance-v4"
	if len(os.Args) > 1 {
		name = os.Args[1]
	}
	ctx := context.Background()
	u, err := url.Parse(os.Getenv("VCENTER_URL"))
	if err != nil {
		panic(err)
	}
	u.User = url.UserPassword(os.Getenv("VCENTER_USER"), os.Getenv("VCENTER_PASSWORD"))
	client, err := govmomi.NewClient(ctx, u, true)
	if err != nil {
		panic(err)
	}
	defer client.Logout(ctx)

	finder := find.NewFinder(client.Client, true)
	dc, err := finder.DefaultDatacenter(ctx)
	if err != nil {
		panic(err)
	}
	finder.SetDatacenter(dc)
	vm, err := finder.VirtualMachine(ctx, name)
	if err != nil {
		panic(err)
	}
	var props mo.VirtualMachine
	if err := vm.Properties(ctx, vm.Reference(), []string{
		"name", "guest",
	}, &props); err != nil {
		panic(err)
	}
	fmt.Printf("vm=%s guest.ipAddress=%q host=%q tools=%s\n",
		props.Name, props.Guest.IpAddress, props.Guest.HostName, props.Guest.ToolsRunningStatus)
	for i, nic := range props.Guest.Net {
		fmt.Printf("  nic[%d] network=%q mac=%s ipAddress=%v ipConfig=%v\n",
			i, nic.Network, nic.MacAddress, nic.IpAddress, nic.IpConfig != nil)
	}
}
