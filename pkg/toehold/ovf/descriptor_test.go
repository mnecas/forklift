package ovf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDescriptorStream(t *testing.T) {
	out, err := Descriptor(DescriptorOptions{
		Name:         "nbdkit-toehold",
		Network:      "VM Network",
		CPUs:         2,
		MemoryMiB:    4096,
		StreamSize:   512 * 1024 * 1024,
		DiskCapacity: 10 * 1024 * 1024 * 1024,
		VMDKFileName: "disk-0.vmdk",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateDescriptor(out, "nbdkit-toehold"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Red Hat Enterprise Linux 9 (64-bit)") {
		t.Fatal("expected RHEL 9 guest OS in ovf")
	}
	if !strings.Contains(out, `diskId="disk-0"`) {
		t.Fatal("expected default disk-0 id when disk hash unset")
	}
}

func TestDescriptorDiskHash(t *testing.T) {
	out, err := Descriptor(DescriptorOptions{
		Name:         "tpl",
		StreamSize:   1,
		DiskCapacity: 1,
		VMDKFileName: "disk-0.vmdk",
		DiskHash:     "abcdef0123456789",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `diskId="vdisk-abcdef01"`) {
		t.Fatal("expected disk id derived from disk hash prefix")
	}
}

func TestDescriptor(t *testing.T) {
	dir := t.TempDir()
	vmdk := filepath.Join(dir, "disk.vmdk")
	if err := os.WriteFile(vmdk, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := Descriptor(DescriptorOptions{
		VMDKPath:  vmdk,
		Name:      "nbdkit-toehold",
		Network:   "VM Network",
		CPUs:      2,
		MemoryMiB: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateDescriptor(out, "nbdkit-toehold"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "disk.vmdk") {
		t.Fatal("expected vmdk href in ovf")
	}
}
