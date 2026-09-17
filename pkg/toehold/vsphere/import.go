package vsphere

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	toeholdovf "github.com/kubev2v/forklift/pkg/toehold/ovf"
	"github.com/vmware/govmomi/nfc"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/methods"
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
	// Template hashes and BaseContainerImage are stamped on the imported VM
	// before it is marked as a template (vCenter rejects some reconfigure ops on templates).
	TemplateDiskHash   string
	TemplateConfigHash string
	BaseContainerImage string
}

type importStreamOptions struct {
	ImportOptions
	DiskReader   io.Reader
	StreamSize   int64
	DiskCapacity int64
	VMDKFileName string
}

// ImportOVF builds an OVF descriptor and imports the VMDK as a template.
func (c *Client) ImportOVF(ctx context.Context, opts ImportOptions) (*VMRef, error) {
	if opts.VMDKPath == "" {
		return nil, fmt.Errorf("vmdk path is required")
	}
	stat, err := os.Stat(opts.VMDKPath)
	if err != nil {
		return nil, err
	}
	return c.importOVFStream(ctx, importStreamOptions{
		ImportOptions: opts,
		StreamSize:    stat.Size(),
		VMDKFileName:  filepath.Base(opts.VMDKPath),
	})
}

func (c *Client) importOVFStream(ctx context.Context, opts importStreamOptions) (*VMRef, error) {
	if opts.DiskReader == nil {
		f, err := os.Open(opts.VMDKPath)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		opts.DiskReader = f
		if opts.StreamSize == 0 {
			stat, err := f.Stat()
			if err != nil {
				return nil, err
			}
			opts.StreamSize = stat.Size()
		}
	}
	if opts.VMDKFileName == "" {
		if opts.VMDKPath != "" {
			opts.VMDKFileName = filepath.Base(opts.VMDKPath)
		} else {
			opts.VMDKFileName = "disk-0.vmdk"
		}
	}
	if opts.DiskCapacity == 0 && opts.VMDKPath != "" {
		opts.DiskCapacity, _ = toeholdovf.DiskCapacity(opts.VMDKPath)
	}
	log.Info("Importing template",
		"name", opts.Name,
		"vmdk", opts.VMDKPath,
		"streamSize", opts.StreamSize,
		"capacity", opts.DiskCapacity,
		"folder", opts.FolderPath,
		"datastore", opts.Datastore,
		"network", opts.Network,
		"cpus", opts.CPUs,
		"memoryMiB", opts.MemoryMiB,
	)
	descriptor, err := toeholdovf.Descriptor(toeholdovf.DescriptorOptions{
		Name:         opts.Name,
		Network:      opts.Network,
		CPUs:         opts.CPUs,
		MemoryMiB:    opts.MemoryMiB,
		StreamSize:   opts.StreamSize,
		DiskCapacity: opts.DiskCapacity,
		VMDKFileName: opts.VMDKFileName,
		DiskHash:     opts.TemplateDiskHash,
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
	log.V(1).Info("Creating import spec")
	res, err := methods.CreateImportSpec(ctx, c.Govmomi.Client, &req)
	if err != nil {
		return nil, err
	}
	if len(res.Returnval.Error) > 0 {
		return nil, fmt.Errorf("create import spec: %s", res.Returnval.Error[0].LocalizedMessage)
	}
	log.V(1).Info("Import spec ready", "fileItems", len(res.Returnval.FileItem))

	host, err := c.findImportHost(ctx, datastore, net)
	if err != nil {
		return nil, err
	}
	log.V(1).Info("Starting NFC lease",
		"host", hostDisplayName(ctx, host),
		"moref", host.Reference().Value,
	)
	lease, err := pool.ImportVApp(ctx, res.Returnval.ImportSpec, folder, host)
	if err != nil {
		return nil, err
	}
	info, err := lease.Wait(ctx, res.Returnval.FileItem)
	if err != nil {
		return nil, err
	}
	log.V(1).Info("NFC lease ready", "uploadItems", len(info.Items))
	if len(info.Items) == 0 {
		return nil, fmt.Errorf("nfc lease returned no upload items")
	}

	updater := lease.StartUpdater(ctx, info)
	defer updater.Done()

	for _, item := range info.Items {
		if !strings.HasSuffix(strings.ToLower(item.Path), ".vmdk") {
			continue
		}
		method := "POST"
		if item.Create {
			method = "PUT"
		}
		log.V(1).Info("Uploading VMDK",
			"path", item.Path,
			"bytes", opts.StreamSize,
			"method", method,
			"create", item.Create,
			"url", item.URL,
		)
		if err = uploadLeaseFile(ctx, c, item, opts.DiskReader, opts.StreamSize); err != nil {
			_ = lease.Abort(ctx, nil)
			return nil, err
		}
		log.V(1).Info("Uploaded VMDK", "path", item.Path)
		break
	}
	log.V(1).Info("Completing NFC lease")
	if err = lease.Complete(ctx); err != nil {
		return nil, err
	}

	log.V(1).Info("Locating imported object", "name", opts.Name, "folder", opts.FolderPath)
	vmRef, err := c.FindVM(ctx, opts.FolderPath, opts.Name)
	if err != nil {
		log.V(1).Info("Imported object not found as VM, retrying without template filter", "err", err)
		vmRef, err = c.findVM(ctx, opts.FolderPath, opts.Name, false)
		if err != nil {
			return nil, err
		}
	}
	log.V(1).Info("Located imported object", "name", vmRef.Name, "moref", vmRef.Moref)
	// rhel-guest-image is built for BIOS boot; forcing EFI here leaves the guest
	// with no EFI boot loader and clones land in the Boot Manager.
	if err = c.SetDiskEnableUUID(ctx, vmRef.VM); err != nil {
		return nil, fmt.Errorf("enable disk.EnableUUID on %s: %w", vmRef.Moref, err)
	}
	if opts.TemplateDiskHash != "" || opts.TemplateConfigHash != "" {
		log.V(1).Info("Stamping template metadata before mark-as-template", "moref", vmRef.Moref)
		stamp := map[string]string{
			ImportedAtAnnotation: time.Now().UTC().Format(time.RFC3339),
		}
		if opts.TemplateDiskHash != "" {
			stamp[DiskHashAnnotation] = opts.TemplateDiskHash
		}
		if opts.TemplateConfigHash != "" {
			stamp[ConfigHashAnnotation] = opts.TemplateConfigHash
		}
		if opts.BaseContainerImage != "" {
			stamp[BaseContainerImageAnnotation] = opts.BaseContainerImage
		}
		if err = c.SetAnnotationMap(ctx, vmRef.VM, stamp); err != nil {
			return nil, fmt.Errorf("stamp template metadata on %s: %w", vmRef.Moref, err)
		}
	}
	log.Info("Marking as template", "name", opts.Name, "moref", vmRef.Moref)
	if err = vmRef.VM.MarkAsTemplate(ctx); err != nil {
		return nil, fmt.Errorf("mark %s as template: %w", vmRef.Moref, err)
	}
	ref, err := c.FindTemplate(ctx, opts.FolderPath, opts.Name)
	if err != nil {
		return nil, err
	}
	log.Info("Template ready", "name", opts.Name, "moref", ref.Moref)
	return ref, nil
}

func uploadLeaseFile(ctx context.Context, c *Client, item nfc.FileItem, f io.Reader, size int64) error {
	opts := soap.Upload{
		Progress: item,
	}
	if size > 0 {
		opts.ContentLength = size
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
