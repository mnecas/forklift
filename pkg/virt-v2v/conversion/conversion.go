package conversion

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/kubev2v/forklift/pkg/virt-v2v/config"
	"github.com/kubev2v/forklift/pkg/virt-v2v/customize"
	"github.com/kubev2v/forklift/pkg/virt-v2v/utils"
	"libvirt.org/go/libvirt"
	libvirtxml "libvirt.org/go/libvirtxml"
)

type Conversion struct {
	*config.AppConfig
	// Disks to be converted
	Disks []*Disk
	// Used for injecting mock to the builder
	CommandBuilder utils.CommandBuilder

	fileSystem utils.FileSystem
}

func NewConversion(env *config.AppConfig) (*Conversion, error) {
	conversion := Conversion{
		AppConfig:      env,
		CommandBuilder: &utils.CommandBuilderImpl{},
		fileSystem:     &utils.FileSystemImpl{},
	}

	disks, err := conversion.getDisk()
	if err != nil {
		return nil, err
	}
	conversion.Disks = disks

	return &conversion, nil
}

func (c *Conversion) getDisk() ([]*Disk, error) {
	var disks []*Disk
	diskPaths, err := filepath.Glob(config.FS)
	if err != nil {
		return nil, err
	}
	disksBlock, err := filepath.Glob(config.BLOCK)
	if err != nil {
		return nil, err
	}
	diskPaths = append(diskPaths, disksBlock...)
	for _, path := range diskPaths {
		disk, err := NewDisk(c.AppConfig, path)
		if err != nil {
			return nil, err
		}
		disks = append(disks, disk)
	}
	return disks, nil
}

// addCommonArgs adds v2v arguments which are shared between all virt-v2v commands
// (virt-v2v, virt-v2v-in-place, and virt-v2v-inspector)
func (c *Conversion) addCommonArgs(cmd utils.CommandBuilder) error {
	c.addRootArg(cmd)
	c.addResourceArgs(cmd)
	c.addStaticIPArgs(cmd)
	return c.addDiskUnlockArgs(cmd)
}

func (c *Conversion) addRootArg(cmd utils.CommandBuilder) {
	// Allow specifying which disk should be the bootable disk
	if c.RootDisk != "" {
		cmd.AddArg("--root", c.RootDisk)
	} else {
		cmd.AddArg("--root", "first")
	}
}

func (c *Conversion) addResourceArgs(cmd utils.CommandBuilder) {
	if c.MemSize > 0 {
		cmd.AddArg("--memsize", fmt.Sprintf("%d", c.MemSize))
	}
	if c.Smp > 0 {
		cmd.AddArg("--smp", fmt.Sprintf("%d", c.Smp))
	}
}

func (c *Conversion) addStaticIPArgs(cmd utils.CommandBuilder) {
	// Add the mapping to the virt-v2v, used mainly in the windows when migrating VMs with static IP
	if c.StaticIPs != "" {
		for _, mac := range strings.Split(c.StaticIPs, "_") {
			cmd.AddArg("--mac", mac)
		}
	}
}

func (c *Conversion) addDiskUnlockArgs(cmd utils.CommandBuilder) error {
	if c.NbdeClevis {
		cmd.AddArgs("--key", "all:clevis")
	} else if c.Luksdir != "" {
		// Adds LUKS keys, if they exist
		err := utils.AddLUKSKeys(c.fileSystem, cmd, c.Luksdir)
		if err != nil {
			return fmt.Errorf("error adding LUKS keys: %v", err)
		}
	}
	return nil
}

func (c *Conversion) addInspectionArgs(cmd utils.CommandBuilder) error {
	c.addRootArg(cmd)
	return c.addDiskUnlockArgs(cmd)
}

// addConversionExtraArgs adds extra args that apply ONLY to virt-v2v and virt-v2v-in-place
func (c *Conversion) addConversionExtraArgs(cmd utils.CommandBuilder) {
	if c.ExtraArgs != nil {
		cmd.AddExtraArgs(c.ExtraArgs...)
	}
}

