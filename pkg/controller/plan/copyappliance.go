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
	toeholdName := provider.Name + "-toehold"
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

	if _, err = r.getCopyAppliance(vm); err == nil {
		return nil
	}
	if !k8serr.IsNotFound(err) {
		return err
	}

	toehold, err := toeholdTemplateForProvider(r.Client, provider)
	if err != nil {
		return liberr.Wrap(err)
	}

	appliance, err := cacontroller.Build(provider, vm.Ref)
	if err != nil {
		return liberr.Wrap(err)
	}
	// Warm CBT leaves the source tip locked; attach the parent base VMDK instead.
	if r.Plan.IsWarm() {
		for i := range appliance.Spec.AttachDisks {
			appliance.Spec.AttachDisks[i].VMDKPath = cacontroller.BaseVMDKPath(appliance.Spec.AttachDisks[i].VMDKPath)
		}
	}
	cacontroller.WithTemplate(appliance, cacontroller.TemplateInventoryPath(toehold))
	// Clone into the toehold folder so appliances sit with the template.
	appliance.Spec.Folder = toehold.Spec.Folder

	appliance.Name = cacontroller.ApplianceName(r.Migration.UID, vm.ID)
	appliance.Namespace = provider.Namespace
	appliance.GenerateName = ""
	appliance.Spec.ExportRequest = &api.ExportRequest{
		Target:     api.ExportTargetExport,
		Generation: 1,
	}

	appliance.Labels[convctx.LabelPlan] = string(r.Plan.UID)
	appliance.Labels[convctx.LabelPlanName] = r.Plan.Name
	appliance.Labels[convctx.LabelPlanNamespace] = r.Plan.Namespace
	appliance.Labels[convctx.LabelMigration] = string(r.Migration.UID)
	appliance.Labels[cacontroller.LabelVM] = vm.ID

	err = controllerutil.SetControllerReference(r.Migration, appliance, scheme.Scheme)
	if err != nil {
		return liberr.Wrap(err)
	}

	err = r.Create(context.TODO(), appliance)
	if err != nil && !k8serr.IsAlreadyExists(err) {
		return liberr.Wrap(err)
	}
	return nil
}

func (r *Migration) patchCopyApplianceExportRequest(vm *plan.VMStatus, target string) error {
	appliance, err := r.getCopyAppliance(vm)
	if err != nil {
		return err
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

	switch appliance.Status.Phase {
	case cacontroller.PhaseDeployFailed:
		return false, liberr.New("copy appliance deployment failed")
	case cacontroller.PhaseDeployCompleted:
		if !cacontroller.IsDeployReady(appliance) {
			return false, nil
		}
		req := appliance.Spec.ExportRequest
		if req != nil && req.Target == api.ExportTargetExport &&
			!cacontroller.ExportRequestObserved(appliance) {
			return false, nil
		}
		_, err = cacontroller.ExportNbdConnections(appliance)
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
	if !cacontroller.ExportRequestObserved(appliance) {
		return false, nil
	}
	return true, nil
}

func (r *Migration) teardownCopyAppliance(vm *plan.VMStatus) (done bool, err error) {
	appliance, err := r.getCopyAppliance(vm)
	if err != nil {
		if k8serr.IsNotFound(err) {
			return true, nil
		}
		return false, err
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
	if k8serr.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return client.IgnoreNotFound(r.Delete(context.TODO(), appliance))
}

func (r *Migration) getCopyAppliance(vm *plan.VMStatus) (*api.CopyAppliance, error) {
	provider := r.Source.Provider
	if provider == nil {
		return nil, liberr.New("source provider is not available")
	}
	appliance := &api.CopyAppliance{}
	err := r.Get(context.TODO(), client.ObjectKey{
		Namespace: provider.Namespace,
		Name:      cacontroller.ApplianceName(r.Migration.UID, vm.ID),
	}, appliance)
	if err != nil {
		return nil, liberr.Wrap(err)
	}
	return appliance, nil
}
