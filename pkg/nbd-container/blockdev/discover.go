// Package blockdev discovers host block devices that are candidates for export
// over NBD: additional disks that are not the root disk and are not in use by the
// host (no mountpoints anywhere in their subtree).
package blockdev

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Device is a discovered block device eligible for export.
type Device struct {
	// WWID is a stable identifier for the disk (SCSI/NVMe WWN, or a serial-based
	// fallback). Used to label the container so re-runs are idempotent.
	WWID string
	// Path is the device node, e.g. /dev/sdb.
	Path string
	// Size is the device size in bytes.
	Size uint64
	// ScsiAddr is the H:C:T:L address used to order exports deterministically.
	ScsiAddr string
}

// lsblkNode mirrors the JSON emitted by `lsblk -J -b -o NAME,TYPE,RO,SIZE,MOUNTPOINT`.
//
// This deliberately uses the older, widely-supported column set: the `PATH` column
// (util-linux 2.31+) and the plural `MOUNTPOINTS` array (util-linux 2.37+) are absent
// on the RHEL 8-era targets we run on, so we request the singular `MOUNTPOINT` and
// derive the device node from the name instead. Older lsblk also emits every JSON
// value as a quoted string (e.g. "ro":"0", "size":"123") whereas newer versions emit
// typed values (false, 123); flexBool/flexUint64 accept both.
type lsblkNode struct {
	Name       string      `json:"name"`
	Type       string      `json:"type"`
	RO         flexBool    `json:"ro"`
	Size       flexUint64  `json:"size"`
	MountPoint string      `json:"mountpoint"`
	Children   []lsblkNode `json:"children"`
}

type lsblkOutput struct {
	BlockDevices []lsblkNode `json:"blockdevices"`
}

// flexBool decodes a JSON boolean that lsblk may render as a real bool (true/false)
// or, on older util-linux, as a quoted string ("0"/"1").
type flexBool bool

func (f *flexBool) UnmarshalJSON(b []byte) error {
	switch strings.Trim(string(b), `"`) {
	case "1", "true":
		*f = true
	default:
		*f = false
	}
	return nil
}

// flexUint64 decodes a JSON integer that lsblk may render as a number or, on older
// util-linux, as a quoted string.
type flexUint64 uint64

func (f *flexUint64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return err
	}
	*f = flexUint64(v)
	return nil
}

// wwidResolver resolves a device path to a stable identifier. Overridable in tests.
type wwidResolver func(ctx context.Context, path string) string

// Discover runs lsblk + udevadm on the host and returns the exportable devices.
func Discover(ctx context.Context) ([]Device, error) {
	out, err := exec.CommandContext(ctx, "lsblk", "-J", "-b",
		"-o", "NAME,TYPE,RO,SIZE,MOUNTPOINT").Output()
	if err != nil {
		// .Output() captures the child's stderr into ExitError.Stderr; include it so
		// failures like an unsupported column name aren't reduced to "exit status 1".
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(bytes.TrimSpace(ee.Stderr)) > 0 {
			return nil, fmt.Errorf("running lsblk: %w: %s", err, bytes.TrimSpace(ee.Stderr))
		}
		return nil, fmt.Errorf("running lsblk: %w", err)
	}
	return parse(bytes.NewReader(out), func(ctx context.Context, path string) string {
		return resolveWWID(ctx, path)
	})
}

// parse selects candidate devices from lsblk JSON. It is separated from Discover so
// it can be unit-tested against captured fixtures.
func parse(r io.Reader, resolve wwidResolver) ([]Device, error) {
	var parsed lsblkOutput
	if err := json.NewDecoder(r).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decoding lsblk output: %w", err)
	}

	var devices []Device
	for _, node := range parsed.BlockDevices {
		if !isCandidate(node) {
			continue
		}
		// The PATH column isn't available on older lsblk, so build the device node
		// from the kernel name. Top-level candidates are always real disks (sda,
		// nvme0n1, ...), for which /dev/<name> is the canonical path.
		path := "/dev/" + node.Name
		ctx := context.Background()
		wwid := resolve(ctx, path)
		devices = append(devices, Device{
			WWID:     wwid,
			Path:     path,
			Size:     uint64(node.Size),
			ScsiAddr: scsiAddress(path),
		})
	}
	sortDevices(devices)
	return devices, nil
}

// isCandidate reports whether a top-level device should be exported: a writable
// physical disk with nothing mounted anywhere in its subtree (which excludes the
// root disk, swap-holding disks, and any host-mounted data disk).
func isCandidate(n lsblkNode) bool {
	if n.Type != "disk" {
		return false
	}
	if n.RO {
		return false
	}
	// Skip pseudo/virtual disks that are never real export targets.
	for _, prefix := range []string{"zram", "loop", "sr"} {
		if strings.HasPrefix(n.Name, prefix) {
			return false
		}
	}
	return !hasMountpoint(n)
}

// hasMountpoint reports whether this node or any descendant has a non-empty mountpoint.
func hasMountpoint(n lsblkNode) bool {
	if strings.TrimSpace(n.MountPoint) != "" {
		return true
	}
	for _, c := range n.Children {
		if hasMountpoint(c) {
			return true
		}
	}
	return false
}

// resolveWWID prefers scsi_id output because it matches VMware backing.Uuid
// when disk.EnableUUID is enabled, then falls back to udev properties.
func resolveWWID(ctx context.Context, path string) string {
	if id := scsiID(ctx, path); id != "" {
		return id
	}
	out, err := exec.CommandContext(ctx, "udevadm", "info",
		"--query=property", "--name", path).Output()
	if err != nil {
		return path
	}
	props := parseUdevProps(bytes.NewReader(out))
	for _, key := range []string{"ID_WWN", "ID_SERIAL", "ID_SERIAL_SHORT"} {
		if v := strings.TrimSpace(props[key]); v != "" {
			return v
		}
	}
	return path
}

func scsiID(ctx context.Context, path string) string {
	for _, bin := range []string{"/usr/lib/udev/scsi_id", "/lib/udev/scsi_id"} {
		out, err := exec.CommandContext(ctx, bin,
			"--whitelisted", "--replace-whitespace", "--device="+path).Output()
		if err != nil {
			continue
		}
		if id := strings.TrimSpace(string(out)); id != "" {
			return id
		}
	}
	return ""
}

func scsiAddress(path string) string {
	name := strings.TrimPrefix(path, "/dev/")
	dir := filepath.Join("/sys/block", name, "device", "scsi_disk")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return path
	}
	return entries[0].Name()
}

func sortDevices(devices []Device) {
	slices.SortFunc(devices, func(a, b Device) int {
		switch {
		case a.ScsiAddr < b.ScsiAddr:
			return -1
		case a.ScsiAddr > b.ScsiAddr:
			return 1
		default:
			return strings.Compare(a.Path, b.Path)
		}
	})
}

// parseUdevProps parses `udevadm info --query=property` KEY=VALUE lines.
func parseUdevProps(r io.Reader) map[string]string {
	props := make(map[string]string)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		key, val, ok := strings.Cut(scanner.Text(), "=")
		if !ok {
			continue
		}
		props[key] = val
	}
	return props
}
