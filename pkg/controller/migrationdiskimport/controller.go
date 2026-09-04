package migrationdiskimport

import (
	"context"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/controller/base"
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	"github.com/kubev2v/forklift/pkg/lib/logging"
	"github.com/kubev2v/forklift/pkg/settings"
	core "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/storage/names"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const Name = "migrationdiskimport"

var log = logging.WithName(Name)

var Settings = &settings.Settings

func Add(mgr manager.Manager) error {
	reconciler := &Reconciler{
		Reconciler: base.Reconciler{
			EventRecorder: mgr.GetEventRecorderFor(Name),
			Client:        mgr.GetClient(),
			Log:           log,
		},
		ensurer: Ensurer{Client: mgr.GetClient()},
	}
	cnt, err := controller.New(Name, mgr, controller.Options{
		Reconciler:              reconciler,
		MaxConcurrentReconciles: Settings.MaxConcurrentReconciles,
	})
	if err != nil {
		return err
	}
	if err = cnt.Watch(source.Kind(mgr.GetCache(), &api.MigrationDiskImport{}, &handler.TypedEnqueueRequestForObject[*api.MigrationDiskImport]{})); err != nil {
		return err
	}
	if err = cnt.Watch(source.Kind(mgr.GetCache(), &core.Pod{}, handler.TypedEnqueueRequestsFromMapFunc[*core.Pod](podToMigrationDiskImport))); err != nil {
		return err
	}
	return nil
}

func podToMigrationDiskImport(_ context.Context, pod *core.Pod) []reconcile.Request {
	if pod.Labels == nil || pod.Labels["migrationDiskImport"] == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{
		Namespace: pod.Namespace,
		Name:      pod.Labels["migrationDiskImport"],
	}}}
}

type Reconciler struct {
	base.Reconciler
	ensurer Ensurer
}

func (r Reconciler) Reconcile(ctx context.Context, request reconcile.Request) (result reconcile.Result, err error) {
	r.Log = logging.WithName(names.SimpleNameGenerator.GenerateName(Name+"|"), "migrationDiskImport", request)
	r.Started()
	defer func() {
		result.RequeueAfter = r.Ended(result.RequeueAfter, err)
		err = nil
	}()

	mdi := &api.MigrationDiskImport{}
	err = r.Get(ctx, request.NamespacedName, mdi)
	if err != nil {
		if k8serr.IsNotFound(err) {
			err = nil
		}
		return
	}

	if mdi.Status.Phase == api.MigrationDiskImportSucceeded || mdi.Status.Phase == api.MigrationDiskImportFailed {
		return
	}

	if err = validateSpec(&mdi.Spec); err != nil {
		mdi.Status.Phase = api.MigrationDiskImportFailed
		mdi.Status.SetCondition(libcnd.Condition{
			Type:     api.ConditionFailed,
			Status:   libcnd.True,
			Category: api.CategoryCritical,
			Message:  err.Error(),
		})
		return r.updateStatus(ctx, mdi)
	}

	bound, err := pvcBound(ctx, r.Client, mdi)
	if err != nil {
		return
	}
	if !bound {
		mdi.Status.Phase = api.MigrationDiskImportPending
		mdi.Status.ClaimName = mdi.Spec.Target.Name
		result.RequeueAfter = base.SlowReQ
		return r.updateStatus(ctx, mdi)
	}

	current := mdi.Spec.CurrentCheckpoint()
	if current == "" {
		mdi.Status.Phase = api.MigrationDiskImportPending
		result.RequeueAfter = base.SlowReQ
		return r.updateStatus(ctx, mdi)
	}

	mdi.Status.TransferType = mdi.Spec.Transfer.Type
	mdi.Status.ClaimName = mdi.Spec.Target.Name
	mdi.Status.FinalCheckpoint = mdi.Spec.FinalCheckpoint
	mdi.Status.CurrentCheckpoint = current

	completed := mdi.Status.CompletedCheckpoints
	if completed >= len(mdi.Spec.Checkpoints) {
		if mdi.Spec.FinalCheckpoint {
			mdi.Status.Phase = api.MigrationDiskImportSucceeded
			mdi.Status.Progress = "100.0%"
		} else {
			mdi.Status.Phase = api.MigrationDiskImportPaused
		}
		return r.updateStatus(ctx, mdi)
	}

	active := mdi.Spec.Checkpoints[completed]
	idx := checkpointIndex(&mdi.Spec, active.Current)
	previous := active.Previous
	if previous == "" && idx > 0 {
		previous = previousForCheckpoint(&mdi.Spec, idx)
	}

	pod, err := r.ensurer.EnsureImporterPod(ctx, mdi, active.Current, previous)
	if err != nil {
		return
	}
	mdi.Status.ImporterPodName = pod.Name
	mdi.Status.Progress = podProgress(pod)

	switch pod.Status.Phase {
	case core.PodSucceeded:
		mdi.Status.CompletedCheckpoints = completed + 1
		if mdi.Spec.FinalCheckpoint && mdi.Status.CompletedCheckpoints >= len(mdi.Spec.Checkpoints) {
			mdi.Status.Phase = api.MigrationDiskImportSucceeded
			mdi.Status.Progress = "100.0%"
		} else {
			mdi.Status.Phase = api.MigrationDiskImportPaused
		}
	case core.PodFailed:
		mdi.Status.Phase = api.MigrationDiskImportFailed
		mdi.Status.SetCondition(libcnd.Condition{
			Type:     api.ConditionFailed,
			Status:   libcnd.True,
			Category: api.CategoryCritical,
			Message:  "Importer pod failed",
		})
	case core.PodRunning:
		mdi.Status.Phase = api.MigrationDiskImportImportInProgress
		result.RequeueAfter = base.FastReQ
	default:
		mdi.Status.Phase = api.MigrationDiskImportImportScheduled
		result.RequeueAfter = base.FastReQ
	}

	return r.updateStatus(ctx, mdi)
}

func (r Reconciler) updateStatus(ctx context.Context, mdi *api.MigrationDiskImport) (reconcile.Result, error) {
	mdi.Status.ObservedGeneration = mdi.Generation
	err := r.Status().Update(ctx, mdi)
	return reconcile.Result{}, err
}
