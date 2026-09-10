package copyappliance

import (
	"context"
	"errors"
	"fmt"
	"time"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/controller/base"
	"github.com/kubev2v/forklift/pkg/controller/plan/adapter/vsphere"
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/kubev2v/forklift/pkg/lib/logging"
	"github.com/kubev2v/forklift/pkg/settings"
	core "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apiserver/pkg/storage/names"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	Name = "copy-appliance"
)

var Settings = &settings.Settings
var log = logging.WithName(Name)

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
	// Primary CR.
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

type Reconciler struct {
	base.Reconciler
}

func (r *Reconciler) Reconcile(ctx context.Context, request reconcile.Request) (result reconcile.Result, err error) {
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
			r.Log.Info("Copy appliance resource deleted.")
			err = nil
		}
		return
	}

	defer func() {
		r.Log.V(2).Info("Conditions.", "all", appliance.Status.Conditions)
	}()

	if appliance.DeletionTimestamp.IsZero() {
		err = r.AddFinalizer(ctx, appliance)
		if err != nil {
			return
		}
		err = r.Deploy(ctx, appliance)
	} else {
		err = r.Teardown(ctx, appliance)
		if err == nil {
			// The appliance VM is gone; releasing the finalizer allows the
			// CR to be removed. No status update is needed afterwards.
			err = r.RemoveFinalizer(ctx, appliance)
			return
		}
	}

	// Always persist status (conditions/phase) even when Deploy/Teardown
	// returned an error, so the user can see why the appliance is not ready.
	r.Record(appliance, appliance.Status.Conditions)
	appliance.Status.ObservedGeneration = appliance.Generation
	uErr := r.Status().Update(ctx, appliance)
	if uErr != nil && err == nil {
		err = liberr.Wrap(uErr)
	}

	// The appliance converges over several passes, waiting on vSphere in
	// between; the phase reached says how soon to look again.
	result.RequeueAfter = requeueFor(appliance.Status.Phase)

	// Done.
	return
}

// requeueFor returns how long to wait before the next pass. Every wait here is
// on vSphere, so it is deliberately slow: each reconcile opens and closes a
// vCenter session, and FastReQ would mean two logins per second per CR.
func requeueFor(phase string) time.Duration {
	switch phase {
	case api.CopyAppliancePhaseProvisioning,
		api.CopyAppliancePhaseCreated,
		api.CopyAppliancePhasePoweringOn,
		api.CopyAppliancePhaseDeleting:
		return base.SlowReQ
	case api.CopyAppliancePhaseFailed:
		// Ended() swallows the error and controller-runtime applies no
		// backoff of its own, so a failed appliance would otherwise retry
		// against vCenter forever at the error cadence.
		return base.LongReQ
	default:
		// Ready, or nothing to do: wait for a watch event.
		return 0
	}
}

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