// addInspectorExtraArgs adds extra args that apply ONLY to virt-v2v-inspector
func (c *Conversion) addInspectorExtraArgs(cmd utils.CommandBuilder) {
	if c.InspectorExtraArgs != nil {
		cmd.AddExtraArgs(c.InspectorExtraArgs...)
	}
}

// addNoFstrimUnlessXfsCompat passes --no-fstrim when the binary supports it
// and XFS compatibility mode is not enabled. The --no-fstrim flag is a
// RHEL-only downstream patch. upstream virt-v2v (CentOS/Fedora) lacks it.
func (c *Conversion) addNoFstrimUnlessXfsCompat(cmd utils.CommandBuilder) {
	if !c.XfsCompatibility && c.SupportsNoFstrim {
		cmd.AddFlag("--no-fstrim")
	}
}

func (c *Conversion) addVddkArgs(cmd utils.CommandBuilder, allowExtraArgOverride bool) {
	if info, err := os.Stat(c.VddkLibDir); err == nil && info.IsDir() {
		cmd.AddArg("-it", "vddk")
		cmd.AddArg("-io", fmt.Sprintf("vddk-libdir=%s", c.VddkLibDir))
		cmd.AddArg("-io", fmt.Sprintf("vddk-thumbprint=%s", c.Fingerprint))
		// Check if the config file exists but still allow the extra args to override the vddk-config for testing
		var extraArgs = c.ExtraArgs
		if _, err := os.Stat(c.VddkConfFile); !errors.Is(err, os.ErrNotExist) && (!allowExtraArgOverride || len(extraArgs) == 0) {
			cmd.AddArg("-io", fmt.Sprintf("vddk-config=%s", c.VddkConfFile))
		}
	}
}

func (c *Conversion) RunVirtV2VInspection() error {
	v2vCmdBuilder := c.CommandBuilder.New("virt-v2v-inspector").
		AddFlag("-v").
		AddFlag("-x").
		AddArg("-if", "raw").
		AddArg("-i", "disk").
		AddArg("-O", c.InspectionOutputFile)
	err := c.addCommonArgs(v2vCmdBuilder)
	if err != nil {
		return err
	}
	c.addNoFstrimUnlessXfsCompat(v2vCmdBuilder)
	c.addInspectorExtraArgs(v2vCmdBuilder)
	for _, disk := range c.Disks {
		v2vCmdBuilder.AddPositional(disk.Link)
	}
	v2vCmd := v2vCmdBuilder.Build()
	v2vCmd.SetStdout(os.Stdout)
	v2vCmd.SetStderr(os.Stderr)
	return v2vCmd.Run()
}

func (c *Conversion) RunVirtV2vInPlace() error {
	return c.runVirtV2vInPlace(nil)
}

// RunVirtV2vInPlaceDisk runs virt-v2v-in-place using disk mode (-i disk).
// This is used for providers like EC2 that don't have libvirt and where
// the disks are already populated and mounted as block devices or files.
func (c *Conversion) RunVirtV2vInPlaceDisk() error {
	return c.runVirtV2vInPlaceDisk(nil)
}

