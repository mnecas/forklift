package toehold

import (
	"context"
	"fmt"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/controller/base"
	"github.com/kubev2v/forklift/pkg/toehold/annotations"
	toeholdvsphere "github.com/kubev2v/forklift/pkg/toehold/vsphere"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

type providerContext struct {
	Provider *api.Provider
	Secret   *core.Secret
	Client   *toeholdvsphere.Client
}

func (r Reconciler) providerContext(ctx context.Context, toehold *api.Toehold) (*providerContext, error) {
	provider := &api.Provider{}
	err := r.Get(ctx, types.NamespacedName{
		Namespace: toehold.Spec.Provider.Namespace,
		Name:      toehold.Spec.Provider.Name,
	}, provider)
	if err != nil {
		return nil, err
	}
	secret := &core.Secret{}
	err = r.Get(ctx, types.NamespacedName{
		Namespace: provider.Spec.Secret.Namespace,
		Name:      provider.Spec.Secret.Name,
	}, secret)
	if err != nil {
		return nil, err
	}
	client, err := toeholdvsphere.Connect(ctx, toeholdvsphere.ConnectOptions{
		URL:        provider.Spec.URL,
		Username:   string(secret.Data["user"]),
		Password:   string(secret.Data["password"]),
		Thumbprint: provider.Status.Fingerprint,
		Insecure:   base.GetInsecureSkipVerifyFlag(secret),
	})
	if err != nil {
		return nil, err
	}
	return &providerContext{Provider: provider, Secret: secret, Client: client}, nil
}

func stampTemplate(ctx context.Context, client *toeholdvsphere.Client, ref *toeholdvsphere.VMRef, toehold *api.Toehold, bootcImageID, contentHash string) error {
	return client.SetAnnotationMap(ctx, ref.VM, map[string]string{
		annotations.ContentHash:  contentHash,
		annotations.BootcImage:   toehold.Spec.BootcImage,
		annotations.BootcImageID: bootcImageID,
		annotations.ImportedAt:   fmt.Sprint(toehold.CreationTimestamp.Time.UTC().Format("2006-01-02T15:04:05Z")),
	})
}

func stampVM(ctx context.Context, client *toeholdvsphere.Client, ref *toeholdvsphere.VMRef, vmHash string) error {
	return client.SetAnnotationMap(ctx, ref.VM, map[string]string{
		annotations.VMContentHash: vmHash,
	})
}