func (r *Reconciler) RemoveFinalizer(ctx context.Context, appliance *api.CopyAppliance) (err error) {
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

// Deploy drives the appliance VM one step closer to running and returns. Each
// step that has to wait on vSphere leaves a phase behind and lets the requeue
// bring us back, rather than blocking the worker on a poll loop.
func (r *Reconciler) Deploy(ctx context.Context, appliance *api.CopyAppliance) (err error) {
	appliance.Status.BeginStagingConditions()
	defer appliance.Status.EndStagingConditions()

	acClient, err := r.applianceClient(ctx, appliance)
	if err != nil {
		r.setFailed(appliance, "ConnectFailed", err)
		return
	}
	defer acClient.Close()

	// A moRef is unique only within one vCenter. If the provider now points
	// somewhere else, the recorded ID names a stranger's VM.
	r.forgetForeignVM(appliance, acClient.InstanceUUID())

	if appliance.Status.VMID == "" {
		appliance.Status.Phase = api.CopyAppliancePhaseProvisioning
		var vmID string
		vmID, err = acClient.EnsureVM(ctx, applianceVMSpec(appliance))
		if err != nil {
			r.setFailed(appliance, "ProvisionFailed", err)
			return
		}
		appliance.Status.VMID = vmID
		appliance.Status.VCenterInstanceUUID = acClient.InstanceUUID()
		appliance.Status.Phase = api.CopyAppliancePhaseCreated
		r.setPending(appliance, "Created", "The appliance VM has been created and is not yet running.")
		return
	}

	poweredOn, err := acClient.PoweredOn(ctx, appliance.Status.VMID)
	if err != nil {
		if errors.Is(err, vsphere.ErrVMNotFound) {
			// Deleted out from under us; rebuild rather than fail forever.
			r.Log.Info("Appliance VM is gone; recreating.", "vm", appliance.Status.VMID)
			r.forgetVM(appliance)
			appliance.Status.Phase = api.CopyAppliancePhaseProvisioning
			r.setPending(appliance, "Recreating", "The appliance VM no longer exists and is being recreated.")
			return nil
		}
		r.setFailed(appliance, "PowerStateFailed", err)
		return
	}

	if !poweredOn {
		if err = acClient.PowerOn(ctx, appliance.Status.VMID); err != nil {
			r.setFailed(appliance, "PowerOnFailed", err)
			return
		}
		appliance.Status.Phase = api.CopyAppliancePhasePoweringOn
		r.setPending(appliance, "PoweringOn", "The appliance VM has been asked to power on.")
		return
	}

	appliance.Status.Phase = api.CopyAppliancePhaseReady
	appliance.Status.SetCondition(libcnd.Condition{
		Type:     libcnd.Ready,
		Status:   libcnd.True,
		Category: libcnd.Required,
		Message:  "The copy appliance VM has been created and is powered on.",
	})
	return
}

// Teardown removes the appliance VM. It must not release the finalizer until
// the VM is genuinely gone: an appliance left behind holds read locks on the
// source vmdks with nothing left in the cluster to point at it.
func (r *Reconciler) Teardown(ctx context.Context, appliance *api.CopyAppliance) (err error) {
	appliance.Status.BeginStagingConditions()
	defer appliance.Status.EndStagingConditions()

	appliance.Status.Phase = api.CopyAppliancePhaseDeleting
	r.setPending(appliance, "Deleting", "The appliance VM is being deleted.")

	acClient, err := r.applianceClient(ctx, appliance)
	if err != nil {
		appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Ready,
			Status:   libcnd.False,
			Reason:   "ConnectFailed",
			Category: libcnd.Error,
			Message:  err.Error(),
		})
		return
	}
	defer acClient.Close()

	r.forgetForeignVM(appliance, acClient.InstanceUUID())

	vmID := appliance.Status.VMID
	if vmID == "" {
		// The VM may have been created without its moRef ever reaching the
		// status subresource. Its name is the only way back to it.
		vmID, err = acClient.FindByName(ctx, applianceVMSpec(appliance))
		if err != nil {
			appliance.Status.SetCondition(libcnd.Condition{
				Type:     libcnd.Ready,
				Status:   libcnd.False,
				Reason:   "FindFailed",
				Category: libcnd.Error,
				Message:  err.Error(),
			})
			return
		}
		if vmID == "" {
			// Genuinely gone.
			return nil
		}
	}

	err = acClient.DeleteVM(ctx, vmID)
	if err != nil {
		appliance.Status.SetCondition(libcnd.Condition{
			Type:     libcnd.Ready,
			Status:   libcnd.False,
			Reason:   "DeleteFailed",
			Category: libcnd.Error,
			Message:  err.Error(),
		})
		return
	}
	r.forgetVM(appliance)
	return
}

// forgetForeignVM discards a recorded moRef that was written against a
// different vCenter than the one we are connected to. Acting on it would mean
// powering on — or destroying — an unrelated VM.
func (r *Reconciler) forgetForeignVM(appliance *api.CopyAppliance, instanceUUID string) {
	recorded := appliance.Status.VCenterInstanceUUID
	if appliance.Status.VMID == "" || recorded == "" || instanceUUID == "" || recorded == instanceUUID {
		return
	}
	r.Log.Info("Recorded appliance VM belongs to a different vCenter; ignoring it.",
		"vm", appliance.Status.VMID,
		"recorded", recorded,
		"connected", instanceUUID)
	r.forgetVM(appliance)
}

// forgetVM clears every status field that describes a specific VM. They are
// only meaningful together, so they are always cleared together.
func (r *Reconciler) forgetVM(appliance *api.CopyAppliance) {
	appliance.Status.VMID = ""
	appliance.Status.VCenterInstanceUUID = ""
}

