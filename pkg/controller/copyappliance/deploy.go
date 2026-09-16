package copyappliance

import (
	"context"

	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	libitr "github.com/kubev2v/forklift/pkg/lib/itinerary"
	"github.com/vmware/govmomi/vim25/types"
)

// DeployRunner drives the appliance VM from nothing to running. It holds no
// state of its own: every pass reads where it got to from the appliance status
// and leaves the next phase behind.
type DeployRunner struct {
	context *ApplianceContext
}

// Begin seeds the deploy itinerary and records the vCenter the appliance VM
// will belong to.
func (r *DeployRunner) Begin() {
	r.context.Appliance.Status.TaskRef = ""
	r.context.Appliance.Status.Phase = PhaseCloneVM
	r.context.Appliance.Status.VCenterInstanceUUID = r.context.InstanceUUID()
}

// Run advances the deployment by one pass.
func (r *DeployRunner) Run(ctx context.Context) (err error) {
	err = r.context.CheckInstance()
	if err != nil {
		return
	}

	next, err := r.ExecutePhase(ctx)
	if err != nil {
		r.context.Log.Error(err, "Deploy phase failed.", "phase", r.context.Appliance.Status.Phase)
	}
	r.context.Appliance.Status.Phase = next
	return
}

// ExecutePhase runs the current phase and returns the phase to record. Steps
// that need no wait fall through to the next in the same pass; a step waiting
// on vSphere returns its own phase and picks up again on the next reconcile.
func (r *DeployRunner) ExecutePhase(ctx context.Context) (next string, err error) {
	phase := r.context.Appliance.Status.Phase
	switch phase {
	case PhaseCloneVM:
		err = r.CloneVM(ctx)
		if err != nil {
			next = PhaseDeployFailed
			break
		}
		next = PhaseWaitForClone
		fallthrough
	case PhaseWaitForClone:
		var done bool
		done, err = r.WaitForClone(ctx)
		if err != nil {
			next = PhaseDeployFailed
			break
		}
		if !done {
			next = phase
			return
		}
		next = PhaseWaitForExports
		fallthrough
	case PhaseWaitForExports:
		var done bool
		done, err = r.WaitForExports(ctx)
		if err != nil {
			next = PhaseDeployFailed
			break
		}
		if !done {
			next = phase
			return
		}
		next = PhaseDeployCompleted
		fallthrough
	case PhaseDeployCompleted:
		r.context.Appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Ready,
			Status:   libcnd.True,
			Category: libcnd.Required,
			Message:  "Deploying the copy appliance has succeeded.",
		})
		next = PhaseDeployCompleted
	case PhaseDeployFailed:
		r.context.Appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Ready,
			Status:   libcnd.False,
			Category: libcnd.Critical,
			Message:  "Deploying the copy appliance has failed.",
		})
		next = PhaseDeployFailed
	default:
		err = liberr.New("unknown phase", "phase", phase)
		next = PhaseDeployFailed
	}
	return
}

// CloneVM clones the template into the appliance VM.
func (r *DeployRunner) CloneVM(ctx context.Context) (err error) {
	task, err := r.context.CloneVM(ctx)
	if err != nil {
		return
	}
	r.context.SetTask(task)
	return
}

// WaitForClone reports whether the appliance VM has finished cloning, and
// records the moRef the clone task named it with.
func (r *DeployRunner) WaitForClone(ctx context.Context) (done bool, err error) {
	done, result, err := r.context.WaitForTask(ctx)
	if err != nil {
		return
	}
	if !done {
		return
	}
	moRef, ok := result.(types.ManagedObjectReference)
	if !ok {
		err = liberr.New("task result is not a ManagedObjectRef", "result", result)
		return
	}
	r.context.Appliance.Status.MoRef = moRef.Reference().Value
	return
}

// WaitForExports reports whether the appliance has published the disk exports
// the migration will read from.
func (r *DeployRunner) WaitForExports(ctx context.Context) (done bool, err error) {
	// NO-OP: the appliance does not report its exports yet.
	done = true
	return
}

// Itinerary is the ordered pipeline of deploy phases.
func (r *DeployRunner) Itinerary() *libitr.Itinerary {
	return &libitr.Itinerary{
		Name: "Deploy",
		Pipeline: libitr.Pipeline{
			{Name: PhaseCloneVM},
			{Name: PhaseWaitForClone},
			{Name: PhaseWaitForExports},
			{Name: PhaseDeployCompleted},
			{Name: PhaseDeployFailed},
		},
	}
}
