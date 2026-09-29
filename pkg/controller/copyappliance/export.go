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
// export itinerary. Terminal phase encodes the last converged target
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

// Begin seeds the export itinerary from the requested target.
func (r *ExportRunner) Begin() (err error) {
	r.context.Appliance.Status.TaskRef = ""
	if r.context.Appliance.Spec.Target == "" {
		return
	}
	itinerary, _, err := r.itinerary()
	if err != nil {
		r.context.Appliance.Status.Phase = PhaseDeployFailed
		return
	}
	step, err := itinerary.First()
	if err != nil {
		return
	}
	r.context.Appliance.Status.Phase = step.Name
	return
}

// Run advances the export pipeline by one pass.
func (r *ExportRunner) Run(ctx context.Context) (err error) {
	err = r.context.CheckInstance()
	if err != nil {
		return
	}

	itinerary, completed, err := r.itinerary()
	if err != nil {
		r.context.Appliance.Status.Phase = PhaseDeployFailed
		r.context.Log.Error(err, "Export phase failed.", "phase", r.context.Appliance.Status.Phase)
		return
	}

	err = advance(
		ctx,
		r.context.Appliance,
		itinerary,
		r.execute,
		completed,
		PhaseDeployFailed)
	if err != nil {
		r.context.Log.Error(err, "Export phase failed.", "phase", r.context.Appliance.Status.Phase)
	}
	return
}

func (r *ExportRunner) itinerary() (itinerary *libitr.Itinerary, completed string, err error) {
	target := r.context.Appliance.Spec.Target
	if target == "" {
		err = liberr.New("export target is not set")
		return
	}
	switch target {
	case api.ExportTargetRelease:
		itinerary = &libitr.Itinerary{
			Name: "Release",
			Pipeline: libitr.Pipeline{
				{Name: PhaseReleaseDisks},
				{Name: PhaseWaitForReleaseDisks},
				{Name: PhaseReleased},
			},
		}
		completed = PhaseReleased
	case api.ExportTargetExport:
		itinerary = &libitr.Itinerary{
			Name: "Export",
			Pipeline: libitr.Pipeline{
				{Name: PhaseAttachDisks},
				{Name: PhaseWaitForAttachDisks},
				{Name: PhaseRestartOrchestrator},
				{Name: PhaseWaitForExports},
				{Name: PhaseDeployCompleted},
			},
		}
		completed = PhaseDeployCompleted
	default:
		err = liberr.New("unknown export target", "target", target)
	}
	return
}

func (r *ExportRunner) execute(ctx context.Context, phase string) (done bool, err error) {
	switch phase {
	case PhaseReleaseDisks:
		detachVM := r.context.VM(r.context.Appliance.Status.MoRef)
		detachTask, detachErr := r.context.DetachAttachedDisks(ctx, detachVM)
		if detachErr != nil {
			err = detachErr
			return
		}
		r.context.SetTask(detachTask)
		done = true
	case PhaseWaitForReleaseDisks:
		done, _, err = r.context.WaitForTask(ctx)
		if err != nil || !done {
			return
		}
		r.context.Appliance.Status.Exports = nil
	case PhaseAttachDisks:
		attachVM := r.context.VM(r.context.Appliance.Status.MoRef)
		attachTask, attachErr := r.context.AttachDisks(ctx, attachVM)
		if attachErr != nil {
			err = attachErr
			return
		}
		r.context.SetTask(attachTask)
		done = true
	case PhaseWaitForAttachDisks:
		done, _, err = r.context.WaitForTask(ctx)
	case PhaseRestartOrchestrator:
		address, ok := applianceAddress(r.context.Appliance.Status.Addresses)
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
			}
			return
		}
		done = true
	case PhaseWaitForExports:
		done, err = r.context.WaitForExports(ctx)
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
