// Package copyappliance deploys and tears down the copy appliance VM that a
// migration reads source disks through. The appliance is a clone of a template
// in the source vCenter, and the CopyAppliance CR is its lifecycle.
package copyappliance

import (
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
	PhaseDeployFailed       = "DeployFailed"
	PhaseCloneVM            = "CloneVM"
	PhaseWaitForClone       = "WaitForClone"
	PhaseWaitForNetwork     = "WaitForNetwork"
	PhaseConfigure          = "Configure"
	PhaseLoadImage          = "LoadImage"
	PhaseWaitForExports     = "WaitForExports"
	PhasePowerOff           = "PowerOff"
	PhaseWaitForPowerOff    = "WaitForPowerOff"
	PhaseDetachDisks        = "DetachDisks"
	PhaseWaitForDetachDisks = "WaitForDetachDisks"
	PhaseDestroyVM          = "DestroyVM"
	PhaseWaitForDestroyVM   = "WaitForDestroyVM"
	PhaseDeployCompleted    = "DeployCompleted"
	PhaseTeardownCompleted  = "TeardownCompleted"
	PhaseTeardownFailed     = "TeardownFailed"
)
