package copyappliance

import (
	"context"
	"slices"
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	libitr "github.com/kubev2v/forklift/pkg/lib/itinerary"
)

// The phases of the table advance is driven over below. They are not real
// phases: what is under test is the walk, not any of the steps.
const (
	stepFirst  = "TestFirst"
	stepSecond = "TestSecond"
	stepDone   = "TestCompleted"
	stepFailed = "TestFailed"
)

// walkItinerary is the shape every pipeline in this package has: ordered steps
// ending in the completed phase, with the failure phase in no table at all.
func walkItinerary() *libitr.Itinerary {
	return &libitr.Itinerary{
		Name: "Walk",
		Pipeline: libitr.Pipeline{
			{Name: stepFirst},
			{Name: stepSecond},
			{Name: stepDone},
		},
	}
}

// phaseNames is the pipeline a runner would actually walk, in order.
func phaseNames(t *testing.T, itinerary *libitr.Itinerary) (names []string) {
	t.Helper()
	steps, err := itinerary.List()
	if err != nil {
		t.Fatalf("list %s: %v", itinerary.Name, err)
	}
	for _, step := range steps {
		names = append(names, step.Name)
	}
	return
}

// advance is the only thing in the package that writes Status.Phase, so every
// way a pass can end is pinned here rather than once per runner.
func TestAdvance(t *testing.T) {
	// finishing runs every step to completion, recording the ones it was asked
	// for. A test overrides one phase to stop, fail or redirect the walk.
	finishing := func(ran *[]string, step func(phase string) (bool, error)) execute {
		return func(_ context.Context, phase string) (done bool, err error) {
			*ran = append(*ran, phase)
			if step != nil {
				return step(phase)
			}
			return true, nil
		}
	}

	t.Run("a step that finishes hands the walk to the next one", func(t *testing.T) {
		appliance := &api.CopyAppliance{}
		appliance.Status.Phase = stepFirst
		var ran []string

		err := advance(context.TODO(), appliance, walkItinerary(),
			finishing(&ran, func(phase string) (bool, error) {
				return phase != stepSecond, nil
			}),
			stepDone, stepFailed)

		if err != nil {
			t.Fatalf("advance: %v", err)
		}
		if appliance.Status.Phase != stepSecond {
			t.Errorf("phase = %q, want %q", appliance.Status.Phase, stepSecond)
		}
		if want := []string{stepFirst, stepSecond}; !slices.Equal(ran, want) {
			t.Errorf("ran %v, want %v", ran, want)
		}
	})

	// This is the property teardown got wrong: the phase left behind is the one
	// whose step stopped the pass, not the one the pass started on. Anything
	// else re-runs a step that has already been done.
	t.Run("a step that has not finished leaves the phase where it is", func(t *testing.T) {
		appliance := &api.CopyAppliance{}
		appliance.Status.Phase = stepSecond
		var ran []string

		err := advance(context.TODO(), appliance, walkItinerary(),
			finishing(&ran, func(string) (bool, error) { return false, nil }),
			stepDone, stepFailed)

		if err != nil {
			t.Fatalf("advance: %v", err)
		}
		if appliance.Status.Phase != stepSecond {
			t.Errorf("phase = %q, want %q", appliance.Status.Phase, stepSecond)
		}
		if want := []string{stepSecond}; !slices.Equal(ran, want) {
			t.Errorf("ran %v, want %v", ran, want)
		}
	})

	t.Run("a walk that runs off the end lands on the completed phase", func(t *testing.T) {
		appliance := &api.CopyAppliance{}
		appliance.Status.Phase = stepFirst
		var ran []string

		err := advance(context.TODO(), appliance, walkItinerary(),
			finishing(&ran, nil), stepDone, stepFailed)

		if err != nil {
			t.Fatalf("advance: %v", err)
		}
		if appliance.Status.Phase != stepDone {
			t.Errorf("phase = %q, want %q", appliance.Status.Phase, stepDone)
		}
		if want := []string{stepFirst, stepSecond, stepDone}; !slices.Equal(ran, want) {
			t.Errorf("ran %v, want %v", ran, want)
		}
	})

	// The error is returned as well as recorded: the reconciler logs it and
	// sets the condition that says why the appliance stopped.
	t.Run("a step that errors lands on the failed phase", func(t *testing.T) {
		appliance := &api.CopyAppliance{}
		appliance.Status.Phase = stepFirst
		var ran []string

		err := advance(context.TODO(), appliance, walkItinerary(),
			finishing(&ran, func(phase string) (bool, error) {
				if phase == stepSecond {
					return false, liberr.New("the step failed")
				}
				return true, nil
			}),
			stepDone, stepFailed)

		if err == nil {
			t.Fatal("advance reported success for a step that failed")
		}
		if appliance.Status.Phase != stepFailed {
			t.Errorf("phase = %q, want %q", appliance.Status.Phase, stepFailed)
		}
		if want := []string{stepFirst, stepSecond}; !slices.Equal(ran, want) {
			t.Errorf("ran %v, want %v", ran, want)
		}
	})

	// Nothing in an itinerary walks backwards, so a step that has to send an
	// appliance back to an earlier one writes the phase itself and reports that
	// it did not finish. Deploy's shim for appliances an older controller
	// parked before LoadImage is the only one that does this.
	t.Run("a step that redirects itself is honoured", func(t *testing.T) {
		appliance := &api.CopyAppliance{}
		appliance.Status.Phase = stepSecond
		var ran []string

		err := advance(context.TODO(), appliance, walkItinerary(),
			finishing(&ran, func(string) (bool, error) {
				appliance.Status.Phase = stepFirst
				return false, nil
			}),
			stepDone, stepFailed)

		if err != nil {
			t.Fatalf("advance: %v", err)
		}
		if appliance.Status.Phase != stepFirst {
			t.Errorf("phase = %q, want %q", appliance.Status.Phase, stepFirst)
		}
	})

	// A predicate is evaluated fresh every pass, so a step can stop applying
	// while the walk is sitting on it — clearing a teardown's moRef does this
	// to every step ahead of it. The step still runs, and then the walk ends.
	t.Run("a step the predicate has dropped ends the walk", func(t *testing.T) {
		appliance := &api.CopyAppliance{}
		appliance.Status.Phase = stepSecond
		itinerary := walkItinerary()
		itinerary.Pipeline[1].All = 0x01
		itinerary.Predicate = &droppingPredicate{}
		var ran []string

		err := advance(context.TODO(), appliance, itinerary,
			finishing(&ran, nil), stepDone, stepFailed)

		if err != nil {
			t.Fatalf("advance: %v", err)
		}
		if appliance.Status.Phase != stepDone {
			t.Errorf("phase = %q, want %q", appliance.Status.Phase, stepDone)
		}
		if want := []string{stepSecond}; !slices.Equal(ran, want) {
			t.Errorf("ran %v, want %v", ran, want)
		}
	})

	// A phase from a pipeline the appliance is no longer on — an export phase
	// recorded before the target changed, say. Each runner's own default case
	// catches this first; the walk must not silently stop if one does not.
	t.Run("a phase that is not in the pipeline fails the walk", func(t *testing.T) {
		appliance := &api.CopyAppliance{}
		appliance.Status.Phase = "TestElsewhere"

		var ran []string
		err := advance(context.TODO(), appliance, walkItinerary(),
			finishing(&ran, nil), stepDone, stepFailed)

		if err == nil {
			t.Fatal("advance walked on from a phase that is not in the pipeline")
		}
		if appliance.Status.Phase != stepFailed {
			t.Errorf("phase = %q, want %q", appliance.Status.Phase, stepFailed)
		}
	})

	// Next resolves a name to the first step that carries it, so a pipeline
	// that repeats one is a cycle. The reconcile worker running the walk is not
	// cancellable, so the walk has to be the thing that gives up.
	t.Run("a pipeline that repeats a step is not walked forever", func(t *testing.T) {
		appliance := &api.CopyAppliance{}
		appliance.Status.Phase = stepFirst
		itinerary := &libitr.Itinerary{
			Name: "Loop",
			Pipeline: libitr.Pipeline{
				{Name: stepFirst},
				{Name: stepSecond},
				{Name: stepFirst},
			},
		}
		var ran []string

		err := advance(context.TODO(), appliance, itinerary,
			finishing(&ran, nil), stepDone, stepFailed)

		if err == nil {
			t.Fatal("advance walked a cycle to completion")
		}
		if appliance.Status.Phase != stepFailed {
			t.Errorf("phase = %q, want %q", appliance.Status.Phase, stepFailed)
		}
		if len(ran) > len(itinerary.Pipeline)+1 {
			t.Errorf("ran %d steps, want the walk bounded by the %d in the pipeline",
				len(ran), len(itinerary.Pipeline))
		}
	})
}

// droppingPredicate excludes every step that carries a flag.
type droppingPredicate struct{}

func (p *droppingPredicate) Count() int { return 1 }

func (p *droppingPredicate) Evaluate(libitr.Flag) (bool, error) { return false, nil }
