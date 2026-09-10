package vsphere

import (
	"errors"
	"fmt"
	"testing"

	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/vmware/govmomi/vim25/types"
)

func TestApplianceControllerCount(t *testing.T) {
	tests := []struct {
		disks int
		want  int
	}{
		{0, 0},
		{1, 1},
		{15, 1},
		{16, 2},
		{30, 2},
		{31, 3},
		{60, 4},
		{61, 5},
	}
	for _, tc := range tests {
		if got := controllerCount(tc.disks); got != tc.want {
			t.Errorf("controllerCount(%d) = %d, want %d", tc.disks, got, tc.want)
		}
	}
}

func TestApplianceDiskPlacement(t *testing.T) {
	tests := []struct {
		disk     int
		wantBus  int32
		wantUnit int32
	}{
		{0, 0, 0},
		{6, 0, 6},
		// Unit 7 is reserved for the controller, so slot 7 lands on unit 8.
		{7, 0, 8},
		{13, 0, 14},
		{14, 0, 15},
		// Bus 1 starts over at unit 0.
		{15, 1, 0},
		{29, 1, 15},
		{30, 2, 0},
		{59, 3, 15},
	}
	for _, tc := range tests {
		bus, unit := diskPlacement(tc.disk)
		if bus != tc.wantBus || unit != tc.wantUnit {
			t.Errorf("diskPlacement(%d) = (bus %d, unit %d), want (bus %d, unit %d)",
				tc.disk, bus, unit, tc.wantBus, tc.wantUnit)
		}
	}
}

// The 60-disk ceiling is 4 SCSI controllers (the vSphere per-VM maximum) times
// 15 addressable units each. Every disk below it must land on a valid bus and
// never on the reserved controller unit.
func TestApplianceDiskPlacementWithinLimits(t *testing.T) {
	const maxDisks = 4 * disksPerController
	seen := map[[2]int32]int{}
	for i := 0; i < maxDisks; i++ {
		bus, unit := diskPlacement(i)
		if bus < 0 || bus > 3 {
			t.Fatalf("disk %d: bus %d outside the 4-controller limit", i, bus)
		}
		if unit == scsiControllerUnit {
			t.Fatalf("disk %d: unit %d is reserved for the controller", i, unit)
		}
		if unit < 0 || unit > 15 {
			t.Fatalf("disk %d: unit %d outside the addressable range", i, unit)
		}
		slot := [2]int32{bus, unit}
		if prev, dup := seen[slot]; dup {
			t.Fatalf("disk %d collides with disk %d at bus %d unit %d", i, prev, bus, unit)
		}
		seen[slot] = i
	}
	// The first disk past the cap is what CRD MaxItems protects against.
	if bus, _ := diskPlacement(maxDisks); bus != 4 {
		t.Errorf("diskPlacement(%d) bus = %d, want 4 (the overflow case)", maxDisks, bus)
	}
}

// Device keys are temporary negative values that vCenter reassigns on creation.
// They must not collide across the controller and disk ranges.
func TestApplianceDeviceKeysUnique(t *testing.T) {
	const maxDisks = 4 * disksPerController
	keys := map[int32]string{}
	for c := 0; c < controllerCount(maxDisks); c++ {
		key := scsiController(c).Key
		if owner, dup := keys[key]; dup {
			t.Fatalf("controller %d key %d collides with %s", c, key, owner)
		}
		keys[key] = fmt.Sprintf("controller %d", c)
	}
	for i := 0; i < maxDisks; i++ {
		key := diskBaseKey - int32(i)
		if owner, dup := keys[key]; dup {
			t.Fatalf("disk %d key %d collides with %s", i, key, owner)
		}
		keys[key] = fmt.Sprintf("disk %d", i)
	}
	for i := 0; i < maxNICs; i++ {
		key := nicBaseKey - int32(i)
		if owner, dup := keys[key]; dup {
			t.Fatalf("NIC %d key %d collides with %s", i, key, owner)
		}
		keys[key] = fmt.Sprintf("nic %d", i)
	}
	for key := range keys {
		if key >= 0 {
			t.Errorf("key %d (%s) is not negative", key, keys[key])
		}
	}
}

func TestApplianceScsiController(t *testing.T) {
	for c := 0; c < 4; c++ {
		ctrl := scsiController(c)
		if ctrl.BusNumber != int32(c) {
			t.Errorf("controller %d: BusNumber = %d, want %d", c, ctrl.BusNumber, c)
		}
		if ctrl.ScsiCtlrUnitNumber != scsiControllerUnit {
			t.Errorf("controller %d: ScsiCtlrUnitNumber = %d, want %d",
				c, ctrl.ScsiCtlrUnitNumber, scsiControllerUnit)
		}
		if ctrl.SharedBus != types.VirtualSCSISharingNoSharing {
			t.Errorf("controller %d: SharedBus = %v, want no sharing", c, ctrl.SharedBus)
		}
	}
}

// ErrVMNotFound tells the reconciler to rebuild rather than fail permanently,
// so the branch has to actually fire. It is returned wrapped in liberr, whose
// Unwrap is not the stdlib's, so the match has to be verified rather than
// assumed.
func TestApplianceVMNotFoundSurvivesWrapping(t *testing.T) {
	wrapped := liberr.Wrap(ErrVMNotFound, "vm", "vm-42")
	if !errors.Is(wrapped, ErrVMNotFound) {
		t.Error("errors.Is failed to see ErrVMNotFound through liberr.Wrap")
	}
	if errors.Is(wrapped, errors.New("unrelated")) {
		t.Error("ErrVMNotFound matched an unrelated error")
	}
}

func TestParseDatastoreName(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"[datastore13] some-vm/disk-0.vmdk", "datastore13"},
		{"[datastore13]some-vm/disk-0.vmdk", "datastore13"},
		{"[ds with spaces] vm/disk.vmdk", "ds with spaces"},
		{"[datastore13]", "datastore13"},
		{"", ""},
		{"/vmfs/volumes/datastore13/vm/disk.vmdk", ""},
		{"no-brackets.vmdk", ""},
	}
	for _, tc := range tests {
		if got := parseDatastoreName(tc.path); got != tc.want {
			t.Errorf("parseDatastoreName(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
