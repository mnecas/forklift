package copyappliance

import (
	"context"
	"time"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/controller/base"
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	libitr "github.com/kubev2v/forklift/pkg/lib/itinerary"
)

// ExportRunner drives disk release and re-export on an already-deployed appliance.
type ExportRunner struct {
	context *ApplianceContext
}

// PendingExportRequest reports whether a terminal appliance must re-enter the
// export pipeline. Terminal phase encodes the last converged target
// (DeployCompleted = Export, Released = Release), so only a mismatched
// target is pending. Warm precopy always Release→Export; there is no
// same-target re-export path.
func PendingExportRequest(appliance *api.CopyAppliance) bool {
	target := appliance.Spec.Target
	if target == "" {
		return false
	}
	switch appliance.Status.Phase {
	case PhaseDeployCompleted:
		return target == api.ExportTargetRelease
	case PhaseReleased:
		return target == api.ExportTargetExport
	default:
		return false
	}
}

// begin seeds the export pipeline from the requested target.
func (r *ExportRunner) begin() (err error) {
	r.context.Appliance.Status.TaskRef = ""
	if r.context.Appliance.Spec.Target == "" {
		return
	}
	itinerary, err := r.itinerary()
	if err != nil {
		r.context.Appliance.Status.Phase = r.failedPhase()
		return
	}
	step, err := itinerary.First()
	if err != nil {
		r.context.Appliance.Status.Phase = r.failedPhase()
		return
	}
	r.context.Appliance.Status.Phase = step.Name
	return
}

// Run runs the current export phase once and reports how long to wait before
// the next pass. A finished step sets Status.Phase to its successor; the next
// reconcile picks it up.
func (r *ExportRunner) Run(ctx context.Context) (reQ time.Duration, err error) {
	// Ended() swallows the error and controller-runtime applies no backoff of
	// its own, so a failed pass would otherwise retry against vCenter forever
	// at the error cadence.
	defer func() {
		if err != nil {
			reQ = base.LongReQ
		}
	}()
	// Seed only from a stable phase. WaitForExports is shared with deploy and
	// is not an export phase, so seeding there would restart attach every pass.
	if PendingExportRequest(r.context.Appliance) {
		err = r.begin()
		if err != nil {
			return
		}
	}
	err = r.context.CheckInstance()
	if err != nil {
		return
	}
	if r.context.Appliance.Spec.Target == "" {
		err = liberr.New("export target is not set")
		r.context.Appliance.Status.Phase = r.failedPhase()
		r.context.Log.Error(err, "Export phase failed.", "phase", r.context.Appliance.Status.Phase)
		return
	}
	reQ, err = r.execute(ctx)
	if err != nil {
		r.context.Appliance.Status.Phase = r.failedPhase()
		r.context.Log.Error(err, "Export phase failed.", "phase", r.context.Appliance.Status.Phase)
	}
	return
}

func (r *ExportRunner) itinerary() (*libitr.Itinerary, error) {
	switch r.context.Appliance.Spec.Target {
	case api.ExportTargetRelease:
		return &libitr.Itinerary{
			Name: "Release",
			Pipeline: libitr.Pipeline{
				{Name: PhaseReleaseDisks},
				{Name: PhaseWaitForReleaseDisks},
				{Name: PhaseReleased},
			},
		}, nil
	case api.ExportTargetExport:
		return &libitr.Itinerary{
			Name: "Export",
			Pipeline: libitr.Pipeline{
				{Name: PhaseAttachDisks},
				{Name: PhaseWaitForAttachDisks},
				{Name: PhaseRestartOrchestrator},
				{Name: PhaseWaitForExports},
				{Name: PhaseDeployCompleted},
			},
		}, nil
	default:
		return nil, liberr.New("unknown export target", "target", r.context.Appliance.Spec.Target)
	}
}

func (r *ExportRunner) failedPhase() string {
	return PhaseDeployFailed
}

func (r *ExportRunner) NextPhase() {
	itinerary, err := r.itinerary()
	if err != nil {
		return
	}
	nextPhase(r.context.Appliance, itinerary)
}

