package copyappliance

import (
	"context"

	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	libitr "github.com/kubev2v/forklift/pkg/lib/itinerary"
	libvsphere "github.com/kubev2v/forklift/pkg/lib/vsphere"
)

// TeardownRunner drives the appliance VM from running to gone. It holds no
// state of its own: every pass reads where it got to from the appliance status
// and leaves the next phase behind.
type TeardownRunner struct {
	context *ApplianceContext
}

// Begin seeds the teardown itinerary. An appliance with no recorded VM has
// nothing to tear down and is already complete.
func (r *TeardownRunner) Begin() (err error) {
	r.context.Appliance.Status.TaskRef = ""
	if r.context.Appliance.Status.MoRef == "" {
		r.context.Appliance.Status.Phase = PhaseTeardownCompleted
		return
	}
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
		vm := r.context.VM(r.context.Appliance.Status.MoRef)
		task, powerErr := libvsphere.PowerOff(ctx, vm)
		if powerErr != nil {
			err = powerErr
			return
		}
		r.context.SetTask(task)
		done = true
	case PhaseWaitForPowerOff:
		done, _, err = r.context.WaitForTask(ctx)
	case PhaseDetachDisks:
		vm := r.context.VM(r.context.Appliance.Status.MoRef)
		task, detachErr := r.context.DetachDisks(ctx, vm)
		if detachErr != nil {
			err = detachErr
			return
		}
		r.context.SetTask(task)
		done = true
	case PhaseWaitForDetachDisks:
		done, _, err = r.context.WaitForTask(ctx)
	case PhaseDestroyVM:
		vm := r.context.VM(r.context.Appliance.Status.MoRef)
		task, destroyErr := libvsphere.DestroyVM(ctx, vm)
		if destroyErr != nil {
			err = destroyErr
			return
		}
		r.context.SetTask(task)
		done = true
	case PhaseWaitForDestroyVM:
		done, _, err = r.context.WaitForTask(ctx)
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

// Itinerary is the ordered pipeline of teardown phases. PhaseTeardownFailed is
// not in the pipeline: a failure is not a step the walk arrives at, it is where
// the walk ends when a step returns an error. Callers with no MoRef should set
// PhaseTeardownCompleted in Begin rather than walking this pipeline.
func (r *TeardownRunner) Itinerary() *libitr.Itinerary {
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
