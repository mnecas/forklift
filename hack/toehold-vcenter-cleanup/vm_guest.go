//go:build ignore

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/kubev2v/forklift/pkg/controller/conversion"
	"github.com/kubev2v/forklift/pkg/settings"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
	core "k8s.io/api/core/v1"
)

func main() {
	name := "e2e-appliance-v4"
	if len(os.Args) > 1 {
		name = os.Args[1]
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

	finder := find.NewFinder(gc.Client, true)
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
		"name", "runtime", "guest", "config.firmware", "config.hardware.device",
	}, &props); err != nil {
		panic(err)
	}
	fmt.Printf("name=%s power=%s firmware=%s tools=%s toolsStatus=%s ip=%s host=%s\n",
		props.Name, props.Runtime.PowerState, props.Config.Firmware,
		props.Guest.ToolsRunningStatus, props.Guest.ToolsStatus,
		props.Guest.IpAddress, props.Guest.HostName)
	for _, n := range props.Guest.Net {
		fmt.Printf("  guest nic network=%q mac=%s ips=%v connected=%v\n",
			n.Network, n.MacAddress, n.IpAddress, n.Connected)
	}
	for _, dev := range props.Config.Hardware.Device {
		switch card := dev.(type) {
		case types.BaseVirtualEthernetCard:
			fmt.Printf("  hw nic mac=%s connected=%v network=%q\n",
				card.GetVirtualEthernetCard().MacAddress,
				card.GetVirtualEthernetCard().Connectable.Connected,
				card.GetVirtualEthernetCard().DeviceInfo.GetDescription().Summary)
		}
	}
}
