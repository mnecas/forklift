package copyappliance

import (
	"context"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/kubev2v/forklift/pkg/nbd-container/announce"
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
func (r *ExportRunner) Begin() {
	r.context.Appliance.Status.TaskRef = ""
	req := r.context.Appliance.Spec.ExportRequest
	if req == nil {
		return
	}
	switch req.Target {
	case api.ExportTargetRelease:
		r.context.Appliance.Status.Phase = PhaseReleaseDisks
	case api.ExportTargetExport:
		r.context.Appliance.Status.Phase = PhaseAttachDisks
	default:
		r.context.Appliance.Status.Phase = PhaseDeployFailed
	}
}

// Run advances the export pipeline by one pass.
func (r *ExportRunner) Run(ctx context.Context) (err error) {
	err = r.context.CheckInstance()
	if err != nil {
		return
	}

	next, err := r.ExecutePhase(ctx)
	if err != nil {
		r.context.Log.Error(err, "Export phase failed.", "phase", r.context.Appliance.Status.Phase)
	}
	r.context.Appliance.Status.Phase = next
	return
}

// ExecutePhase runs the current export phase and returns the phase to record.
func (r *ExportRunner) ExecutePhase(ctx context.Context) (next string, err error) {
	req := r.context.Appliance.Spec.ExportRequest
	if req == nil {
		err = liberr.New("export request is not set")
		next = PhaseDeployFailed
		return
	}

	switch req.Target {
	case api.ExportTargetRelease:
		return r.executeRelease(ctx)
	case api.ExportTargetExport:
		return r.executeExport(ctx)
	default:
		err = liberr.New("unknown export target", "target", req.Target)
		next = PhaseDeployFailed
		return
	}
}

func (r *ExportRunner) executeRelease(ctx context.Context) (next string, err error) {
	switch r.context.Appliance.Status.Phase {
	case PhaseReleaseDisks:
		err = r.detachAttached(ctx)
		if err != nil {
			next = PhaseDeployFailed
			break
		}
		next = PhaseWaitForReleaseDisks
		fallthrough
	case PhaseWaitForReleaseDisks:
		var done bool
		done, err = r.waitForDetach(ctx)
		if err != nil {
			next = PhaseDeployFailed
			break
		}
		if !done {
			next = PhaseWaitForReleaseDisks
			return
		}
		r.context.Appliance.Status.Exports = nil
		observeExportRequest(r.context.Appliance)
		r.context.Appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Ready,
			Status:   libcnd.True,
			Category: libcnd.Required,
			Message:  "Copy appliance disks have been released.",
		})
		next = PhaseReleased
	default:
		if r.context.Appliance.Status.Phase == PhaseReleased && ExportRequestObserved(r.context.Appliance) {
			next = PhaseReleased
			return
		}
		err = liberr.New("unexpected phase for release", "phase", r.context.Appliance.Status.Phase)
		next = PhaseDeployFailed
	}
	return
}

func (r *ExportRunner) executeExport(ctx context.Context) (next string, err error) {
	switch r.context.Appliance.Status.Phase {
	case PhaseAttachDisks:
		err = r.attach(ctx)
		if err != nil {
			next = PhaseDeployFailed
			break
		}
		next = PhaseWaitForAttachDisks
		fallthrough
	case PhaseWaitForAttachDisks:
		var done bool
		done, err = r.waitForAttach(ctx)
		if err != nil {
			next = PhaseDeployFailed
			break
		}
		if !done {
			next = PhaseWaitForAttachDisks
			return
		}
		next = PhaseRestartOrchestrator
		fallthrough
	case PhaseRestartOrchestrator:
		var done bool
		done, err = r.restartOrchestrator(ctx)
		if err != nil {
			next = PhaseDeployFailed
			break
		}
		if !done {
			next = PhaseRestartOrchestrator
			return
		}
		next = PhaseWaitForExports
		fallthrough
	case PhaseWaitForExports:
		var done bool
		done, err = r.context.WaitForExports(ctx)
		if err != nil {
			next = PhaseDeployFailed
			break
		}
		if !done {
			next = PhaseWaitForExports
			return
		}
		observeExportRequest(r.context.Appliance)
		r.context.Appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Ready,
			Status:   libcnd.True,
			Category: libcnd.Required,
			Message:  "Copy appliance disk export has succeeded.",
		})
		next = PhaseDeployCompleted
	default:
		err = liberr.New("unexpected phase for export", "phase", r.context.Appliance.Status.Phase)
		next = PhaseDeployFailed
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

	client, answered, err := r.context.SSHLoginFor(ctx, address, sshTransferTimeout)
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

func observeExportRequest(appliance *api.CopyAppliance) {
	if appliance.Spec.ExportRequest != nil {
		appliance.Status.ObservedExportRequest = appliance.Spec.ExportRequest.DeepCopy()
	}
}

// WaitForExports reports whether the appliance has published the disk exports
// the migration reads from, and records them.
func (r *ApplianceContext) WaitForExports(ctx context.Context) (done bool, err error) {
	address, ok := applianceAddress(r.Appliance.Status.Addresses)
	if !ok {
		err = liberr.New(
			"the appliance reports no address to reach it on",
			"appliance", r.Appliance.Name)
		return
	}
	ca, certificate, key, err := r.ClientTLS()
	if err != nil {
		return
	}
	client, err := announce.NewClient(ca, certificate, key)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}

	exports, err := client.Disks(ctx, r.announceAddr(address))
	if err != nil {
		if exportsNotReady(err) {
			r.Log.Info("The appliance is not announcing its exports yet.",
				"address", address)
			err = nil
			return
		}
		err = liberr.Wrap(err, "address", address)
		return
	}

	attached := r.Appliance.Spec.AttachedDisks()
	matched, matchErr := matchExports(attached, exports)
	if matchErr == errExportsIncomplete {
		r.Log.Info("The appliance has not exported every disk yet.",
			"address", address,
			"exported", len(exports),
			"attached", len(attached))
		return
	}
	if matchErr != nil {
		err = liberr.Wrap(matchErr, "address", address)
		return
	}
	r.Appliance.Status.Exports = matched
	r.Log.Info("The appliance is exporting its disks.",
		"address", address, "exports", len(matched))
	done = true
	return
}
