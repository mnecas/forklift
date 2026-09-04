package vsphere

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	toeholdovf "github.com/kubev2v/forklift/pkg/toehold/ovf"
	"github.com/vmware/govmomi/nfc"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/methods"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// ImportOptions configures OVF import into vCenter.
type ImportOptions struct {
	FolderPath string
	Datastore  string
	Network    string
	Name       string
	VMDKPath   string
	CPUs       int32
	MemoryMiB  int32
}

func importLog(format string, args ...any) {
	log.Printf("toehold-import: "+format, args...)
}

// ImportOVF builds an OVF descriptor and imports the VMDK as a template.
func (c *Client) ImportOVF(ctx context.Context, opts ImportOptions) (*VMRef, error) {
	if opts.VMDKPath == "" {
		return nil, fmt.Errorf("vmdk path is required")
	}
	importLog("importing %q from %s datastore=%s network=%s", opts.Name, opts.VMDKPath, opts.Datastore, opts.Network)
	descriptor, err := toeholdovf.Descriptor(toeholdovf.DescriptorOptions{
		VMDKPath:  opts.VMDKPath,
		Name:      opts.Name,
		Network:   opts.Network,
		CPUs:      opts.CPUs,
		MemoryMiB: opts.MemoryMiB,
	})
	if err != nil {
		return nil, err
	}

	folder, err := c.findFolder(ctx, opts.FolderPath)
	if err != nil {
		return nil, err
	}
	datastore, err := c.findDatastore(ctx, opts.Datastore)
	if err != nil {
		return nil, err
	}
	pool, err := c.findResourcePool(ctx, opts.FolderPath)
	if err != nil {
		return nil, err
	}
	var net object.NetworkReference
	if opts.Network != "" {
		net, err = c.findNetwork(ctx, opts.Network)
		if err != nil {
			return nil, err
		}
	}

	params := &types.OvfCreateImportSpecParams{
		EntityName:       opts.Name,
		DiskProvisioning: string(types.OvfCreateImportSpecParamsDiskProvisioningTypeThin),
	}
	if opts.Network != "" {
		params.NetworkMapping = []types.OvfNetworkMapping{{
			Name:    opts.Network,
			Network: net.Reference(),
		}}
	}

	req := types.CreateImportSpec{
		This:          *c.Govmomi.ServiceContent.OvfManager,
		OvfDescriptor: descriptor,
		ResourcePool:  pool.Reference(),
		Datastore:     datastore.Reference(),
		Cisp:          params,
	}
	importLog("creating import spec")
	res, err := methods.CreateImportSpec(ctx, c.Govmomi.Client, &req)
	if err != nil {
		return nil, err
	}
	if len(res.Returnval.Error) > 0 {
		return nil, fmt.Errorf("create import spec: %s", res.Returnval.Error[0].LocalizedMessage)
	}
	importLog("import spec ready (%d file items)", len(res.Returnval.FileItem))

	host, err := c.findImportHost(ctx, datastore, net)
	if err != nil {
		return nil, err
	}
	importLog("starting NFC lease on host %s", host.Name())
	lease, err := pool.ImportVApp(ctx, res.Returnval.ImportSpec, folder, host)
	if err != nil {
		return nil, err
	}
	info, err := lease.Wait(ctx, res.Returnval.FileItem)
	if err != nil {
		return nil, err
	}
	importLog("lease ready (%d upload items)", len(info.Items))
	if len(info.Items) == 0 {
		return nil, fmt.Errorf("nfc lease returned no upload items")
	}

	updater := lease.StartUpdater(ctx, info)
	defer updater.Done()

	ovfDir := filepath.Dir(opts.VMDKPath)
	for _, item := range info.Items {
		path := filepath.Join(ovfDir, item.Path)
		stat, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		importLog("uploading %s (%d bytes) to %s", path, stat.Size(), item.URL.Host)
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		if err = uploadLeaseFile(ctx, c, lease, item, f, stat.Size()); err != nil {
			_ = lease.Abort(ctx, nil)
			_ = f.Close()
			return nil, err
		}
		_ = f.Close()
		importLog("uploaded %s", item.Path)
	}
	importLog("completing NFC lease")
	if err = lease.Complete(ctx); err != nil {
		return nil, err
	}

	importLog("marking %q as template", opts.Name)
	vmRef, err := c.FindVM(ctx, opts.FolderPath, opts.Name)
	if err != nil {
		// Imported object may still be marked as template later.
		vmRef, err = c.findVM(ctx, opts.FolderPath, opts.Name, false)
		if err != nil {
			return nil, err
		}
	}
	if err = c.SetEFIBoot(ctx, vmRef.VM); err != nil {
		return nil, err
	}
	if err = vmRef.VM.MarkAsTemplate(ctx); err != nil {
		return nil, err
	}
	importLog("template %q ready", opts.Name)
	return c.FindTemplate(ctx, opts.FolderPath, opts.Name)
}