// execute runs the phase the appliance is on and reports how long to wait
// before running the next one. A step that advances the phase asks for no wait:
// writing the new phase to the status fires the watch, which brings the next
// pass back at once. A step still waiting names the interval it wants to be
// polled at, and those are deliberately slow because each pass costs a vCenter
// session.
func (r *ExportRunner) execute(ctx context.Context) (reQ time.Duration, err error) {
	phase := r.context.Appliance.Status.Phase
	switch phase {
	case PhaseReleaseDisks:
		detachVM := r.context.VM(r.context.Appliance.Status.MoRef)
		detachTask, detachErr := r.context.DetachAttachedDisks(ctx, detachVM)
		if detachErr != nil {
			err = detachErr
			return
		}
		r.context.SetTask(detachTask)
		r.NextPhase()
	case PhaseWaitForReleaseDisks:
		done, _, waitErr := r.context.WaitForTask(ctx)
		if waitErr != nil {
			err = waitErr
			return
		}
		if !done {
			// A vSphere task, which settles in seconds.
			reQ = base.SlowReQ
			return
		}
		r.context.Appliance.Status.Exports = nil
		r.NextPhase()
		// Ready must be set on this pass: the reconciler idles on Released.
		r.context.Appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Ready,
			Status:   libcnd.True,
			Category: libcnd.Required,
			Message:  "Copy appliance disks have been released.",
		})
	case PhaseAttachDisks:
		attachVM := r.context.VM(r.context.Appliance.Status.MoRef)
		attachTask, attachErr := r.context.AttachDisks(ctx, attachVM)
		if attachErr != nil {
			err = attachErr
			return
		}
		r.context.SetTask(attachTask)
		r.NextPhase()
	case PhaseWaitForAttachDisks:
		done, _, waitErr := r.context.WaitForTask(ctx)
		if waitErr != nil {
			err = waitErr
			return
		}
		if done {
			r.NextPhase()
			return
		}
		// A vSphere task, which settles in seconds.
		reQ = base.SlowReQ
	case PhaseRestartOrchestrator:
		address, ok := r.context.Appliance.Address()
		if !ok {
			err = liberr.New(
				"the appliance reports no address to reach it on",
				"appliance", r.context.Appliance.Name)
			return
		}
		orch, ready, loginErr := NewOrchestrator(ctx, r.context, SSHFileTransferTimeout)
		if loginErr != nil {
			err = loginErr
			return
		}
		if !ready {
			r.context.Log.Info("The appliance is not answering on SSH yet.", "address", address)
			// Waiting on sshd, which is seconds away on an appliance that was
			// answering a moment ago.
			reQ = base.SlowReQ
			return
		}
		defer func() {
			_ = orch.Close()
		}()
		err = orch.Restart()
		if err != nil {
			if !IsExitError(err) {
				r.context.Log.Info(
					"Lost the connection to the appliance while restarting the supervisor.",
					"address", address,
					"error", err.Error())
				err = nil
				reQ = base.SlowReQ
			}
			return
		}
		r.NextPhase()
	case PhaseWaitForExports:
		done, waitErr := r.context.WaitForExports(ctx)
		if waitErr != nil {
			err = waitErr
			return
		}
		if done {
			r.NextPhase()
			// Ready must be set on this pass: the reconciler idles on
			// DeployCompleted, so the case below would never run.
			r.context.Appliance.Status.SetCondition(libcnd.Condition{
				Type:     libcnd.Ready,
				Status:   libcnd.True,
				Category: libcnd.Required,
				Message:  "Copy appliance disk export has succeeded.",
			})
			return
		}
		// Waiting on the guest to enumerate its disks and bring up a container
		// for each, which is tens of seconds. Polling that at the task cadence
		// buys nothing but vCenter logins.
		reQ = base.LongReQ
	case PhaseReleased, PhaseDeployCompleted:
		msg := "Copy appliance disk export has succeeded."
		if phase == PhaseReleased {
			msg = "Copy appliance disks have been released."
		}
		r.context.Appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Ready,
			Status:   libcnd.True,
			Category: libcnd.Required,
			Message:  msg,
		})
	default:
		err = liberr.New("unexpected phase for export", "phase", phase)
	}
	return
}
