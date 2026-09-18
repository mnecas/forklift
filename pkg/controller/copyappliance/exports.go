package copyappliance

import (
	"errors"
	"strings"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/kubev2v/forklift/pkg/nbd-container/runner"
)

var errExportsIncomplete = errors.New("not all attached disks are exported yet")

// matchExports pairs announced appliance exports with the disks the controller
// asked vSphere to attach. Every attached disk must match exactly one export
// by serial/WWID; unmatched exports are rejected.
func matchExports(attached []api.AttachedDisk, announced []runner.Export) ([]api.ApplianceExport, error) {
	used := make([]bool, len(announced))
	matched := make([]api.ApplianceExport, 0, len(attached))

	for _, disk := range attached {
		if disk.Serial == "" {
			return nil, liberr.New(
				"attached disk has no serial to match exports with",
				"vmdk", disk.VMDKPath)
		}
		idx := -1
		for i, export := range announced {
			if used[i] || !sameDiskID(disk.Serial, export.WWID) {
				continue
			}
			if idx >= 0 {
				return nil, liberr.New(
					"more than one appliance export matches the attached disk serial",
					"vmdk", disk.VMDKPath,
					"serial", disk.Serial)
			}
			idx = i
		}
		if idx < 0 {
			return nil, liberr.New(
				"no appliance export matches the attached disk serial",
				"vmdk", disk.VMDKPath,
				"serial", disk.Serial)
		}
		used[idx] = true
		export := announced[idx]
		matched = append(matched, api.ApplianceExport{
			WWID:         export.WWID,
			Port:         int32(export.Port), // #nosec G115
			Device:       export.Device,
			DiskKey:      disk.DiskKey,
			VMDKPath:     disk.VMDKPath,
			SourceSerial: disk.Serial,
		})
	}

	for i, export := range announced {
		if !used[i] {
			return nil, liberr.New(
				"the appliance announced an export that does not match any attached disk",
				"wwid", export.WWID,
				"device", export.Device)
		}
	}
	return matched, nil
}

// sameDiskID compares a VMware backing.Uuid to a guest scsi_id WWID.
func sameDiskID(serial, wwid string) bool {
	a := normalizeDiskID(serial)
	return a != "" && a == normalizeDiskID(wwid)
}

func normalizeDiskID(id string) string {
	s := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(id), "-", ""))
	// scsi_id prefixes NAA identifiers with "3".
	if len(s) > 1 && s[0] == '3' && strings.HasPrefix(s[1:], "6000c29") {
		s = s[1:]
	}
	return s
}
