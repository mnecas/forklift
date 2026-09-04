package ovf

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
)

// DescriptorOptions configures OVF generation.
type DescriptorOptions struct {
	VMDKPath  string
	Name      string
	Network   string
	CPUs      int32
	MemoryMiB int32
}

// Descriptor generates a minimal OVF envelope for a stream-optimized VMDK.
func Descriptor(opts DescriptorOptions) (string, error) {
	if opts.VMDKPath == "" {
		return "", fmt.Errorf("vmdk path is required")
	}
	if opts.Name == "" {
		return "", fmt.Errorf("name is required")
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

	size, err := fileSize(opts.VMDKPath)
	if err != nil {
		return "", err
	}
	capacity, err := DiskCapacity(opts.VMDKPath)
	if err != nil {
		capacity = size
	}

	vmdkName := filepath.Base(opts.VMDKPath)
	name := html.EscapeString(opts.Name)
	network := html.EscapeString(opts.Network)
	diskID := fmt.Sprintf("vdisk-%s", hashName(opts.VMDKPath))

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<Envelope ovf:version="1.0" xml:lang="en-US"
  xmlns="http://schemas.dmtf.org/ovf/envelope/1"
  xmlns:ovf="http://schemas.dmtf.org/ovf/envelope/1"
  xmlns:rasd="http://schemas.dmtf.org/wbem/wscim/1/cim-schema/2/CIM_ResourceAllocationSettingData"
  xmlns:vssd="http://schemas.dmtf.org/wbem/wscim/1/cim-schema/2/CIM_VirtualSystemSettingData"
  xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <References>
    <File ovf:id="file1" ovf:href="%s" ovf:size="%d"/>
  </References>
  <DiskSection>
    <Info>Virtual disk information</Info>
    <Disk ovf:capacity="%d" ovf:capacityAllocationUnits="byte"
          ovf:diskId="%s" ovf:fileRef="file1" ovf:format="http://www.vmware.com/interfaces/specifications/vmdk.html#streamOptimized"/>
  </DiskSection>
  <NetworkSection>
    <Info>Logical networks</Info>
    <Network ovf:name="%s">
      <Description>%s</Description>
    </Network>
  </NetworkSection>
  <VirtualSystem ovf:id="%s">
    <Info>%s</Info>
    <Name>%s</Name>
    <OperatingSystemSection ovf:id="107" ovf:version="8">
      <Info>Guest OS</Info>
      <Description>Red Hat Enterprise Linux 8 (64-bit)</Description>
    </OperatingSystemSection>
    <VirtualHardwareSection>
      <Info>Virtual hardware</Info>
      <System>
        <vssd:ElementName>Virtual Hardware Family</vssd:ElementName>
        <vssd:InstanceID>0</vssd:InstanceID>
        <vssd:VirtualSystemIdentifier>%s</vssd:VirtualSystemIdentifier>
        <vssd:VirtualSystemType>vmx-21</vssd:VirtualSystemType>
      </System>
      <Item>
        <rasd:AllocationUnits>hertz * 10^6</rasd:AllocationUnits>
        <rasd:Description>Number of Virtual CPUs</rasd:Description>
        <rasd:ElementName>%d virtual CPU(s)</rasd:ElementName>
        <rasd:InstanceID>1</rasd:InstanceID>
        <rasd:ResourceType>3</rasd:ResourceType>
        <rasd:VirtualQuantity>%d</rasd:VirtualQuantity>
      </Item>
      <Item>
        <rasd:AllocationUnits>byte * 2^20</rasd:AllocationUnits>
        <rasd:Description>Memory Size</rasd:Description>
        <rasd:ElementName>%d MB of memory</rasd:ElementName>
        <rasd:InstanceID>2</rasd:InstanceID>
        <rasd:ResourceType>4</rasd:ResourceType>
        <rasd:VirtualQuantity>%d</rasd:VirtualQuantity>
      </Item>
      <Item>
        <rasd:Address>0</rasd:Address>
        <rasd:Description>SCSI Controller</rasd:Description>
        <rasd:ElementName>SCSI Controller 0</rasd:ElementName>
        <rasd:InstanceID>3</rasd:InstanceID>
        <rasd:ResourceSubType>lsilogic</rasd:ResourceSubType>
        <rasd:ResourceType>6</rasd:ResourceType>
      </Item>
      <Item>
        <rasd:AddressOnParent>0</rasd:AddressOnParent>
        <rasd:ElementName>Hard disk 1</rasd:ElementName>
        <rasd:HostResource>ovf:/disk/%s</rasd:HostResource>
        <rasd:InstanceID>4</rasd:InstanceID>
        <rasd:Parent>3</rasd:Parent>
        <rasd:ResourceType>17</rasd:ResourceType>
      </Item>
      <Item>
        <rasd:AddressOnParent>0</rasd:AddressOnParent>
        <rasd:AutomaticAllocation>true</rasd:AutomaticAllocation>
        <rasd:Connection>%s</rasd:Connection>
        <rasd:ElementName>Network adapter 1</rasd:ElementName>
        <rasd:InstanceID>5</rasd:InstanceID>
        <rasd:ResourceSubType>vmxnet3</rasd:ResourceSubType>
        <rasd:ResourceType>10</rasd:ResourceType>
      </Item>
    </VirtualHardwareSection>
  </VirtualSystem>
</Envelope>
`, html.EscapeString(vmdkName), size, capacity, diskID, network, network, name, name, name, name, opts.CPUs, opts.CPUs, opts.MemoryMiB, opts.MemoryMiB, diskID, network), nil
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func hashName(path string) string {
	sum := uint32(2166136261)
	for i := 0; i < len(path); i++ {
		sum ^= uint32(path[i])
		sum *= 16777619
	}
	return fmt.Sprintf("%08x", sum)
}
