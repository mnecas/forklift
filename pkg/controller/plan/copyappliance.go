package plan

import (
	"context"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/plan"
	convctx "github.com/kubev2v/forklift/pkg/controller/conversion/context"
	cacontroller "github.com/kubev2v/forklift/pkg/controller/copyappliance"
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func toeholdTemplateForProvider(r client.Client, provider *api.Provider) (*api.ToeholdTemplate, error) {
	list := &api.ToeholdTemplateList{}
	err := r.List(context.TODO(), list)
	if err != nil {
		return nil, liberr.Wrap(err)
	}
	var fallback *api.ToeholdTemplate
	for i := range list.Items {
		t := &list.Items[i]
		ref := t.Spec.Provider
		if ref.Name != provider.Name || ref.Namespace != provider.Namespace {
			continue
		}
		if t.Status.Phase != api.ToeholdTemplatePhaseSucceeded {
			continue
		}
		if fallback == nil {
			fallback = t
		}
		if !t.Status.Template.Reused {
			return t, nil
		}
	}
	if fallback != nil {
		return fallback, nil
	}
	toeholdName := provider.ToeholdTemplateName()
	toehold := &api.ToeholdTemplate{}
	err = r.Get(context.TODO(), client.ObjectKey{
		Namespace: provider.Namespace,
		Name:      toeholdName,
	}, toehold)
	if err != nil {
		return nil, liberr.Wrap(err, "toehold template", toeholdName)
	}
	return toehold, nil
}

func (r *Migration) ensureCopyAppliance(vm *plan.VMStatus) (err error) {
	provider := r.Source.Provider
	if provider == nil {
		return liberr.New("source provider is not available")
	}

	// Checked before building: building reads the provider's inventory service
	// over the network, and this runs on every pass.
	found, err := r.getCopyAppliance(vm)
	if err != nil {
		return err
	}
	if found != nil {
		return nil
	}

	toehold, err := toeholdTemplateForProvider(r.Client, provider)
	if err != nil {
		return liberr.Wrap(err)
	}

	builder, err := cacontroller.NewBuilder(provider)
	if err != nil {
		return liberr.Wrap(err)
	}
	appliance, err := builder.Appliance(toehold, vm.Ref, r.Migration.UID)
	if err != nil {
		return liberr.Wrap(err)
	}
	// Warm CBT leaves the source tip locked; attach the parent base VMDK instead.
	if r.Plan.IsWarm() {
		for i := range appliance.Spec.AttachDisks {
			appliance.Spec.AttachDisks[i].VMDKPath = cacontroller.BaseVMDKPath(appliance.Spec.AttachDisks[i].VMDKPath)
		}
	}
	appliance.Namespace = provider.Namespace
	appliance.Spec.ExportRequest = &api.ExportRequest{
		Target:     api.ExportTargetExport,
		Generation: 1,
	}

	// The plan labels are not identity; they are what cleanupOrphanedResources
	// selects on.
	appliance.Labels[convctx.LabelPlan] = string(r.Plan.UID)
	appliance.Labels[convctx.LabelPlanName] = r.Plan.Name
	appliance.Labels[convctx.LabelPlanNamespace] = r.Plan.Namespace

	err = controllerutil.SetControllerReference(r.Migration, appliance, scheme.Scheme)
	if err != nil {
		return liberr.Wrap(err)
	}

	_, err = r.copyAppliances().Appliance(context.TODO(), appliance)
	return
}

// copyAppliances finds and creates the migration's copy appliances.
func (r *Migration) copyAppliances() *cacontroller.Ensurer {
	return &cacontroller.Ensurer{Client: r.Client, Log: r.Log}
}

func (r *Migration) patchCopyApplianceExportRequest(vm *plan.VMStatus, target string) error {
	appliance, err := r.getCopyAppliance(vm)
	if err != nil {
		return err
	}
	if appliance == nil {
		return liberr.New("copy appliance is gone", "vm", vm.ID)
	}
	next := int64(1)
	if appliance.Spec.ExportRequest != nil {
		next = appliance.Spec.ExportRequest.Generation + 1
	}
	patch := client.MergeFrom(appliance.DeepCopy())
	appliance.Spec.ExportRequest = &api.ExportRequest{
		Target:     target,
		Generation: next,
	}
	return r.Patch(context.TODO(), appliance, patch)
}

func (r *Migration) releaseCopyAppliance(vm *plan.VMStatus) error {
	return r.patchCopyApplianceExportRequest(vm, api.ExportTargetRelease)
}

func (r *Migration) refreshCopyAppliance(vm *plan.VMStatus) error {
	return r.patchCopyApplianceExportRequest(vm, api.ExportTargetExport)
}

func (r *Migration) waitForCopyAppliance(vm *plan.VMStatus) (ready bool, err error) {
	appliance, err := r.getCopyAppliance(vm)
	if err != nil {
		return false, err
	}
	if appliance == nil {
		return false, liberr.New("copy appliance is gone", "vm", vm.ID)
	}

	switch appliance.Status.Phase {
	case cacontroller.PhaseDeployFailed:
		return false, liberr.New("copy appliance deployment failed")
	case cacontroller.PhaseDeployCompleted:
		if !cacontroller.IsDeployReady(appliance) {
			return false, nil
		}
		req := appliance.Spec.ExportRequest
		if req != nil && req.Target == api.ExportTargetExport &&
			cacontroller.NeedsExportConvergence(appliance) {
			return false, nil
		}
		_, err = cacontroller.ExportNbdConnections(appliance, r.Source.Provider.ToeholdNbdSsl())
		if err != nil {
			return false, liberr.Wrap(err)
		}
		return true, nil
	default:
		readyCond := appliance.Status.Conditions.FindCondition(libcnd.Ready)
		if readyCond != nil && readyCond.Category == libcnd.Critical {
			return false, liberr.New(readyCond.Message)
		}
		return false, nil
	}
}

func (r *Migration) waitForCopyApplianceReleased(vm *plan.VMStatus) (ready bool, err error) {
	appliance, err := r.getCopyAppliance(vm)
	if err != nil {
		return false, err
	}
	if appliance == nil {
		return false, liberr.New("copy appliance is gone", "vm", vm.ID)
	}

	switch appliance.Status.Phase {
	case cacontroller.PhaseDeployFailed:
		return false, liberr.New("copy appliance deployment failed")
	default:
		readyCond := appliance.Status.Conditions.FindCondition(libcnd.Ready)
		if readyCond != nil && readyCond.Category == libcnd.Critical {
			return false, liberr.New(readyCond.Message)
		}
	}

	req := appliance.Spec.ExportRequest
	if req == nil || req.Target != api.ExportTargetRelease {
		return false, nil
	}
	if appliance.Status.Phase != cacontroller.PhaseReleased {
		return false, nil
	}
	if cacontroller.NeedsExportConvergence(appliance) {
		return false, nil
	}
	return true, nil
}

func (r *Migration) teardownCopyAppliance(vm *plan.VMStatus) (done bool, err error) {
	namespace, labels, err := r.copyApplianceKey(vm)
	if err != nil {
		return false, err
	}
	// Not live-only: teardown is reported on the appliance's own status, and by
	// then it has a deletion timestamp.
	appliance, err := r.copyAppliances().Find(context.TODO(), namespace, labels, false)
	if err != nil {
		return false, err
	}
	if appliance == nil {
		return true, nil
	}

	if appliance.DeletionTimestamp == nil {
		err = r.Delete(context.TODO(), appliance)
		if err != nil && !k8serr.IsNotFound(err) {
			return false, liberr.Wrap(err)
		}
		return false, nil
	}

	switch appliance.Status.Phase {
	case cacontroller.PhaseTeardownCompleted, cacontroller.PhaseTeardownFailed:
		return true, nil
	default:
		return false, nil
	}
}

func (r *Migration) deleteCopyAppliance(vm *plan.VMStatus) error {
	appliance, err := r.getCopyAppliance(vm)
	if err != nil {
		return err
	}
	if appliance == nil {
		return nil
	}
	return client.IgnoreNotFound(r.Delete(context.TODO(), appliance))
}

// getCopyAppliance returns the appliance serving the VM, or nil when there is
// none. Absence is not an error: the appliance is created on demand and torn
// down before the migration ends, so most of its callers have a use for nil.
func (r *Migration) getCopyAppliance(vm *plan.VMStatus) (*api.CopyAppliance, error) {
	namespace, labels, err := r.copyApplianceKey(vm)
	if err != nil {
		return nil, err
	}
	return r.copyAppliances().Find(context.TODO(), namespace, labels, true)
}

// copyApplianceKey is where the appliance serving the VM lives and what
// identifies it.
func (r *Migration) copyApplianceKey(vm *plan.VMStatus) (namespace string, labels map[string]string, err error) {
	provider := r.Source.Provider
	if provider == nil {
		err = liberr.New("source provider is not available")
		return
	}
	namespace = provider.Namespace
	labels = r.copyAppliances().Labeler.ApplianceLabels(provider, r.Migration.UID, vm.ID)
	return
}
