package copyappliance

import (
	"context"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	libitr "github.com/kubev2v/forklift/pkg/lib/itinerary"
)

// execute runs the step for one phase. done reports whether the step finished;
// a step that has not finished ends the pass and is re-entered on the next
// reconcile.
type execute func(ctx context.Context, phase string) (done bool, err error)

// advance walks the itinerary from the recorded phase as far as one pass can
// get, so that steps with nothing to wait for cost no requeue.
//
// It is the only thing that writes Status.Phase. A step that finishes hands its
// phase to the itinerary and the walk moves on; a step that is still waiting
// leaves the phase where it is, which is how the next pass re-enters the step
// that stopped the last one; a step that errors lands on failed. Terminal
// phases belong to the runner, which parks on them by reporting not done.
func advance(
	ctx context.Context,
	appliance *api.CopyAppliance,
	itinerary *libitr.Itinerary,
	run execute,
	completed string,
	failed string,
) (err error) {
	// Next only ever moves forward, so the walk cannot revisit a step and this
	// bound is unreachable. It is here because the alternative to reaching it
	// is spinning a reconcile worker.
	for hop := 0; hop <= len(itinerary.Pipeline); hop++ {
		phase := appliance.Status.Phase
		var done bool
		done, err = run(ctx, phase)
		if err != nil {
			appliance.Status.Phase = failed
			return
		}
		if !done {
			// Deliberately not written back: a step that redirects itself sets
			// the phase it wants and reports not done.
			return
		}
		next, end, nErr := itinerary.Next(phase)
		if nErr != nil {
			err = nErr
			appliance.Status.Phase = failed
			return
		}
		if end {
			appliance.Status.Phase = completed
			return
		}
		appliance.Status.Phase = next.Name
	}

	err = liberr.New(
		"the itinerary did not end",
		"itinerary", itinerary.Name,
		"phase", appliance.Status.Phase)
	appliance.Status.Phase = failed
	return
}
