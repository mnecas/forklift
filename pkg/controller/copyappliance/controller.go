package copyappliance

import (
	"context"
	"time"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/controller/base"
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/kubev2v/forklift/pkg/lib/logging"
	core "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/storage/names"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// Add the controller to the manager and watch CopyAppliance resources.
func Add(mgr manager.Manager) error {
	reconciler := &Reconciler{
		Reconciler: base.Reconciler{
			Client:        mgr.GetClient(),
			EventRecorder: mgr.GetEventRecorderFor(Name),
			Log:           log,
		},
	}
	cnt, err := controller.New(
		Name,
		mgr,
		controller.Options{
			MaxConcurrentReconciles: Settings.MaxConcurrentReconciles,
			Reconciler:              reconciler,
		})
	if err != nil {
		log.Trace(err)
		return err
	}
	err = cnt.Watch(
		source.Kind(mgr.GetCache(), &api.CopyAppliance{},
			&handler.TypedEnqueueRequestForObject[*api.CopyAppliance]{},
		))
	if err != nil {
		log.Trace(err)
		return err
	}
	return nil
}

var _ reconcile.Reconciler = &Reconciler{}

// Reconciles a CopyAppliance object.
type Reconciler struct {
	base.Reconciler
}

// Reconcile a CopyAppliance CR.
// Note: Must not a pointer receiver to ensure that the
// logger and other state is not shared.
func (r Reconciler) Reconcile(ctx context.Context, request reconcile.Request) (result reconcile.Result, err error) {
	r.Log = logging.WithName(
		names.SimpleNameGenerator.GenerateName(Name+"|"),
		"copy-appliance",
		request)
	r.Started()
	defer func() {
		result.RequeueAfter = r.Ended(
			result.RequeueAfter,
			err)
		err = nil
	}()

	appliance := &api.CopyAppliance{}
	err = r.Get(ctx, request.NamespacedName, appliance)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			r.Log.Info("Resource deleted.")
			err = nil
		}
		return
	}

	defer func() {
		r.Log.V(2).Info("Conditions.", "all", appliance.Status.Conditions)
	}()

	deleting := !appliance.DeletionTimestamp.IsZero()
	if !deleting && appliance.Status.Phase == PhaseDeployCompleted {
		// Nothing left to do. Connecting would cost a vCenter login per watch
		// event for a pass that cannot change anything.
		return
	}

	appliance.Status.BeginStagingConditions()

	switch {
	case deleting:
		err = r.Teardown(ctx, appliance)
	default:
		err = r.AddFinalizer(ctx, appliance)
		if err != nil {
			return
		}
		err = r.Deploy(ctx, appliance)
	}

	appliance.Status.EndStagingConditions()

	// The status is written even when the pass failed. The runner advanced the
	// phase before it returned, and dropping that means repeating the vSphere
	// work the pass did manage to do.
	r.Record(appliance, appliance.Status.Conditions)
	appliance.Status.ObservedGeneration = appliance.Generation
	uErr := r.Status().Update(ctx, appliance)
	if uErr != nil {
		r.Log.Error(uErr, "Failed to update status.")
		if err == nil {
			err = liberr.Wrap(uErr)
		}
	}

	result.RequeueAfter = requeueFor(appliance.Status.Phase)

	// Released after the status update, because releasing it lets the API
	// server delete the object out from under us.
	if deleting {
		fErr := r.RemoveFinalizer(ctx, appliance)
		if fErr != nil {
			r.Log.Error(fErr, "Failed to remove finalizer.")
			if err == nil {
				err = liberr.Wrap(fErr)
			}
		}
	}
	return
}

