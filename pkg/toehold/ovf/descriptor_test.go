package ovf

import (
	"os"
	"path/filepath"
	"testing"
)

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
	if !contains(out, "disk.vmdk") {
		t.Fatal("expected vmdk href in ovf")
	}
}
