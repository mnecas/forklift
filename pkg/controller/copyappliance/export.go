package copyappliance

import (
	"context"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
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

// Begin seeds the export pipeline from the requested target.
func (r *ExportRunner) Begin() (err error) {
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

// Run runs the current export phase once. A finished step sets Status.Phase
// to its successor; the next reconcile picks it up.
func (r *ExportRunner) Run(ctx context.Context) (err error) {
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
	err = r.execute(ctx)
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

func (r *ExportRunner) execute(ctx context.Context) (err error) {
	phase := r.context.Appliance.Status.Phase
	switch phase {
	case PhaseReleaseDisks:
		detachVM := r.context.VM(r.context.Appliance.Status.MoRef)
		detachTask, detachErr := r.context.DetachAttachedDisks(ctx, detachVM)
		if detachErr != nil {
			return detachErr
		}
		r.context.SetTask(detachTask)
		r.NextPhase()
	case PhaseWaitForReleaseDisks:
		done, _, waitErr := r.context.WaitForTask(ctx)
		if waitErr != nil {
			return waitErr
		}
		if !done {
			return
		}
		r.context.Appliance.Status.Exports = nil
		r.NextPhase()
	case PhaseAttachDisks:
		attachVM := r.context.VM(r.context.Appliance.Status.MoRef)
		attachTask, attachErr := r.context.AttachDisks(ctx, attachVM)
		if attachErr != nil {
			return attachErr
		}
		r.context.SetTask(attachTask)
		r.NextPhase()
	case PhaseWaitForAttachDisks:
		done, _, waitErr := r.context.WaitForTask(ctx)
		if waitErr != nil {
			return waitErr
		}
		if done {
			r.NextPhase()
		}
	case PhaseRestartOrchestrator:
		address, ok := applianceAddress(r.context.Appliance.Status.Addresses)
		if !ok {
			return liberr.New(
				"the appliance reports no address to reach it on",
				"appliance", r.context.Appliance.Name)
		}
		orch, ready, loginErr := NewOrchestrator(ctx, r.context, SSHFileTransferTimeout)
		if loginErr != nil {
			return loginErr
		}
		if !ready {
			r.context.Log.Info("The appliance is not answering on SSH yet.", "address", address)
			return
		}
		defer func() {
			_ = orch.Close()
		}()
		err = orch.systemctl("restart")
		if err != nil {
			if !IsExitError(err) {
				r.context.Log.Info(
					"Lost the connection to the appliance while restarting the supervisor.",
					"address", address,
					"error", err.Error())
				err = nil
			}
			return
		}
		r.NextPhase()
	case PhaseWaitForExports:
		done, waitErr := r.context.WaitForExports(ctx)
		if waitErr != nil {
			return waitErr
		}
		if done {
			r.NextPhase()
		}
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
