package plan

import (
	"context"
	"path"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/plan"
	planbase "github.com/kubev2v/forklift/pkg/controller/plan/adapter/base"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	core "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8slabels "k8s.io/apimachinery/pkg/labels"
	cdi "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *KubeVirt) blankDataVolumesReady(vm *plan.VMStatus) (ready bool, err error) {
	dvs, err := r.getDVs(vm)
	if err != nil {
		return false, err
	}
	if len(dvs) == 0 {
		return false, nil
	}
	for _, dv := range dvs {
		if dv.Status.Phase != cdi.Succeeded {
			return false, nil
		}
	}
	return true, nil
}

func (r *KubeVirt) migrationDiskImportTemplate(vm *plan.VMStatus) *api.MigrationDiskImport {
	labels := r.vmLabels(vm.Ref)
	labels[planbase.LabelMigrationDiskImport] = "true"
	return &api.MigrationDiskImport{
		ObjectMeta: meta.ObjectMeta{
			Namespace:   r.Plan.Spec.TargetNamespace,
			Labels:      labels,
			Annotations: r.vmLabels(vm.Ref),
		},
	}
}

func (r *KubeVirt) EnsureMigrationDiskImports(vm *plan.VMStatus, imports []api.MigrationDiskImport) (err error) {
	list := &api.MigrationDiskImportList{}
	err = r.Destination.Client.List(context.TODO(), list, &client.ListOptions{
		Namespace:     r.Plan.Spec.TargetNamespace,
		LabelSelector: k8slabels.SelectorFromSet(r.vmLabels(vm.Ref)),
	})
	if err != nil {
		return liberr.Wrap(err)
	}

	existing := map[string]struct{}{}
	for _, item := range list.Items {
		existing[item.Name] = struct{}{}
	}

	for _, mdi := range imports {
		found := false
		for _, item := range list.Items {
			if item.Annotations[planbase.AnnDiskSource] == mdi.Annotations[planbase.AnnDiskSource] {
				found = true
				break
			}
		}
		if found {
			continue
		}
		err = r.Destination.Client.Create(context.TODO(), &mdi)
		if err != nil {
			return liberr.Wrap(err)
		}
		r.Log.Info("Created MigrationDiskImport.",
			"mdi", path.Join(mdi.Namespace, mdi.Name),
			"vm", vm.String())
	}
	return nil
}

func (r *KubeVirt) getMigrationDiskImports(vm *plan.VMStatus) (imports []api.MigrationDiskImport, err error) {
	list := &api.MigrationDiskImportList{}
	err = r.Destination.Client.List(context.TODO(), list, &client.ListOptions{
		Namespace:     r.Plan.Spec.TargetNamespace,
		LabelSelector: k8slabels.SelectorFromSet(r.vmLabels(vm.Ref)),
	})
	if err != nil {
		return nil, liberr.Wrap(err)
	}
	return list.Items, nil
}

func (r *KubeVirt) MigrationDiskImports(vm *plan.VMStatus) (imports []api.MigrationDiskImport, err error) {
	builder, ok := r.Builder.(planbase.MigrationDiskImportBuilder)
	if !ok {
		return nil, liberr.New("provider does not support MigrationDiskImport")
	}
	labels := r.vmLabels(vm.Ref)
	labels[kDV] = "true"
	secret, err := r.ensureSecret(vm.Ref, r.secretDataSetterForCDI(vm.Ref), labels)
	if err != nil {
		return nil, err
	}
	var vddkConfigMap *core.ConfigMap
	if r.Source.Provider.UseVddkAioOptimization() {
		vddkConfigMap, err = r.ensureVddkConfigMap()
		if err != nil {
			return nil, err
		}
	}
	template := r.migrationDiskImportTemplate(vm)
	return builder.MigrationDiskImports(vm.Ref, secret, template, vddkConfigMap)
}

func (r *KubeVirt) DeleteMigrationDiskImports(vm *plan.VMStatus) error {
	imports, err := r.getMigrationDiskImports(vm)
	if err != nil {
		return err
	}
	for i := range imports {
		err = r.Destination.Client.Delete(context.TODO(), &imports[i])
		if err != nil && !k8serr.IsNotFound(err) {
			return liberr.Wrap(err)
		}
	}
	return nil
}

func (r *KubeVirt) annotateBlankDVProvisionedPVCs(vm *plan.VMStatus) error {
	dvs, err := r.getDVs(vm)
	if err != nil {
		return err
	}
	for _, dv := range dvs {
		if dv.Status.ClaimName == "" {
			continue
		}
		pvc := &core.PersistentVolumeClaim{}
		err = r.Destination.Client.Get(context.TODO(), client.ObjectKey{
			Namespace: r.Plan.Spec.TargetNamespace,
			Name:      dv.Status.ClaimName,
		}, pvc)
		if err != nil {
			return liberr.Wrap(err)
		}
		if pvc.Annotations == nil {
			pvc.Annotations = map[string]string{}
		}
		pvc.Annotations[planbase.AnnProvisionedByBlankDV] = planbase.ProvisionedByBlankDVValue
		err = r.Destination.Client.Update(context.TODO(), pvc)
		if err != nil {
			return liberr.Wrap(err)
		}
	}
	return nil
}
