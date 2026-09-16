package copyappliance

import (
	"context"

	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	libitr "github.com/kubev2v/forklift/pkg/lib/itinerary"
)

// TeardownRunner drives the appliance VM from running to gone. It holds no
// state of its own: every pass reads where it got to from the appliance status
// and leaves the next phase behind.
type TeardownRunner struct {
	context *ApplianceContext
}

// Begin seeds the teardown itinerary. An appliance with no recorded VM has
// nothing to tear down: there is no moRef to destroy, so teardown is already
// complete.
func (r *TeardownRunner) Begin() {
	r.context.Appliance.Status.TaskRef = ""
	if r.context.Appliance.Status.MoRef == "" {
		r.context.Appliance.Status.Phase = PhaseTeardownCompleted
		return
	}
	r.context.Appliance.Status.Phase = PhasePowerOff
}

// Run advances the teardown by one pass.
func (r *TeardownRunner) Run(ctx context.Context) (err error) {
	err = r.context.CheckInstance()
	if err != nil {
		return
	}

	next, err := r.ExecutePhase(ctx)
	if err != nil {
		r.context.Log.Error(err, "Teardown phase failed.", "phase", r.context.Appliance.Status.Phase)
	}
	r.context.Appliance.Status.Phase = next
	return
}

// ExecutePhase runs the current phase and returns the phase to record. Steps
// that need no wait fall through to the next in the same pass; a step waiting
// on vSphere returns its own phase and picks up again on the next reconcile.
func (r *TeardownRunner) ExecutePhase(ctx context.Context) (next string, err error) {
	phase := r.context.Appliance.Status.Phase
	switch phase {
	case PhasePowerOff:
		err = r.PowerOff(ctx)
		if err != nil {
			next = PhaseTeardownFailed
			break
		}
		next = PhaseWaitForPowerOff
		fallthrough
	case PhaseWaitForPowerOff:
		var done bool
		done, err = r.WaitForPowerOff(ctx)
		if err != nil {
			next = PhaseTeardownFailed
			break
		}
		if !done {
			next = phase
			return
		}
		next = PhaseDetachDisks
		fallthrough
	case PhaseDetachDisks:
		err = r.DetachDisks(ctx)
		if err != nil {
			next = PhaseTeardownFailed
			break
		}
		next = PhaseWaitForDetachDisks
		fallthrough
	case PhaseWaitForDetachDisks:
		var done bool
		done, err = r.WaitForDetachDisks(ctx)
		if err != nil {
			next = PhaseTeardownFailed
			break
		}
		if !done {
			next = phase
			return
		}
		next = PhaseDestroyVM
		fallthrough
	case PhaseDestroyVM:
		err = r.DestroyVM(ctx)
		if err != nil {
			next = PhaseTeardownFailed
			break
		}
		next = PhaseWaitForDestroyVM
		fallthrough
	case PhaseWaitForDestroyVM:
		var done bool
		done, err = r.WaitForDestroyVM(ctx)
		if err != nil {
			next = PhaseTeardownFailed
			break
		}
		if !done {
			next = phase
			return
		}
		next = PhaseTeardownCompleted
		fallthrough
	case PhaseTeardownCompleted:
		// The VM is gone, so the moRef names nothing and the addresses reach
		// nothing. Clearing them makes a repeated teardown a no-op rather than
		// a second destroy attempt.
		if r.context.Appliance.Status.MoRef != "" {
			r.context.Log.Info("Deleted appliance VM.", "vm", r.context.Appliance.Status.MoRef)
			r.context.Appliance.Status.MoRef = ""
		}
		r.context.Appliance.Status.Addresses = nil
		r.context.Appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Ready,
			Status:   libcnd.True,
			Category: libcnd.Required,
			Message:  "Tearing down the copy appliance has succeeded.",
		})
		next = PhaseTeardownCompleted
	case PhaseTeardownFailed:
		r.context.Appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Ready,
			Status:   libcnd.False,
			Category: libcnd.Critical,
			Message:  "Tearing down the copy appliance has failed.",
		})
		next = PhaseTeardownFailed
	default:
		err = liberr.New("unknown phase", "phase", phase)
		next = PhaseTeardownFailed
	}
	return
}

// PowerOff asks the appliance VM to power off.
func (r *TeardownRunner) PowerOff(ctx context.Context) (err error) {
	vm := r.context.VM(r.context.Appliance.Status.MoRef)
	task, err := r.context.PowerOff(ctx, vm)
	if err != nil {
		return
	}
	r.context.SetTask(task)
	return
}

// WaitForPowerOff reports whether the appliance VM has finished powering off.
func (r *TeardownRunner) WaitForPowerOff(ctx context.Context) (done bool, err error) {
	done, _, err = r.context.WaitForTask(ctx)
	return
}

// DetachDisks removes the source vmdks from the appliance VM. The files
// themselves belong to other VMs and are left where they are.
func (r *TeardownRunner) DetachDisks(ctx context.Context) (err error) {
	vm := r.context.VM(r.context.Appliance.Status.MoRef)
	task, err := r.context.DetachDisks(ctx, vm)
	if err != nil {
		return
	}
	r.context.SetTask(task)
	return
}

// WaitForDetachDisks reports whether the disks have finished detaching.
func (r *TeardownRunner) WaitForDetachDisks(ctx context.Context) (done bool, err error) {
	done, _, err = r.context.WaitForTask(ctx)
	return
}

// DestroyVM destroys the appliance VM shell.
func (r *TeardownRunner) DestroyVM(ctx context.Context) (err error) {
	vm := r.context.VM(r.context.Appliance.Status.MoRef)
	task, err := r.context.DestroyVM(ctx, vm)
	if err != nil {
		return
	}
	r.context.SetTask(task)
	return
}

// WaitForDestroyVM reports whether the appliance VM has finished being
// destroyed.
func (r *TeardownRunner) WaitForDestroyVM(ctx context.Context) (done bool, err error) {
	done, _, err = r.context.WaitForTask(ctx)
	return
}

// TeardownItinerary is the ordered pipeline of teardown phases.
func TeardownItinerary() *libitr.Itinerary {
	return &libitr.Itinerary{
		Name: "Teardown",
		Pipeline: libitr.Pipeline{
			{Name: PhasePowerOff},
			{Name: PhaseWaitForPowerOff},
			{Name: PhaseDetachDisks},
			{Name: PhaseWaitForDetachDisks},
			{Name: PhaseDestroyVM},
			{Name: PhaseWaitForDestroyVM},
			{Name: PhaseTeardownCompleted},
		},
	}
}
