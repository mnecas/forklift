package toehold

import (
	"context"
	"fmt"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	core "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

func (r Reconciler) validate(ctx context.Context, toehold *api.Toehold) error {
	if toehold.Spec.Provider.Name == "" {
		return liberr.New("spec.provider.name is required")
	}
	if toehold.Spec.BootcImage == "" {
		return liberr.New("spec.bootcImage is required")
	}
	if toehold.Spec.TemplateName == "" {
		return liberr.New("spec.templateName is required")
	}
	if toehold.Spec.VMName == "" {
		return liberr.New("spec.vmName is required")
	}
	if toehold.Spec.Datastore == "" {
		return liberr.New("spec.datastore is required")
	}
	if toehold.Spec.Folder == "" {
		return liberr.New("spec.folder is required")
	}
	if toehold.Spec.Network == "" {
		return liberr.New("spec.network is required")
	}
	for i, disk := range toehold.Spec.Disks {
		if disk.Name == "" {
			return liberr.New(fmt.Sprintf("spec.disks[%d].name is required", i))
		}
		if disk.SourceVM == "" {
			return liberr.New(fmt.Sprintf("spec.disks[%d].sourceVM is required", i))
		}
		if disk.VMDKPath == "" {
			return liberr.New(fmt.Sprintf("spec.disks[%d].vmdkPath is required", i))
		}
	}
	provider := &api.Provider{}
	err := r.Get(ctx, types.NamespacedName{
		Namespace: toehold.Spec.Provider.Namespace,
		Name:      toehold.Spec.Provider.Name,
	}, provider)
	if err != nil {
		return liberr.Wrap(err)
	}
	if provider.Type() != api.VSphere {
		return liberr.New("spec.provider must reference a vSphere provider")
	}
	secret := &core.Secret{}
	err = r.Get(ctx, types.NamespacedName{
		Namespace: provider.Spec.Secret.Namespace,
		Name:      provider.Spec.Secret.Name,
	}, secret)
	if err != nil {
		if k8serr.IsNotFound(err) {
			return liberr.New(fmt.Sprintf("provider secret %s not found", provider.Spec.Secret.Name))
		}
		return liberr.Wrap(err)
	}
	return nil
}