// requeueFor returns how long to wait before the next pass. Every wait here is
// on vSphere, so it is deliberately slow: each reconcile opens and closes a
// vCenter session, and FastReQ would mean two logins per second per CR.
func requeueFor(phase string) (reQ time.Duration) {
	switch phase {
	case PhaseWaitForClone,
		PhaseWaitForExports,
		PhaseWaitForPowerOff,
		PhaseWaitForDetachDisks,
		PhaseWaitForDestroyVM:
		reQ = base.SlowReQ
	case PhaseDeployFailed, PhaseTeardownFailed:
		// Ended() swallows the error and controller-runtime applies no
		// backoff of its own, so a failed appliance would otherwise retry
		// against vCenter forever at the error cadence.
		reQ = base.LongReQ
	default:
		// An action phase is never observed: ExecutePhase falls through it in
		// the same pass. So this is a completed appliance, or nothing to do
		// at all, and either way we wait for a watch event.
	}
	return
}

// AddFinalizer holds the appliance in the cluster until its VM has been torn
// down.
func (r *Reconciler) AddFinalizer(ctx context.Context, appliance *api.CopyAppliance) (err error) {
	patch := client.MergeFrom(appliance.DeepCopy())
	if controllerutil.AddFinalizer(appliance, api.CopyApplianceFinalizer) {
		err = r.Patch(ctx, appliance, patch)
		if err != nil {
			err = liberr.Wrap(err)
			r.Log.Error(err, "failed to add finalizer", "appliance", appliance.Name, "namespace", appliance.Namespace)
			return
		}
	}
	return
}

// RemoveFinalizer releases the finalizer once the appliance VM is gone. It must
// not be released sooner: an appliance left behind holds read locks on the
// source vmdks with nothing left in the cluster to point at it.
func (r *Reconciler) RemoveFinalizer(ctx context.Context, appliance *api.CopyAppliance) (err error) {
	if appliance.Status.Phase != PhaseTeardownCompleted {
		return
	}
	patch := client.MergeFrom(appliance.DeepCopy())
	if controllerutil.RemoveFinalizer(appliance, api.CopyApplianceFinalizer) {
		err = r.Patch(ctx, appliance, patch)
		if err != nil {
			err = liberr.Wrap(err)
			r.Log.Error(err, "failed to remove finalizer", "appliance", appliance.Name, "namespace", appliance.Namespace)
			return
		}
	}
	return
}

// ApplianceContext resolves the referenced provider and its secret and connects
// to the source provider. The caller owns the returned context and must Close
// it.
func (r *Reconciler) ApplianceContext(ctx context.Context, appliance *api.CopyAppliance) (ac *ApplianceContext, err error) {
	providerKey := types.NamespacedName{
		Namespace: appliance.Spec.Provider.Namespace,
		Name:      appliance.Spec.Provider.Name,
	}
	provider := &api.Provider{}
	err = r.Client.Get(ctx, providerKey, provider)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	secretKey := types.NamespacedName{
		Namespace: provider.Spec.Secret.Namespace,
		Name:      provider.Spec.Secret.Name,
	}
	secret := &core.Secret{}
	err = r.Client.Get(ctx, secretKey, secret)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	ac, err = NewApplianceContext(ctx, appliance, provider, secret, r.Log)
	if err != nil {
		return
	}
	return
}

// Deploy drives the appliance VM one step closer to running and returns. Each
// step that has to wait on vSphere leaves a phase behind and lets the requeue
// bring us back, rather than blocking the worker on a poll loop.
func (r *Reconciler) Deploy(ctx context.Context, appliance *api.CopyAppliance) (err error) {
	applianceContext, err := r.ApplianceContext(ctx, appliance)
	if err != nil {
		r.setFailed(appliance, PhaseDeployFailed, "ConnectFailed", err)
		err = nil
		return
	}
	defer applianceContext.Close()

	r.forgetForeignVM(appliance, applianceContext.InstanceUUID())

	runner := DeployRunner{context: applianceContext}
	if appliance.Status.Phase == "" {
		runner.Begin()
	}
	err = runner.Run(ctx)
	if err != nil {
		r.setFailed(appliance, PhaseDeployFailed, "DeployFailed", err)
		err = nil
		return
	}
	r.setConverging(appliance, "The copy appliance is being deployed.")
	return
}

