package copyappliance

import (
	"fmt"
	"net"
	"regexp"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
)

// snapshotVMDKPattern matches VMware snapshot delta suffixes such as -000003.vmdk.
var snapshotVMDKPattern = regexp.MustCompile(`-\d{6}\.vmdk$`)

// NbdURI builds a TCP NBD connection URI for an appliance export.
func NbdURI(host string, port int32) string {
	return fmt.Sprintf("nbd://%s:%d", host, port)
}

// baseVMDKPath strips a VMware snapshot suffix from a backing file path.
func baseVMDKPath(path string) string {
	if path == "" {
		return path
	}
	return snapshotVMDKPattern.ReplaceAllString(path, ".vmdk")
}

// ExportNbdConnections maps each attached VMDK path to its NBD connection URI.
func ExportNbdConnections(appliance *api.CopyAppliance) (map[string]string, error) {
	if appliance == nil {
		return nil, liberr.New("copy appliance is not set")
	}
	if len(appliance.Status.Addresses) == 0 {
		return nil, liberr.New("copy appliance has no guest address yet")
	}
	host := appliance.Status.Addresses[0].IP
	if host == "" {
		return nil, liberr.New("copy appliance guest address is empty")
	}
	if net.ParseIP(host) == nil {
		return nil, liberr.New("copy appliance guest address is not a valid IP", "address", host)
	}
	if len(appliance.Status.Exports) == 0 {
		return nil, liberr.New("copy appliance has no disk exports yet")
	}
	attached := appliance.Spec.AttachedDisks()
	if len(appliance.Status.Exports) < len(attached) {
		return nil, liberr.New(
			"copy appliance exports are incomplete",
			"exports", fmt.Sprintf("%d", len(appliance.Status.Exports)),
			"attached", fmt.Sprintf("%d", len(attached)))
	}

	connections := make(map[string]string, len(appliance.Status.Exports)*2)
	for _, export := range appliance.Status.Exports {
		if export.VMDKPath == "" {
			return nil, liberr.New("copy appliance export is missing a VMDK path")
		}
		uri := NbdURI(host, export.Port)
		connections[export.VMDKPath] = uri
		if base := baseVMDKPath(export.VMDKPath); base != export.VMDKPath {
			connections[base] = uri
		}
	}
	return connections, nil
}

// IsDeployReady reports whether the appliance finished deploying and can serve exports.
func IsDeployReady(appliance *api.CopyAppliance) bool {
	if appliance == nil {
		return false
	}
	if appliance.Status.Phase != PhaseDeployCompleted {
		return false
	}
	ready := appliance.Status.Conditions.FindCondition(libcnd.Ready)
	return ready != nil && ready.Status == libcnd.True
}
