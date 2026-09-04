package toehold

import (
	"context"
	"fmt"
	"strings"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/lib/util"
	"github.com/kubev2v/forklift/pkg/toehold/registry"
	"github.com/kubev2v/forklift/pkg/toehold/version"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

func resolveBootcImageID(ctx context.Context, r Reconciler, toehold *api.Toehold) string {
	image := strings.TrimSpace(toehold.Spec.BootcImage)
	if image == "" {
		return ""
	}
	var auth []byte
	if toehold.Spec.RegistrySecret != nil && toehold.Spec.RegistrySecret.Name != "" {
		secret := &core.Secret{}
		err := r.Get(ctx, types.NamespacedName{
			Namespace: toehold.TargetNS(),
			Name:      toehold.Spec.RegistrySecret.Name,
		}, secret)
		if err == nil {
			auth = secret.Data[".dockerconfigjson"]
		}
	}
	return registry.ResolveImageID(ctx, image, auth)
}

func desiredTemplateHash(spec api.ToeholdSpec, bootcImageID string) string {
	return version.TemplateContentHash(spec, bootcImageID)
}

func desiredVMHash(templateHash, vmName, publicKey string) string {
	return version.VMContentHash(templateHash, vmName, version.SSHKeyFingerprint(publicKey))
}

func sshPublicKey(ctx context.Context, r Reconciler, provider *api.Provider) (string, error) {
	name, err := util.GenerateSSHPublicSecretName(provider.Name)
	if err != nil {
		return "", err
	}
	secret := &core.Secret{}
	err = r.Get(ctx, types.NamespacedName{Namespace: provider.Namespace, Name: name}, secret)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(secret.Data["public-key"])), nil
}

func sshPrivateKey(ctx context.Context, r Reconciler, provider *api.Provider) ([]byte, error) {
	name, err := util.GenerateSSHPrivateSecretName(provider.Name)
	if err != nil {
		return nil, err
	}
	secret := &core.Secret{}
	err = r.Get(ctx, types.NamespacedName{Namespace: provider.Namespace, Name: name}, secret)
	if err != nil {
		return nil, err
	}
	key := secret.Data["private-key"]
	if len(key) == 0 {
		return nil, fmt.Errorf("provider SSH private key secret %q is empty", name)
	}
	return key, nil
}
