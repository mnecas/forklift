package vsphere

import (
	"testing"

	model "github.com/kubev2v/forklift/pkg/controller/provider/model/vsphere"
)

func TestBuildFileCountIgnoresCopyAppliance(t *testing.T) {
	path := "[ds] guest/disk.vmdk"
	vms := []model.VM{
		{Disks: []model.Disk{{File: path}}},
		{
			Annotation: copyApplianceAnnotation,
			Disks:      []model.Disk{{File: path}},
		},
	}
	count := buildFileCount(vms)
	if count[path] != 1 {
		t.Fatalf("file count = %d, want 1 (appliance ignored)", count[path])
	}
	guest := vms[0]
	applySharedFlags(&guest, count)
	if guest.Disks[0].Shared {
		t.Fatal("guest disk Shared=true after appliance-only co-attach")
	}
}

func TestBuildFileCountMarksTrulyShared(t *testing.T) {
	path := "[ds] shared/disk.vmdk"
	vms := []model.VM{
		{Disks: []model.Disk{{File: path}}},
		{Disks: []model.Disk{{File: path}}},
	}
	count := buildFileCount(vms)
	if count[path] != 2 {
		t.Fatalf("file count = %d, want 2", count[path])
	}
	guest := vms[0]
	applySharedFlags(&guest, count)
	if !guest.Disks[0].Shared {
		t.Fatal("expected Shared=true for two real guests")
	}
}
