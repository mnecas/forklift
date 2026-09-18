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

// NeedsExportConvergence reports whether spec.exportRequest has not yet been applied.
func NeedsExportConvergence(appliance *api.CopyAppliance) bool {
	req := appliance.Spec.ExportRequest
	if req == nil {
		return false
	}
	obs := appliance.Status.ObservedExportRequest
	return obs == nil || req.Generation != obs.Generation || req.Target != obs.Target
}

// ExportRequestObserved reports whether the controller has converged spec.exportRequest.
func ExportRequestObserved(appliance *api.CopyAppliance) bool {
	req := appliance.Spec.ExportRequest
	obs := appliance.Status.ObservedExportRequest
	if req == nil || obs == nil {
		return false
	}
	return req.Generation == obs.Generation && req.Target == obs.Target
}

// IsExportPhase reports whether phase belongs to the export release/reattach
// pipeline. Deploy also uses PhaseWaitForExports; RoutesToExportRunner decides
// which runner should handle the CR.
func IsExportPhase(phase string) bool {
	switch phase {
	case PhaseReleaseDisks, PhaseWaitForReleaseDisks,
		PhaseAttachDisks, PhaseWaitForAttachDisks,
		PhaseRestartOrchestrator:
		return true
	default:
		return false
	}
}

// RoutesToExportRunner reports whether the export runner should reconcile the
// appliance. Initial deploy waits for exports without an exportRequest.
func RoutesToExportRunner(appliance *api.CopyAppliance) bool {
	if IsExportPhase(appliance.Status.Phase) {
		return true
	}
	if appliance.Status.Phase == PhaseWaitForExports &&
		appliance.Spec.ExportRequest != nil {
		return true
	}
	if (appliance.Status.Phase == PhaseDeployCompleted ||
		appliance.Status.Phase == PhaseReleased) &&
		NeedsExportConvergence(appliance) {
		return true
	}
	return false
}

// Begin seeds the export itinerary from the requested target.
func (r *ExportRunner) Begin() (err error) {
	r.context.Appliance.Status.TaskRef = ""
	if r.context.Appliance.Spec.ExportRequest == nil {
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

// itinerary returns the pipeline for the requested target and the phase that
// ends it. The two targets are disjoint routes rather than variations on one,
// so the target picks the table and the phase alone then picks the step.
func (r *ExportRunner) itinerary() (itinerary *libitr.Itinerary, completed string, err error) {
	req := r.context.Appliance.Spec.ExportRequest
	if req == nil {
		err = liberr.New("export request is not set")
		return
	}
	switch req.Target {
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
		err = liberr.New("unknown export target", "target", req.Target)
	}
	return
}

// execute runs one export step and reports whether it finished. A step with
// nothing to wait for finishes, and the walk moves on to the next step within
// the same pass. The terminal phases report not finished, which parks the walk
// on them.
func (r *ExportRunner) execute(ctx context.Context, phase string) (done bool, err error) {
	switch phase {
	case PhaseReleaseDisks:
		err = r.detachAttached(ctx)
		if err != nil {
			return
		}
		done = true
	case PhaseWaitForReleaseDisks:
		done, err = r.waitForDetach(ctx)
		if err != nil || !done {
			return
		}
		r.context.Appliance.Status.Exports = nil
	case PhaseAttachDisks:
		err = r.attach(ctx)
		if err != nil {
			return
		}
		done = true
	case PhaseWaitForAttachDisks:
		done, err = r.waitForAttach(ctx)
	case PhaseRestartOrchestrator:
		done, err = r.restartOrchestrator(ctx)
	case PhaseWaitForExports:
		done, err = r.context.WaitForExports(ctx)
	case PhaseReleased:
		r.context.observeExportRequest()
		r.context.Appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Ready,
			Status:   libcnd.True,
			Category: libcnd.Required,
			Message:  "Copy appliance disks have been released.",
		})
	case PhaseDeployCompleted:
		r.context.observeExportRequest()
		r.context.Appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Ready,
			Status:   libcnd.True,
			Category: libcnd.Required,
			Message:  "Copy appliance disk export has succeeded.",
		})
	default:
		err = liberr.New("unexpected phase for export", "phase", phase)
	}
	return
}

func (r *ExportRunner) detachAttached(ctx context.Context) (err error) {
	vm := r.context.VM(r.context.Appliance.Status.MoRef)
	task, err := r.context.DetachAttachedDisks(ctx, vm)
	if err != nil {
		return
	}
	r.context.SetTask(task)
	return
}

func (r *ExportRunner) waitForDetach(ctx context.Context) (done bool, err error) {
	done, _, err = r.context.WaitForTask(ctx)
	return
}

func (r *ExportRunner) attach(ctx context.Context) (err error) {
	vm := r.context.VM(r.context.Appliance.Status.MoRef)
	task, err := r.context.AttachDisks(ctx, vm)
	if err != nil {
		return
	}
	r.context.SetTask(task)
	return
}

func (r *ExportRunner) waitForAttach(ctx context.Context) (done bool, err error) {
	done, _, err = r.context.WaitForTask(ctx)
	return
}

func (r *ExportRunner) restartOrchestrator(ctx context.Context) (done bool, err error) {
	address, ok := applianceAddress(r.context.Appliance.Status.Addresses)
	if !ok {
		err = liberr.New(
			"the appliance reports no address to reach it on",
			"appliance", r.context.Appliance.Name)
		return
	}

	client, answered, err := r.context.SSHLoginFor(ctx, address, SSHFileTransferTimeout)
	if err != nil {
		return
	}
	if !answered {
		r.context.Log.Info("The appliance is not answering on SSH yet.", "address", address)
		return
	}
	defer func() {
		_ = client.Close()
	}()

	err = r.context.RestartOrchestrator(client)
	if err != nil {
		if !r.context.Alive(client) {
			r.context.Log.Info(
				"Lost the connection to the appliance while restarting the supervisor.",
				"address", address,
				"error", err.Error())
			err = nil
			return
		}
		return
	}
	done = true
	return
}