func (c *Client) findImportHost(ctx context.Context, datastore *object.Datastore, net object.NetworkReference) (*object.HostSystem, error) {
	hosts, err := datastore.AttachedHosts(ctx)
	if err != nil {
		return nil, err
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("no hosts attached to datastore %q", datastore.Name())
	}
	var netRef types.ManagedObjectReference
	if net != nil {
		netRef = net.Reference()
	}
	for _, host := range hosts {
		var o mo.HostSystem
		if err := host.Properties(ctx, host.Reference(), []string{"network", "runtime"}, &o); err != nil {
			continue
		}
		if o.Runtime.InMaintenanceMode ||
			o.Runtime.PowerState == types.HostSystemPowerStatePoweredOff ||
			o.Runtime.PowerState == types.HostSystemPowerStateStandBy {
			continue
		}
		if net != nil {
			if _, ok := net.(*object.Network); ok {
				found := false
				for _, n := range o.Network {
					if n.Value == netRef.Value {
						found = true
						break
					}
				}
				if !found {
					continue
				}
			}
		}
		return host, nil
	}
	importLog("no host matched network/runtime filters; using %s", hosts[0].Name())
	return hosts[0], nil
}

func uploadLeaseFile(ctx context.Context, c *Client, lease *nfc.Lease, item nfc.FileItem, f io.Reader, size int64) error {
	opts := soap.Upload{
		Progress:      item,
		ContentLength: size,
	}
	if item.Create {
		opts.Method = "PUT"
		opts.Headers = map[string]string{"Overwrite": "t"}
	} else {
		opts.Method = "POST"
		opts.Type = "application/x-vnd.vmware-streamVmdk"
	}
	err := c.Govmomi.Client.Upload(ctx, f, item.URL, &opts)
	if err == nil {
		return nil
	}
	return fmt.Errorf("nfc upload to %s: %w", item.URL, err)
}

// CloneOptions configures cloning a template into a VM.
type CloneOptions struct {
	FolderPath   string
	Datastore    string
	TemplateName string
	VMName       string
	GuestUserdata string
}

// Clone creates a VM from a template with guestinfo cloud-init userdata.
func (c *Client) Clone(ctx context.Context, opts CloneOptions) (*VMRef, error) {
	template, err := c.FindTemplate(ctx, opts.FolderPath, opts.TemplateName)
	if err != nil {
		return nil, err
	}
	folder, err := c.findFolder(ctx, opts.FolderPath)
	if err != nil {
		return nil, err
	}
	datastore, err := c.findDatastore(ctx, opts.Datastore)
	if err != nil {
		return nil, err
	}
	pool, err := c.findResourcePool(ctx, opts.FolderPath)
	if err != nil {
		return nil, err
	}

	folderRef := folder.Reference()
	poolRef := pool.Reference()
	dsRef := datastore.Reference()
	extra := []types.BaseOptionValue{
		&types.OptionValue{Key: "guestinfo.userdata", Value: opts.GuestUserdata},
		&types.OptionValue{Key: "guestinfo.userdata.encoding", Value: "base64"},
	}
	spec := types.VirtualMachineCloneSpec{
		Config: &types.VirtualMachineConfigSpec{
			Name:        opts.VMName,
			ExtraConfig: extra,
		},
		Location: types.VirtualMachineRelocateSpec{
			Folder:    &folderRef,
			Datastore: &dsRef,
			Pool:      &poolRef,
		},
		PowerOn:  false,
		Template: false,
	}
	task, err := template.VM.Clone(ctx, folder, opts.VMName, spec)
	if err != nil {
		return nil, err
	}
	info, err := task.WaitForResult(ctx, nil)
	if err != nil {
		return nil, err
	}
	ref, ok := info.Result.(types.ManagedObjectReference)
	if !ok {
		return nil, fmt.Errorf("clone result was not a managed object reference")
	}
	vm := object.NewVirtualMachine(c.Govmomi.Client, ref)
	return &VMRef{Name: opts.VMName, Moref: ref.Value, VM: vm}, nil
}
