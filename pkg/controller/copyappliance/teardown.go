package copyappliance

import (
	"context"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
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
// nothing to tear down: the predicate filters out every step that touches one,
// so the first step is the last, and teardown is already complete.
func (r *TeardownRunner) Begin() (err error) {
	r.context.Appliance.Status.TaskRef = ""
	step, err := r.Itinerary().First()
	if err != nil {
		return
	}
	r.context.Appliance.Status.Phase = step.Name
	return
}

// Run advances the teardown by one pass.
func (r *TeardownRunner) Run(ctx context.Context) (err error) {
	err = r.context.CheckInstance()
	if err != nil {
		return
	}

	err = advance(
		ctx,
		r.context.Appliance,
		r.Itinerary(),
		r.execute,
		PhaseTeardownCompleted,
		PhaseTeardownFailed)
	if err != nil {
		r.context.Log.Error(err, "Teardown phase failed.", "phase", r.context.Appliance.Status.Phase)
	}
	return
}

// execute runs one teardown step and reports whether it finished. A step with
// nothing to wait for finishes, and the walk moves on to the next step within
// the same pass. The terminal phases report not finished, which parks the walk
// on them.
func (r *TeardownRunner) execute(ctx context.Context, phase string) (done bool, err error) {
	switch phase {
	case PhasePowerOff:
		err = r.PowerOff(ctx)
		if err != nil {
			return
		}
		done = true
	case PhaseWaitForPowerOff:
		done, err = r.WaitForPowerOff(ctx)
	case PhaseDetachDisks:
		err = r.DetachDisks(ctx)
		if err != nil {
			return
		}
		done = true
	case PhaseWaitForDetachDisks:
		done, err = r.WaitForDetachDisks(ctx)
	case PhaseDestroyVM:
		err = r.DestroyVM(ctx)
		if err != nil {
			return
		}
		done = true
	case PhaseWaitForDestroyVM:
		done, err = r.WaitForDestroyVM(ctx)
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
	case PhaseTeardownFailed:
		r.context.Appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Ready,
			Status:   libcnd.False,
			Category: libcnd.Critical,
			Message:  "Tearing down the copy appliance has failed.",
		})
	default:
		err = liberr.New("unknown phase", "phase", phase)
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

// flagHasVM marks the steps that only mean something when a VM was recorded.
var flagHasVM libitr.Flag = 0x01

// teardownPredicate decides which steps apply to the appliance being torn down.
type teardownPredicate struct {
	appliance *api.CopyAppliance
}

// Count is the number of flag bit positions, not a mask of them.
func (r *teardownPredicate) Count() int {
	return 1
}

func (r *teardownPredicate) Evaluate(flag libitr.Flag) (pTrue bool, err error) {
	if flag == flagHasVM {
		pTrue = r.appliance.Status.MoRef != ""
	}
	return
}

// Itinerary is the ordered pipeline of teardown phases. Everything before the
// last step needs a VM to act on, so an appliance with no recorded moRef walks
// straight to the end. PhaseTeardownFailed is not in the pipeline: a failure is
// not a step the walk arrives at, it is where the walk ends when a step returns
// an error.
func (r *TeardownRunner) Itinerary() *libitr.Itinerary {
	return &libitr.Itinerary{
		Name:      "Teardown",
		Predicate: &teardownPredicate{appliance: r.context.Appliance},
		Pipeline: libitr.Pipeline{
			{Name: PhasePowerOff, All: flagHasVM},
			{Name: PhaseWaitForPowerOff, All: flagHasVM},
			{Name: PhaseDetachDisks, All: flagHasVM},
			{Name: PhaseWaitForDetachDisks, All: flagHasVM},
			{Name: PhaseDestroyVM, All: flagHasVM},
			{Name: PhaseWaitForDestroyVM, All: flagHasVM},
			{Name: PhaseTeardownCompleted},
		},
	}
}
