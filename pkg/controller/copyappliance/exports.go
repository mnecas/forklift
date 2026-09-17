package copyappliance

import (
	"fmt"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/lib/diskid"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/kubev2v/forklift/pkg/nbd-container/runner"
)

// matchExports pairs announced appliance exports with the disks the controller
// asked vSphere to attach. Every attached disk must match exactly one export
// by serial/WWID; unmatched exports are rejected.
func matchExports(attached []api.AttachedDisk, announced []runner.Export) ([]api.ApplianceExport, error) {
	if len(announced) < len(attached) {
		return nil, errExportsIncomplete
	}

	used := make([]bool, len(announced))
	matched := make([]api.ApplianceExport, 0, len(attached))

	for _, disk := range attached {
		idx, err := findExportIndex(disk, announced, used)
		if err != nil {
			return nil, err
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

func findExportIndex(disk api.AttachedDisk, announced []runner.Export, used []bool) (int, error) {
	if disk.Serial != "" {
		var matches []int
		for i, export := range announced {
			if used[i] {
				continue
			}
			if diskid.Match(disk.Serial, export.WWID) {
				matches = append(matches, i)
			}
		}
		switch len(matches) {
		case 1:
			return matches[0], nil
		case 0:
			return -1, liberr.New(
				"no appliance export matches the attached disk serial",
				"vmdk", disk.VMDKPath,
				"serial", disk.Serial)
		default:
			return -1, liberr.New(
				"more than one appliance export matches the attached disk serial",
				"vmdk", disk.VMDKPath,
				"serial", disk.Serial)
		}
	}

	// Without a serial, fall back to a unique capacity match.
	if disk.Capacity <= 0 {
		return -1, liberr.New(
			"attached disk has no serial or capacity to match exports with",
			"vmdk", disk.VMDKPath)
	}
	var matches []int
	for i, export := range announced {
		if used[i] {
			continue
		}
		if int64(export.Size) == disk.Capacity {
			matches = append(matches, i)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return -1, liberr.New(
			"no appliance export matches the attached disk capacity",
			"vmdk", disk.VMDKPath,
			"capacity", fmt.Sprintf("%d", disk.Capacity))
	default:
		return -1, liberr.New(
			"more than one appliance export matches the attached disk capacity",
			"vmdk", disk.VMDKPath,
			"capacity", fmt.Sprintf("%d", disk.Capacity))
	}
}
