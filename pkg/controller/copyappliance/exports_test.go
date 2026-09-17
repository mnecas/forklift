package copyappliance

import (
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/nbd-container/runner"
)

func TestMatchExports(t *testing.T) {
	attached := []api.AttachedDisk{
		{
			VMDKPath: "[ds] vm/disk-0.vmdk",
			DiskKey:  2000,
			Serial:   "6000C297-7d53-fad7-e8b4-5194193802f7",
			Capacity: 16 << 30,
		},
		{
			VMDKPath: "[ds] vm/disk-1.vmdk",
			DiskKey:  2001,
			Serial:   "6000C2902b72f55a-2146-4350-72ab-cdef01234567",
			Capacity: 2 << 30,
		},
	}
	announced := []runner.Export{
		{WWID: "36000c2902b72f55a2146435072abcdef01", Port: 10810, Device: "/dev/sdc", Size: 2 << 30},
		{WWID: "36000c2977d53fad7e8b45194193802f7", Port: 10809, Device: "/dev/sdb", Size: 16 << 30},
	}

	matched, err := matchExports(attached, announced)
	if err != nil {
		t.Fatalf("matchExports: %v", err)
	}
	if len(matched) != 2 {
		t.Fatalf("matched %d exports, want 2", len(matched))
	}
	if matched[0].DiskKey != 2000 || matched[0].Port != 10809 {
		t.Errorf("first export = %+v, want disk 2000 on port 10809", matched[0])
	}
	if matched[1].DiskKey != 2001 || matched[1].Port != 10810 {
		t.Errorf("second export = %+v, want disk 2001 on port 10810", matched[1])
	}
}

func TestMatchExportsRejectsAmbiguousSerial(t *testing.T) {
	attached := []api.AttachedDisk{
		{Serial: "6000C297-7d53-fad7-e8b4-5194193802f7", VMDKPath: "[ds] a.vmdk"},
	}
	announced := []runner.Export{
		{WWID: "36000c2977d53fad7e8b45194193802f7", Port: 10809, Device: "/dev/sdb"},
		{WWID: "36000c2977d53fad7e8b45194193802f7", Port: 10810, Device: "/dev/sdc"},
	}

	_, err := matchExports(attached, announced)
	if err == nil {
		t.Fatal("matchExports succeeded with duplicate serial matches")
	}
}