// Teardown drives the appliance VM one step closer to gone and returns. Each
// step that has to wait on vSphere leaves a phase behind and lets the requeue
// bring us back, rather than blocking the worker on a poll loop.
func (r *Reconciler) Teardown(ctx context.Context, appliance *api.CopyAppliance) (err error) {
	applianceContext, err := r.ApplianceContext(ctx, appliance)
	if err != nil {
		r.setFailed(appliance, PhaseTeardownFailed, "ConnectFailed", err)
		err = nil
		return
	}
	defer applianceContext.Close()

	r.forgetForeignVM(appliance, applianceContext.InstanceUUID())

	runner := TeardownRunner{context: applianceContext}
	switch appliance.Status.Phase {
	case PhasePowerOff, PhaseWaitForPowerOff,
		PhaseDetachDisks, PhaseWaitForDetachDisks,
		PhaseDestroyVM, PhaseWaitForDestroyVM,
		PhaseTeardownCompleted:
		// Already tearing down; resume where the last pass left off.
	default:
		// A deploy phase, an empty phase, or a previous teardown failure.
		// Giving up means an undeletable CR and source vmdks locked forever,
		// so a failed teardown restarts rather than parking.
		runner.Begin()
	}
	err = runner.Run(ctx)
	if err != nil {
		r.setFailed(appliance, PhaseTeardownFailed, "TeardownFailed", err)
		err = nil
		return
	}
	r.setConverging(appliance, "The copy appliance is being torn down.")
	return
}

// forgetForeignVM discards a recorded moRef that was written against a
// different vCenter than the one we are connected to. Acting on it would mean
// powering on — or destroying — an unrelated VM.
func (r *Reconciler) forgetForeignVM(appliance *api.CopyAppliance, instanceUUID string) {
	recorded := appliance.Status.VCenterInstanceUUID
	if appliance.Status.MoRef == "" || recorded == "" || instanceUUID == "" || recorded == instanceUUID {
		return
	}
	r.Log.Info("Recorded appliance VM belongs to a different vCenter; ignoring it.",
		"vm", appliance.Status.MoRef,
		"recorded", recorded,
		"connected", instanceUUID)
	r.forgetVM(appliance)
}

// forgetVM clears every status field that describes a specific VM. They are
// only meaningful together, so they are always cleared together. Clearing the
// phase restarts the itinerary from the beginning on the next pass.
func (r *Reconciler) forgetVM(appliance *api.CopyAppliance) {
	appliance.Status.MoRef = ""
	appliance.Status.VCenterInstanceUUID = ""
	appliance.Status.TaskRef = ""
	appliance.Status.Phase = ""
}

// setFailed records a failed pass as a not-ready condition and a failed phase.
// The phase is passed in because a failure during teardown must not be recorded
// as a deployment failure: they requeue the same way but read very differently.
func (r *Reconciler) setFailed(appliance *api.CopyAppliance, phase, reason string, err error) {
	appliance.Status.Phase = phase
	appliance.Status.SetCondition(libcnd.Condition{
		Type:     libcnd.Ready,
		Status:   libcnd.False,
		Reason:   reason,
		Category: libcnd.Error,
		Message:  err.Error(),
	})
}

// setConverging records that the appliance is still on its way to the phase it
// is headed for. A terminal phase has already set its own condition, and
// staging would otherwise leave the appliance with no Ready condition at all
// between passes. This is not an error, so it is Advisory.
func (r *Reconciler) setConverging(appliance *api.CopyAppliance, message string) {
	phase := appliance.Status.Phase
	switch phase {
	case PhaseDeployCompleted, PhaseDeployFailed,
		PhaseTeardownCompleted, PhaseTeardownFailed:
		return
	}
	appliance.Status.SetCondition(libcnd.Condition{
		Type:     libcnd.Ready,
		Status:   libcnd.False,
		Reason:   phase,
		Category: libcnd.Advisory,
		Message:  message,
	})
}
