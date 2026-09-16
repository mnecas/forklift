package ovf

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	govmovi "github.com/vmware/govmomi/ovf"
	"github.com/vmware/govmomi/vim25/xml"
)

// DescriptorOptions configures OVF generation.
type DescriptorOptions struct {
	VMDKPath string
	// VMDKFileName is the href for the disk file in the OVF (for streaming import).
	VMDKFileName string
	Name         string
	Network      string
	CPUs         int32
	MemoryMiB    int32
	// StreamSize is the uploaded VMDK byte size for the OVF File element.
	// When zero, size is read from VMDKPath on disk.
	StreamSize int64
	// DiskCapacity is the virtual disk capacity in bytes.
	// When zero, capacity is inferred from VMDKPath.
	DiskCapacity int64
	// DiskHash fingerprints the base disk content (e.g. toehold version.DiskHash).
	// When set, used as the OVF diskId suffix; otherwise "disk-0".
	DiskHash string
}

// Descriptor generates a minimal OVF envelope for a stream-optimized VMDK.
func Descriptor(opts DescriptorOptions) (string, error) {
	if opts.Name == "" {
		return "", fmt.Errorf("name is required")
	}
	if opts.VMDKPath == "" && opts.DiskCapacity == 0 && opts.StreamSize == 0 {
		return "", fmt.Errorf("vmdk path or stream size/capacity is required")
	}
	if opts.Network == "" {
		opts.Network = "VM Network"
	}
	if opts.CPUs == 0 {
		opts.CPUs = 2
	}
	if opts.MemoryMiB == 0 {
		opts.MemoryMiB = 4096
	}

	var size int64
	var err error
	if opts.VMDKPath != "" {
		size, err = fileSize(opts.VMDKPath)
		if err != nil && opts.StreamSize == 0 {
			return "", err
		}
	}
	if opts.StreamSize > 0 {
		size = opts.StreamSize
	}
	capacity := opts.DiskCapacity
	if capacity == 0 && opts.VMDKPath != "" {
		capacity, err = DiskCapacity(opts.VMDKPath)
		if err != nil {
			capacity = size
		}
	}
	if capacity == 0 {
		capacity = size
	}
	if size == 0 {
		return "", fmt.Errorf("stream file size is required (set StreamSize or VMDKPath)")
	}

	vmdkName := opts.VMDKFileName
	if vmdkName == "" && opts.VMDKPath != "" {
		vmdkName = filepath.Base(opts.VMDKPath)
	}
	if vmdkName == "" {
		vmdkName = "disk-0.vmdk"
	}

	envelope := streamOptimizedEnvelope(opts, vmdkName, diskID(opts), size, capacity)
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	if err := envelope.Write(&buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func streamOptimizedEnvelope(opts DescriptorOptions, vmdkName, diskID string, size, capacity int64) govmovi.Envelope {
	name := opts.Name
	network := opts.Network
	fileRef := "file1"
	capUnits := "byte"
	format := "http://www.vmware.com/interfaces/specifications/vmdk.html#streamOptimized"
	capacityStr := fmt.Sprintf("%d", capacity)

	return govmovi.Envelope{
		References: []govmovi.File{{
			ID:   fileRef,
			Href: vmdkName,
			Size: uint(size),
		}},
		Disk: &govmovi.DiskSection{
			Section: govmovi.Section{Info: "Virtual disk information"},
			Disks: []govmovi.VirtualDiskDesc{{
				DiskID:                  diskID,
				FileRef:                 &fileRef,
				Capacity:                capacityStr,
				CapacityAllocationUnits: &capUnits,
				Format:                  &format,
			}},
		},
		Network: &govmovi.NetworkSection{
			Section: govmovi.Section{Info: "Logical networks"},
			Networks: []govmovi.Network{{
				Name:        network,
				Description: network,
			}},
		},
		VirtualSystem: &govmovi.VirtualSystem{
			Content: govmovi.Content{
				ID:   name,
				Info: name,
				Name: &name,
			},
			OperatingSystem: &govmovi.OperatingSystemSection{
				Section:     govmovi.Section{Info: "Guest OS"},
				ID:          109,
				Version:     strPtr("9"),
				OSType:      strPtr("rhel9_64Guest"),
				Description: strPtr("Red Hat Enterprise Linux 9 (64-bit)"),
			},
			VirtualHardware: []govmovi.VirtualHardwareSection{{
				Section: govmovi.Section{Info: "Virtual hardware"},
				System: &govmovi.VirtualSystemSettingData{
					CIMVirtualSystemSettingData: govmovi.CIMVirtualSystemSettingData{
						ElementName:             "Virtual Hardware Family",
						InstanceID:              "0",
						VirtualSystemIdentifier: &name,
						VirtualSystemType:       strPtr("vmx-17"),
					},
				},
				Item: []govmovi.ResourceAllocationSettingData{
					cpuItem(opts.CPUs),
					memoryItem(opts.MemoryMiB),
					scsiControllerItem(),
					diskItem(diskID),
					networkItem(network),
				},
			}},
		},
	}
}

func cpuItem(cpus int32) govmovi.ResourceAllocationSettingData {
	return govmovi.ResourceAllocationSettingData{
		CIMResourceAllocationSettingData: govmovi.CIMResourceAllocationSettingData{
			AllocationUnits:   strPtr("hertz * 10^6"),
			Description:     strPtr("Number of Virtual CPUs"),
			ElementName:     fmt.Sprintf("%d virtual CPU(s)", cpus),
			InstanceID:      "1",
			ResourceType:    resourceType(govmovi.Processor),
			VirtualQuantity: uintPtr(uint(cpus)),
		},
	}
}

func memoryItem(memoryMiB int32) govmovi.ResourceAllocationSettingData {
	return govmovi.ResourceAllocationSettingData{
		CIMResourceAllocationSettingData: govmovi.CIMResourceAllocationSettingData{
			AllocationUnits:   strPtr("byte * 2^20"),
			Description:     strPtr("Memory Size"),
			ElementName:     fmt.Sprintf("%d MB of memory", memoryMiB),
			InstanceID:      "2",
			ResourceType:    resourceType(govmovi.Memory),
			VirtualQuantity: uintPtr(uint(memoryMiB)),
		},
	}
}

func scsiControllerItem() govmovi.ResourceAllocationSettingData {
	return govmovi.ResourceAllocationSettingData{
		CIMResourceAllocationSettingData: govmovi.CIMResourceAllocationSettingData{
			Address:         strPtr("0"),
			Description:     strPtr("SCSI Controller"),
			ElementName:     "SCSI Controller 0",
			InstanceID:      "3",
			ResourceSubType: strPtr("lsilogic"),
			ResourceType:    resourceType(govmovi.ParallelScsiHba),
		},
	}
}

func diskItem(diskID string) govmovi.ResourceAllocationSettingData {
	return govmovi.ResourceAllocationSettingData{
		CIMResourceAllocationSettingData: govmovi.CIMResourceAllocationSettingData{
			AddressOnParent: strPtr("0"),
			ElementName:     "Hard disk 1",
			HostResource:    []string{"ovf:/disk/" + diskID},
			InstanceID:      "4",
			Parent:          strPtr("3"),
			ResourceType:    resourceType(govmovi.DiskDrive),
		},
	}
}

func networkItem(network string) govmovi.ResourceAllocationSettingData {
	return govmovi.ResourceAllocationSettingData{
		CIMResourceAllocationSettingData: govmovi.CIMResourceAllocationSettingData{
			AddressOnParent:     strPtr("0"),
			AutomaticAllocation: boolPtr(true),
			Connection:          []string{network},
			ElementName:         "Network adapter 1",
			InstanceID:          "5",
			ResourceSubType:     strPtr("vmxnet3"),
			ResourceType:        resourceType(govmovi.EthernetAdapter),
		},
	}
}

func strPtr(s string) *string { return &s }

func boolPtr(v bool) *bool { return &v }

func uintPtr(v uint) *uint { return &v }

func resourceType(t govmovi.CIMResourceType) *govmovi.CIMResourceType { return &t }

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func diskID(opts DescriptorOptions) string {
	if opts.DiskHash == "" {
		return "disk-0"
	}
	h := opts.DiskHash
	if len(h) > 8 {
		h = h[:8]
	}
	return "vdisk-" + h
}
