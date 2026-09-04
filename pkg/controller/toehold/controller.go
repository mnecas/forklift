package toehold

import (
	"context"
	"fmt"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/controller/base"
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	"github.com/kubev2v/forklift/pkg/lib/logging"
	"github.com/kubev2v/forklift/pkg/settings"
	batch "k8s.io/api/batch/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/storage/names"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const Name = "toehold"

var log = logging.WithName(Name)
var Settings = &settings.Settings

func Add(mgr manager.Manager) error {
	reconciler := &Reconciler{
		Reconciler: base.Reconciler{
			EventRecorder: mgr.GetEventRecorderFor(Name),
			Client:        mgr.GetClient(),
			Log:           log,
		},
	}
	cnt, err := controller.New(Name, mgr, controller.Options{
		Reconciler:              reconciler,
		MaxConcurrentReconciles: Settings.MaxConcurrentReconciles,
	})
	if err != nil {
		return err
	}
	err = cnt.Watch(
		source.Kind(mgr.GetCache(), &api.Toehold{}, &handler.TypedEnqueueRequestForObject[*api.Toehold]{}, &ToeholdPredicate{}))
	if err != nil {
		return err
	}
	return cnt.Watch(
		source.Kind(mgr.GetCache(), &batch.Job{}, toeholdForJobMapper(), predicate.NewTypedPredicateFuncs(jobOwnedByToehold)))
}

type Reconciler struct {
	base.Reconciler
}

func (r Reconciler) Reconcile(ctx context.Context, request reconcile.Request) (result reconcile.Result, err error) {
	r.Log = logging.WithName(names.SimpleNameGenerator.GenerateName(Name+"|"), "toehold", request)
	r.Started()
	defer func() {
		result.RequeueAfter = r.Ended(result.RequeueAfter, err)
		err = nil
	}()

	toehold := &api.Toehold{}
	err = r.Get(ctx, request.NamespacedName, toehold)
	if err != nil {
		if k8serr.IsNotFound(err) {
			err = nil
		}
		return
	}

	if !toehold.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, toehold)
	}

	if toehold.Status.Phase == api.ToeholdPhaseSucceeded || toehold.Status.Phase == api.ToeholdPhaseFailed {
		return
	}

	if !controllerutil.ContainsFinalizer(toehold, api.ToeholdFinalizer) {
		controllerutil.AddFinalizer(toehold, api.ToeholdFinalizer)
		if err = r.Update(ctx, toehold); err != nil {
			return
		}
	}

	toehold.Status.BeginStagingConditions()
	if err = r.validate(ctx, toehold); err != nil {
		r.fail(toehold, err)
		toehold.Status.EndStagingConditions()
		r.Record(toehold, toehold.Status.Conditions)
		toehold.Status.ObservedGeneration = toehold.Generation
		_ = r.Status().Update(ctx, toehold)
		return
	}

	pipe := &pipeline{ctx: ctx, r: &r, toehold: toehold}
	done, pipeErr := pipe.run()
	if isRequeue(pipeErr) {
		result.RequeueAfter = base.SlowReQ
		pipeErr = nil
	} else if pipeErr != nil {
		r.fail(toehold, pipeErr)
	} else if done {
		toehold.Status.Phase = api.ToeholdPhaseSucceeded
		toehold.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Ready,
			Status:   libcnd.True,
			Category: libcnd.Required,
			Message:  "Toehold is ready.",
		})
	} else {
		result.RequeueAfter = base.SlowReQ
	}

	resolveMessage(toehold)
	toehold.Status.EndStagingConditions()
	r.Record(toehold, toehold.Status.Conditions)
	toehold.Status.ObservedGeneration = toehold.Generation
	err = r.Status().Update(ctx, toehold)
	return
}

func (r Reconciler) fail(toehold *api.Toehold, cause error) {
	toehold.Status.Phase = api.ToeholdPhaseFailed
	now := meta.Now()
	toehold.Status.CompletionTime = &now
	msg := "The toehold has failed."
	if cause != nil {
		msg = fmt.Sprintf("%s %s", msg, cause.Error())
	}
	toehold.Status.Message = msg
	toehold.Status.SetCondition(libcnd.Condition{
		Type:     api.ToeholdFailed,
		Status:   libcnd.True,
		Category: libcnd.Critical,
		Message:  msg,
	})
}

func resolveMessage(toehold *api.Toehold) {
	if toehold.Status.Message != "" {
		return
	}
	switch toehold.Status.Stage {
	case api.StageEnsurePrerequisites:
		toehold.Status.Message = "Ensuring prerequisites."
	case api.StageEnsureTemplate:
		toehold.Status.Message = "Checking template reuse."
	case api.StageBuildAndUpload:
		toehold.Status.Message = "Building and uploading template."
	case api.StageEnsureVM:
		toehold.Status.Message = "Checking VM reuse."
	case api.StageCloneVM:
		toehold.Status.Message = "Cloning toehold VM."
	case api.StageConfigureVM:
		toehold.Status.Message = "Configuring toehold VM."
	case api.StageAttachDisks:
		toehold.Status.Message = "Attaching migration disks."
	case api.StageStartNBD:
		toehold.Status.Message = "Starting NBD exports."
	case api.StageVerifyNBD:
		toehold.Status.Message = "Verifying NBD exports."
	case api.StageToeholdFinished:
		toehold.Status.Message = "Toehold is ready."
	default:
		toehold.Status.Message = "Reconciling toehold."
	}
}

func (r Reconciler) finalize(ctx context.Context, toehold *api.Toehold) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(toehold, api.ToeholdFinalizer) {
		return reconcile.Result{}, nil
	}
	pctx, err := r.providerContext(ctx, toehold)
	if err == nil {
		defer pctx.Client.Close(ctx)
		pipe := &pipeline{ctx: ctx, r: &r, toehold: toehold, pctx: pctx}
		pipe.stopExports()
		_ = pipe.detachDisks()
		if ref, findErr := pctx.Client.FindVM(ctx, toehold.Spec.Folder, toehold.Spec.VMName); findErr == nil {
			_ = pctx.Client.Destroy(ctx, ref.VM)
		}
		if !toehold.Spec.RetainTemplateEnabled() {
			if ref, findErr := pctx.Client.FindTemplate(ctx, toehold.Spec.Folder, toehold.Spec.TemplateName); findErr == nil {
				_ = pctx.Client.Destroy(ctx, ref.VM)
			}
		}
	}
	_ = r.deleteJob(ctx, toehold)
	controllerutil.RemoveFinalizer(toehold, api.ToeholdFinalizer)
	if err := r.Update(ctx, toehold); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}
