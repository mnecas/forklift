package copyappliance

import (
	"strings"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/kubev2v/forklift/pkg/nbd-container/runner"
)

// matchExports finds the announced NBD export for each attached disk by serial.
// Extra announced exports are ignored; WaitForExports already waits until the
// guest has at least as many exports as attached disks.
func matchExports(attached []api.AttachedDisk, announced []runner.Export) ([]api.ApplianceExport, error) {
	byID := make(map[string]runner.Export, len(announced))
	for _, export := range announced {
		if id := normalizeDiskID(export.WWID); id != "" {
			byID[id] = export
		}
	}

	matched := make([]api.ApplianceExport, 0, len(attached))
	for _, disk := range attached {
		id := normalizeDiskID(disk.Serial)
		if id == "" {
			return nil, liberr.New(
				"attached disk has no serial to match exports with",
				"vmdk", disk.VMDKPath)
		}
		export, ok := byID[id]
		if !ok {
			return nil, liberr.New(
				"no appliance export matches the attached disk serial",
				"vmdk", disk.VMDKPath,
				"serial", disk.Serial)
		}
		matched = append(matched, api.ApplianceExport{
			WWID:         export.WWID,
			Port:         int32(export.Port), // #nosec G115
			Device:       export.Device,
			DiskKey:      disk.DiskKey,
			VMDKPath:     disk.VMDKPath,
			SourceSerial: disk.Serial,
		})
	}
	return matched, nil
}

// normalizeDiskID strips formatting so a VMware backing.Uuid matches a guest scsi_id WWID.
func normalizeDiskID(id string) string {
	s := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(id), "-", ""))
	// scsi_id prefixes NAA identifiers with "3".
	if len(s) > 1 && s[0] == '3' && strings.HasPrefix(s[1:], "6000c29") {
		s = s[1:]
	}
	return s
}
