// Package copyappliance deploys and tears down the copy appliance VM that a
// migration reads source disks through. The appliance is a clone of a template
// in the source vCenter, and the CopyAppliance CR is its lifecycle.
package copyappliance

import (
	"fmt"
	"net"
	"regexp"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/kubev2v/forklift/pkg/lib/logging"
	"github.com/kubev2v/forklift/pkg/settings"
)

// Name of the controller, used for its logger and its event source.
const Name = "copy-appliance"

// Settings are the forklift settings the controller reads.
var Settings = &settings.Settings

var log = logging.WithName(Name)

// Phases of the deploy and teardown itineraries. The runners record where they
// got to as one of these, and pick up from it on the next reconcile.
const (
	PhaseDeployFailed        = "DeployFailed"
	PhaseCloneVM             = "CloneVM"
	PhaseWaitForClone        = "WaitForClone"
	PhaseWaitForNetwork      = "WaitForNetwork"
	PhaseConfigure           = "Configure"
	PhaseLoadImage           = "LoadImage"
	PhaseWaitForExports      = "WaitForExports"
	PhaseReleased            = "Released"
	PhaseReleaseDisks        = "ReleaseDisks"
	PhaseWaitForReleaseDisks = "WaitForReleaseDisks"
	PhaseAttachDisks         = "AttachDisks"
	PhaseWaitForAttachDisks  = "WaitForAttachDisks"
	PhaseRestartOrchestrator = "RestartOrchestrator"
	PhasePowerOff            = "PowerOff"
	PhaseWaitForPowerOff     = "WaitForPowerOff"
	PhaseDetachDisks         = "DetachDisks"
	PhaseWaitForDetachDisks  = "WaitForDetachDisks"
	PhaseDestroyVM           = "DestroyVM"
	PhaseWaitForDestroyVM    = "WaitForDestroyVM"
	PhaseDeployCompleted     = "DeployCompleted"
	PhaseTeardownCompleted   = "TeardownCompleted"
	PhaseTeardownFailed      = "TeardownFailed"
)

// snapshotVMDKPattern matches VMware snapshot delta suffixes such as -000003.vmdk.
var snapshotVMDKPattern = regexp.MustCompile(`-\d{6}\.vmdk$`)

// NbdURI builds a TCP NBD connection URI for an appliance export.
func NbdURI(host string, port int32, ssl bool) string {
	scheme := "nbd"
	if ssl {
		scheme = "nbds"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, port)
}

// BaseVMDKPath strips a VMware snapshot suffix from a backing file path.
func BaseVMDKPath(path string) string {
	if path == "" {
		return path
	}
	return snapshotVMDKPattern.ReplaceAllString(path, ".vmdk")
}

// ExportNbdConnections maps each attached VMDK path to its NBD connection URI.
func ExportNbdConnections(appliance *api.CopyAppliance, ssl bool) (map[string]string, error) {
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
	attached := appliance.Spec.AttachDisks
	if len(appliance.Status.Exports) < len(attached) {
		return nil, liberr.New(
			"copy appliance exports are incomplete",
			"exports", fmt.Sprintf("%d", len(appliance.Status.Exports)),
			"attached", fmt.Sprintf("%d", len(attached)))
	}

	connections := map[string]string{}
	for _, export := range appliance.Status.Exports {
		if export.VMDKPath == "" {
			return nil, liberr.New("copy appliance export is missing a VMDK path")
		}
		uri := NbdURI(host, export.Port, ssl)
		connections[export.VMDKPath] = uri
		if base := BaseVMDKPath(export.VMDKPath); base != export.VMDKPath {
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
