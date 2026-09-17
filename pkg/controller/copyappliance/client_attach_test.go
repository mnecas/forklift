package copyappliance

import (
	"testing"
)

func TestAttachedDiskPathSet(t *testing.T) {
	appliance := testAppliance()
	paths := attachedDiskPathSet(appliance.Spec)
	if len(paths) != 2 {
		t.Fatalf("expected 2 attached disk paths, got %d", len(paths))
	}
	if !paths["[datastore13] vm-a/disk-0.vmdk"] {
		t.Fatal("expected vm-a disk path")
	}
}