func (c *Conversion) addVirtV2vArgs(cmd utils.CommandBuilder) (err error) {
	outputName := c.NewVmName
	// HyperV uses -i disk, so virt-v2v derives -on from the input filename
	if outputName == "" && c.Source == config.HYPERV {
		outputName = c.VmName
	}
	cmd.AddFlag("-v").
		AddFlag("-x").
		AddArg("-o", "kubevirt").
		AddArg("-os", c.Workdir).
		AddArg("-on", outputName)
	switch c.Source {
	case config.VSPHERE:
		err = c.addVirtV2vVsphereArgs(cmd)
		if err != nil {
			return err
		}
	case config.OVA:
		c.virtV2vOVAArgs(cmd)
	case config.HYPERV:
		err = c.virtV2vHyperVArgs(cmd)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Conversion) addVirtV2vVsphereArgs(cmd utils.CommandBuilder) (err error) {
	cmd.AddArg("-i", "libvirt").
		AddArg("-ic", c.LibvirtUrl).
		AddArg("-ip", c.SecretKey).
		AddArg("--hostname", c.HostName)

	err = c.addCommonArgs(cmd)
	if err != nil {
		return err
	}
	c.addConversionExtraArgs(cmd)
	c.addVddkArgs(cmd, true)
	cmd.AddPositional("--")
	cmd.AddPositional(c.VmName)
	return nil
}

// addVirtV2vVsphereArgsForInspection adds vSphere-specific args WITHOUT conversion extra args
// This is used for remote inspection where we want inspector-specific args instead
func (c *Conversion) addVirtV2vVsphereArgsForInspection(cmd utils.CommandBuilder) (err error) {
	cmd.AddArg("-i", "libvirt").
		AddArg("-ic", c.LibvirtUrl).
		AddArg("-ip", c.SecretKey).
		AddArg("--hostname", c.HostName)

	err = c.addCommonArgs(cmd)
	if err != nil {
		return err
	}
	// Note: NO addConversionExtraArgs here - this is for inspection
	c.addVddkArgs(cmd, false)
	c.addNoFstrimUnlessXfsCompat(cmd)
	cmd.AddPositional("--")
	cmd.AddPositional(c.VmName)
	return nil
}

func (c *Conversion) addVirtV2vOpenVsphereArgs(cmd utils.CommandBuilder) error {
	cmd.AddArg("-i", "libvirt").
		AddArg("-ic", c.LibvirtUrl).
		AddArg("-ip", c.SecretKey).
		AddArg("--hostname", c.HostName)

	err := c.addInspectionArgs(cmd)
	if err != nil {
		return err
	}
	c.addVddkArgs(cmd, false)
	cmd.AddPositional("--")
	cmd.AddPositional(c.VmName)
	return nil
}

func (c *Conversion) virtV2vOVAArgs(cmd utils.CommandBuilder) {
	cmd.AddArg("-i", "ova")
	cmd.AddPositional(c.DiskPath)
}

func (c *Conversion) virtV2vHyperVArgs(cmd utils.CommandBuilder) error {
	cmd.AddArg("-i", "disk")
	if err := c.addCommonArgs(cmd); err != nil {
		return err
	}
	// Add disk paths as positional arguments (comma-separated in DiskPath)
	var addedDisks int
	for _, diskPath := range strings.Split(c.DiskPath, ",") {
		diskPath = strings.TrimSpace(diskPath)
		if diskPath != "" {
			cmd.AddPositional(diskPath)
			addedDisks++
		}
	}
	if addedDisks == 0 {
		return fmt.Errorf("no valid disk paths provided for HyperV conversion")
	}
	return nil
}

func (c *Conversion) addCustomizeArgs(cmd utils.CommandBuilder, osinfo *utils.InspectionOS) error {
	if osinfo == nil {
		return nil
	}
	custom := customize.NewCustomize(c.AppConfig, nil, *osinfo)
	return custom.AddVirtV2vCustomizationArgs(cmd)
}

func (c *Conversion) RunVirtV2v() error {
	return c.runVirtV2v(nil)
}

func (c *Conversion) RunVirtV2vWithCustomization(osinfo utils.InspectionOS) error {
	return c.runVirtV2v(&osinfo)
}

func (c *Conversion) runVirtV2v(osinfo *utils.InspectionOS) error {
	v2vCmdBuilder := c.CommandBuilder.New("virt-v2v")
	err := c.addVirtV2vArgs(v2vCmdBuilder)
	if err != nil {
		return err
	}
	if err := c.addCustomizeArgs(v2vCmdBuilder, osinfo); err != nil {
		return err
	}

	v2vCmd := v2vCmdBuilder.Build()
	// The virt-v2v-monitor reads the virt-v2v stdout and processes it and exposes the progress of the migration.
	monitorCmd := c.CommandBuilder.New("/usr/local/bin/virt-v2v-monitor").Build()
	monitorCmd.SetStdout(os.Stdout)
	monitorCmd.SetStderr(os.Stderr)

	pipe, writer := io.Pipe()
	monitorCmd.SetStdin(pipe)
	v2vCmd.SetStdout(writer)
	v2vCmd.SetStderr(writer)
	defer writer.Close()

	if err := monitorCmd.Start(); err != nil {
		fmt.Printf("Error executing monitor command: %v\n", err)
		return err
	}
	if err := v2vCmd.Run(); err != nil {
		fmt.Printf("Error executing v2v command: %v\n", err)
		return err
	}

	// virt-v2v is done, we can close the pipe to virt-v2v-monitor
	writer.Close()

	if err := monitorCmd.Wait(); err != nil {
		fmt.Printf("Error waiting for virt-v2v-monitor to finish: %v\n", err)
		return err
	}

	return nil
}

func (c *Conversion) RunVirtV2vInPlaceWithCustomization(osinfo utils.InspectionOS) error {
	return c.runVirtV2vInPlace(&osinfo)
}

func (c *Conversion) runVirtV2vInPlace(osinfo *utils.InspectionOS) error {
	v2vCmdBuilder := c.CommandBuilder.New("virt-v2v-in-place").
		AddFlag("-v").
		AddFlag("-x").
		AddArg("-i", "libvirtxml")
	err := c.addCommonArgs(v2vCmdBuilder)
	if err != nil {
		return err
	}
	c.addNoFstrimUnlessXfsCompat(v2vCmdBuilder)
	c.addConversionExtraArgs(v2vCmdBuilder)
	if err := c.addCustomizeArgs(v2vCmdBuilder, osinfo); err != nil {
		return err
	}
	v2vCmdBuilder.AddPositional(c.LibvirtDomainFile)
	v2vCmd := v2vCmdBuilder.Build()
	v2vCmd.SetStdout(os.Stdout)
	v2vCmd.SetStderr(os.Stderr)
	return v2vCmd.Run()
}

func (c *Conversion) RunCustomize(osinfo utils.InspectionOS) error {
	var disks []string
	for _, disk := range c.Disks {
		disks = append(disks, disk.Link)
	}
	custom := customize.NewCustomize(c.AppConfig, disks, osinfo)
	return custom.Run()
}

func (c *Conversion) RunRemoteV2vInspection() (err error) {
	v2vCmdBuilder := c.CommandBuilder.New("virt-v2v-inspector").
		AddFlag("-v").
		AddFlag("-x")

	err = c.addVirtV2vRemoteInspectionArgs(v2vCmdBuilder)
	if err != nil {
		return err
	}

	// Use the inspection-specific helper that doesn't add conversion extra args
	err = c.addVirtV2vVsphereArgsForInspection(v2vCmdBuilder)
	if err != nil {
		return err
	}
	c.addInspectorExtraArgs(v2vCmdBuilder)

	v2vCmd := v2vCmdBuilder.Build()
	v2vCmd.SetStdout(os.Stdout)
	v2vCmd.SetStderr(os.Stderr)
	return v2vCmd.Run()
}

func (c *Conversion) RunVirtV2vInPlaceDiskWithCustomization(osinfo utils.InspectionOS) error {
	return c.runVirtV2vInPlaceDisk(&osinfo)
}

func (c *Conversion) runVirtV2vInPlaceDisk(osinfo *utils.InspectionOS) error {
	if len(c.Disks) == 0 {
		return fmt.Errorf("no disks found for in-place conversion")
	}

	v2vCmdBuilder := c.CommandBuilder.New("virt-v2v-in-place").
		AddFlag("-v").
		AddFlag("-x").
		AddArg("-i", "disk")

	err := c.addCommonArgs(v2vCmdBuilder)
	if err != nil {
		return err
	}
	c.addNoFstrimUnlessXfsCompat(v2vCmdBuilder)
	c.addConversionExtraArgs(v2vCmdBuilder)
	if err := c.addCustomizeArgs(v2vCmdBuilder, osinfo); err != nil {
		return err
	}

	// Add all disks as positional arguments
	for _, disk := range c.Disks {
		v2vCmdBuilder.AddPositional(disk.Link)
	}

	v2vCmd := v2vCmdBuilder.Build()
	v2vCmd.SetStdout(os.Stdout)
	v2vCmd.SetStderr(os.Stderr)
	return v2vCmd.Run()
}

func (c *Conversion) InspectSource() (*utils.InspectionV2V, error) {
	rawInspectionFile := c.InspectionOutputFile + ".virt-inspector"
	if err := c.RunVirtV2VOpen(rawInspectionFile); err != nil {
		return nil, err
	}

	inspection, err := utils.GetInspectionV2vFromFile(rawInspectionFile)
	if err != nil {
		return nil, err
	}

	if err := utils.WriteInspectionV2vToFile(c.InspectionOutputFile, inspection); err != nil {
		return nil, err
	}

	if rawInspectionFile != c.InspectionOutputFile {
		_ = os.Remove(rawInspectionFile)
	}

	return inspection, nil
}

func (c *Conversion) RunVirtV2VOpen(outputFile string) error {
	v2vCmdBuilder := c.CommandBuilder.New("virt-v2v-open").
		AddFlag("-v").
		AddFlag("-x")

	if c.IsRemoteInspection {
		if err := c.addVirtV2vRemoteInspectionArgs(v2vCmdBuilder); err != nil {
			return err
		}
	}

	err := c.addVirtV2VOpenInputArgs(v2vCmdBuilder)
	if err != nil {
		return err
	}

	v2vCmdBuilder.AddArg("--run", fmt.Sprintf("virt-inspector --format=raw @@ > %s", outputFile))
	v2vCmd := v2vCmdBuilder.Build()
	v2vCmd.SetStdout(os.Stdout)
	v2vCmd.SetStderr(os.Stderr)
	return v2vCmd.Run()
}

func (c *Conversion) addVirtV2VOpenInputArgs(cmd utils.CommandBuilder) error {
	switch {
	case c.IsRemoteInspection:
		return c.addVirtV2vOpenVsphereArgs(cmd)
	case c.IsInPlace && c.LibvirtUrl != "":
		cmd.AddArg("-i", "libvirtxml")
		if err := c.addInspectionArgs(cmd); err != nil {
			return err
		}
		cmd.AddPositional(c.LibvirtDomainFile)
		return nil
	case c.IsInPlace:
		cmd.AddArg("-i", "disk")
		if err := c.addInspectionArgs(cmd); err != nil {
			return err
		}
		for _, disk := range c.Disks {
			cmd.AddPositional(disk.Link)
		}
		return nil
	}

	switch c.Source {
	case config.VSPHERE:
		return c.addVirtV2vOpenVsphereArgs(cmd)
	case config.OVA:
		cmd.AddArg("-i", "ova")
		if err := c.addInspectionArgs(cmd); err != nil {
			return err
		}
		cmd.AddPositional(c.DiskPath)
		return nil
	case config.HYPERV:
		cmd.AddArg("-i", "disk")
		if err := c.addInspectionArgs(cmd); err != nil {
			return err
		}
		var addedDisks int
		for _, diskPath := range strings.Split(c.DiskPath, ",") {
			diskPath = strings.TrimSpace(diskPath)
			if diskPath != "" {
				cmd.AddPositional(diskPath)
				addedDisks++
			}
		}
		if addedDisks == 0 {
			return fmt.Errorf("no valid disk paths provided for HyperV inspection")
		}
		return nil
	default:
		return fmt.Errorf("unsupported source %q for virt-v2v-open inspection", c.Source)
	}
}

func (c *Conversion) addVirtV2vRemoteInspectionArgs(cmd utils.CommandBuilder) (err error) {
	if len(c.RemoteInspectionDisks) == 0 {
		return fmt.Errorf("No remote disks were supplied")
	}
	for _, disk := range c.RemoteInspectionDisks {
		cmd.AddArg("-io", fmt.Sprintf("vddk-file=%s", disk))
	}
	return
}

// retrieve and modify the domain XML from libvirt
func (c *Conversion) GetDomainXML() (string, error) {
	libvirtURL, err := url.Parse(c.LibvirtUrl)

	if err != nil {
		return "", fmt.Errorf("failed to parse libvirt URL: %w", err)
	}

	usernameData, err := os.ReadFile(c.AccessKeyId)
	if err != nil {
		return "", fmt.Errorf("failed to read username from secret: %w", err)
	}
	username := string(usernameData)

	passwordData, err := os.ReadFile(c.SecretKey)
	if err != nil {
		return "", fmt.Errorf("failed to read password from secret: %w", err)
	}
	password := string(passwordData)

	auth := &libvirt.ConnectAuth{
		CredType: []libvirt.ConnectCredentialType{
			libvirt.CRED_AUTHNAME,
			libvirt.CRED_PASSPHRASE,
		},
		Callback: func(creds []*libvirt.ConnectCredential) {
			for _, cred := range creds {
				switch cred.Type {
				case libvirt.CRED_AUTHNAME:
					cred.Result = username
					cred.ResultLen = len(username)
				case libvirt.CRED_PASSPHRASE:
					cred.Result = password
					cred.ResultLen = len(password)
				}
			}
		},
	}

	conn, err := libvirt.NewConnectWithAuth(libvirtURL.String(), auth, 0)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	domain, err := conn.LookupDomainByName(c.VmName)
	if err != nil {
		return "", fmt.Errorf("failed to lookup domain %s: %w", c.VmName, err)
	}
	defer func() {
		if err := domain.Free(); err != nil {
			fmt.Printf("Failed to free libvirt domain: %s", err)
		}
	}()

	domainXML, err := domain.GetXMLDesc(0)
	if err != nil {
		return "", fmt.Errorf("failed to get domain XML: %w", err)
	}

	modifiedXML, err := c.updateDiskPaths(domainXML)
	if err != nil {
		return "", fmt.Errorf("failed to update disk paths in domain XML: %w", err)
	}

	return modifiedXML, nil
}

func updateDiskSource(disk *libvirtxml.DomainDisk, path string) bool {
	if disk.Source == nil {
		return false
	}

	switch {
	case disk.Source.File != nil:
		disk.Source.File.File = path
	case disk.Source.Block != nil:
		disk.Source.Block.Dev = path
	default:
		return false
	}
	return true
}

// modify the domain XML to use the local disk paths for in-place conversions
func (c *Conversion) updateDiskPaths(domainXML string) (string, error) {
	fmt.Printf("Updating disk paths: found %d disks\n", len(c.Disks))
	domain := &libvirtxml.Domain{}
	err := domain.Unmarshal(domainXML)
	if err != nil {
		return "", fmt.Errorf("failed to parse domain XML: %w", err)
	}
	updatedDisks := []libvirtxml.DomainDisk{}
	diskIdx := 0
	for _, disk := range domain.Devices.Disks {
		if diskIdx >= len(c.Disks) {
			fmt.Printf("WARNING: disk %d in domain XML but only %d disks available\n", diskIdx, len(c.Disks))
			break
		}
		if disk.Device == "cdrom" {
			continue
		}
		newPath := c.Disks[diskIdx].Link
		if updated := updateDiskSource(&disk, newPath); updated {
			disk.Driver = &libvirtxml.DomainDiskDriver{
				Name: "qemu",
				Type: "qcow2",
			}
			updatedDisks = append(updatedDisks, disk)
		}
		diskIdx++
	}
	domain.Devices.Disks = updatedDisks

	modifiedXML, err := domain.Marshal()
	if err != nil {
		return "", fmt.Errorf("failed to marshal modified domain XML: %w", err)
	}

	return modifiedXML, nil
}