// applianceClient resolves the referenced provider and its secret and connects
// to the source provider. The caller owns the returned client and must Close it.
func (r *Reconciler) applianceClient(ctx context.Context, appliance *api.CopyAppliance) (acClient *vsphere.ApplianceClient, err error) {
	provider, secret, err := r.providerWithSecret(ctx, appliance)
	if err != nil {
		return
	}
	acClient, err = vsphere.NewApplianceClient(ctx, provider, secret, r.Log)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	return
}

// providerWithSecret fetches the provider referenced by the CR along with the
// secret holding its credentials.
func (r *Reconciler) providerWithSecret(ctx context.Context, appliance *api.CopyAppliance) (provider *api.Provider, secret *core.Secret, err error) {
	provider = &api.Provider{}
	err = r.Get(
		ctx,
		client.ObjectKey{
			Namespace: appliance.Spec.Provider.Namespace,
			Name:      appliance.Spec.Provider.Name,
		},
		provider)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}

	secret = &core.Secret{}
	err = r.Get(
		ctx,
		client.ObjectKey{
			Namespace: provider.Spec.Secret.Namespace,
			Name:      provider.Spec.Secret.Name,
		},
		secret)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	return
}

// applianceVMSpec builds the vSphere appliance VM spec from the CR.
func applianceVMSpec(appliance *api.CopyAppliance) vsphere.ApplianceVMSpec {
	return vsphere.ApplianceVMSpec{
		Name:            applianceVMName(appliance),
		GuestId:         appliance.Spec.GuestId,
		NumCPUs:         appliance.Spec.NumCPUs,
		MemoryMB:        appliance.Spec.MemoryMB,
		Datacenter:      appliance.Spec.Datacenter,
		Datastore:       appliance.Spec.Datastore,
		ResourcePool:    appliance.Spec.ResourcePool,
		Host:            appliance.Spec.Host,
		Folder:          appliance.Spec.Folder,
		Networks:        applianceNetworks(appliance),
		RootDiskPath:    appliance.Spec.RootDiskPath,
		AttachDiskPaths: appliance.Spec.AttachDiskPaths,
		VMID:            appliance.Status.VMID,
		Annotation: fmt.Sprintf(
			"Forklift copy appliance for CopyAppliance %s/%s.",
			appliance.Namespace, appliance.Name),
	}
}

// applianceNetworks returns the networks to attach to the appliance VM, in NIC
// order. The transfer network is optional, so an appliance may have only the
// one management NIC.
func applianceNetworks(appliance *api.CopyAppliance) []string {
	networks := []string{appliance.Spec.ManagementNetwork}
	if appliance.Spec.TransferNetwork != "" {
		networks = append(networks, appliance.Spec.TransferNetwork)
	}
	return networks
}

// setFailed records a failed reconcile as a not-ready condition and phase.
func (r *Reconciler) setFailed(appliance *api.CopyAppliance, reason string, err error) {
	appliance.Status.Phase = api.CopyAppliancePhaseFailed
	appliance.Status.SetCondition(libcnd.Condition{
		Type:     libcnd.Ready,
		Status:   libcnd.False,
		Reason:   reason,
		Category: libcnd.Error,
		Message:  err.Error(),
	})
}

// setPending records that the appliance is still converging. This is not an
// error: it is the normal state between passes, so it is Advisory.
func (r *Reconciler) setPending(appliance *api.CopyAppliance, reason, message string) {
	appliance.Status.SetCondition(libcnd.Condition{
		Type:     libcnd.Ready,
		Status:   libcnd.False,
		Reason:   reason,
		Category: libcnd.Advisory,
		Message:  message,
	})
}

// applianceVMName returns the name the appliance VM is created and found under.
// It is derived from the CopyAppliance's UID, so the controller can recompute
// it rather than remember it, and so a VM can always be traced back to the
// resource that asked for it. A UID is 36 characters, leaving the result well
// inside the 80 vSphere allows in a VM name.
func applianceVMName(appliance *api.CopyAppliance) string {
	return fmt.Sprintf("forklift-copy-%s", appliance.UID)
}
