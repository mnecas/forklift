package blockdev

import (
	"context"
	"strings"
	"testing"
)

// Synthetic lsblk JSON exercising every selection rule. Values are quoted strings and
// mountpoints are null/singular to mirror older util-linux (util-linux < 2.37), which
// also exercises the flexBool/flexUint64 decoders. Layout:
//
//	sda    disk, partition mounted at "/"      -> excluded (root)
//	sdb    disk, nothing mounted in subtree    -> CANDIDATE
//	sdc    disk, read-only                     -> excluded (ro)
//	zram0  disk, pseudo (zram* prefix)         -> excluded
//	sr0    rom                                 -> excluded (not a disk)
//	sdd    disk, nested lvm mounted at "/home" -> excluded (subtree mount)
const fakeLsblk = `{
  "blockdevices": [
    { "name": "sda", "type": "disk", "ro": "0", "size": "1000000", "mountpoint": null,
      "children": [
        { "name": "sda1", "type": "part", "ro": "0", "size": "1000000", "mountpoint": "/" }
      ] },
    { "name": "sdb", "type": "disk", "ro": "0", "size": "2048", "mountpoint": null,
      "children": [
        { "name": "sdb1", "type": "part", "ro": "0", "size": "2000", "mountpoint": null }
      ] },
    { "name": "sdc", "type": "disk", "ro": "1", "size": "512", "mountpoint": null },
    { "name": "zram0", "type": "disk", "ro": "0", "size": "4096", "mountpoint": "[SWAP]" },
    { "name": "sr0", "type": "rom", "ro": "1", "size": "999", "mountpoint": null },
    { "name": "sdd", "type": "disk", "ro": "0", "size": "8192", "mountpoint": null,
      "children": [
        { "name": "sdd1", "type": "part", "ro": "0", "size": "8192", "mountpoint": null,
          "children": [
            { "name": "vg-home", "type": "lvm", "ro": "0", "size": "8000", "mountpoint": "/home" }
          ] }
      ] }
  ]
}`

func TestParseSelectsOnlyUnmountedNonRootDisks(t *testing.T) {
	// Deterministic WWID resolver so the test doesn't shell out to udevadm.
	resolve := func(_ context.Context, path string) string { return "wwn-" + path }

	devices, err := parse(strings.NewReader(fakeLsblk), resolve)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Only /dev/sdb qualifies (see fakeLsblk layout above).
	if len(devices) != 1 {
		t.Fatalf("expected 1 candidate, got %d: %+v", len(devices), devices)
	}
	got := devices[0]
	if got.Path != "/dev/sdb" {
		t.Errorf("path = %q, want /dev/sdb", got.Path)
	}
	if got.WWID != "wwn-/dev/sdb" {
		t.Errorf("wwid = %q, want wwn-/dev/sdb", got.WWID)
	}
	if got.Size != 2048 {
		t.Errorf("size = %d, want 2048", got.Size)
	}
}

func TestParseUdevPropsPrefersWWNThenSerial(t *testing.T) {
	tests := []struct {
		name  string
		props string
		want  string
	}{
		{
			name:  "wwn present",
			props: "DEVTYPE=disk\nID_WWN=eui.0025388411b1e8d8\nID_SERIAL=SAMSUNG_x\n",
			want:  "eui.0025388411b1e8d8",
		},
		{
			name:  "no wwn falls back to serial",
			props: "DEVTYPE=disk\nID_SERIAL=PNY_USB_071C49142511A667\nID_SERIAL_SHORT=071C\n",
			want:  "PNY_USB_071C49142511A667",
		},
		{
			name:  "only serial_short",
			props: "DEVTYPE=disk\nID_SERIAL_SHORT=071C\n",
			want:  "071C",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			props := parseUdevProps(strings.NewReader(tt.props))
			var got string
			for _, key := range []string{"ID_WWN", "ID_SERIAL", "ID_SERIAL_SHORT"} {
				if v := strings.TrimSpace(props[key]); v != "" {
					got = v
					break
				}
			}
			if got != tt.want {
				t.Errorf("resolved %q, want %q", got, tt.want)
			}
		})
	}
}
