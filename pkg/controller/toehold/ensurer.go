package toehold

import (
	"context"
	"fmt"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/controller/base"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/kubev2v/forklift/pkg/lib/util"
	"github.com/kubev2v/forklift/pkg/settings"
	"github.com/kubev2v/forklift/pkg/toehold/version"
	core "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	credsSecretSuffix = "-vcenter-creds"
	labelToehold      = "forklift.konveyor.io/toehold"
	saName            = "toehold-builder"
)

func credsSecretName(toehold *api.ToeholdTemplate) string {
	return toehold.Name + credsSecretSuffix
}

func toeholdSSHPublicSecretName(toehold *api.ToeholdTemplate) (string, error) {
	return util.GenerateToeholdSSHPublicSecretName(toehold.Spec.Provider.Name)
}

func (r Reconciler) setOwner(toehold *api.ToeholdTemplate, obj meta.Object) error {
	if r.Scheme == nil {
		return liberr.New("controller scheme is not configured")
	}
	return controllerutil.SetControllerReference(toehold, obj, r.Scheme)
}

func (r Reconciler) ensureControllerOwner(ctx context.Context, toehold *api.ToeholdTemplate, obj client.Object) error {
	if toehold.UID == "" || toehold.Namespace != obj.GetNamespace() {
		return nil
	}
	if meta.IsControlledBy(obj, toehold) {
		return nil
	}
	if err := r.setOwner(toehold, obj); err != nil {
		return liberr.Wrap(err)
	}
	return liberr.Wrap(r.Update(ctx, obj))
}

func (r Reconciler) ensureServiceAccount(ctx context.Context, toehold *api.ToeholdTemplate) error {
	sa := &core.ServiceAccount{
		ObjectMeta: meta.ObjectMeta{
			Name:      saName,
			Namespace: toehold.TargetNS(),
		},
	}
	err := r.Create(ctx, sa)
	if err != nil && !k8serr.IsAlreadyExists(err) {
		return liberr.Wrap(err)
	}
	return nil
}

func (r Reconciler) ensureCredsSecret(ctx context.Context, toehold *api.ToeholdTemplate, pctx *providerContext, sshPublicKey string) error {
	name := credsSecretName(toehold)
	ns := toehold.TargetNS()
	data := map[string]string{
		settings.VCenterURL:                 pctx.Provider.Spec.URL,
		settings.VCenterUser:                string(pctx.Secret.Data["user"]),
		settings.VCenterPassword:            string(pctx.Secret.Data["password"]),
		settings.VCenterInsecure:            fmt.Sprint(base.GetInsecureSkipVerifyFlag(pctx.Secret)),
		settings.VCenterThumbprint:          pctx.Provider.Status.Fingerprint,
		settings.ToeholdTemplateContentHash: version.DiskHash(toehold.Spec, sshPublicKey),
		settings.ToeholdBaseContainerImage:  toehold.Spec.BaseDisk.ContainerImage,
	}
	secret := &core.Secret{}
	err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, secret)
	if k8serr.IsNotFound(err) {
		secret = &core.Secret{
			ObjectMeta: meta.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
			Type:       core.SecretTypeOpaque,
			StringData: data,
		}
		if err = r.setOwner(toehold, secret); err != nil {
			return liberr.Wrap(err)
		}
		return liberr.Wrap(r.Create(ctx, secret))
	}
	if err != nil {
		return liberr.Wrap(err)
	}
	secret.StringData = data
	if err = r.setOwner(toehold, secret); err != nil {
		return liberr.Wrap(err)
	}
	return liberr.Wrap(r.Update(ctx, secret))
}

func (r Reconciler) ensureBuildPod(ctx context.Context, toehold *api.ToeholdTemplate) (*core.Pod, error) {
	podList := &core.PodList{}
	if err := r.List(ctx, podList, &client.ListOptions{
		Namespace:     toehold.TargetNS(),
		LabelSelector: labels.SelectorFromSet(map[string]string{labelToehold: toehold.Name}),
	}); err != nil {
		return nil, liberr.Wrap(err)
	}

	for i := range podList.Items {
		pod := &podList.Items[i]
		switch pod.Status.Phase {
		case core.PodFailed:
			if err := r.Delete(ctx, pod); err != nil && !k8serr.IsNotFound(err) {
				return nil, liberr.Wrap(err)
			}
			continue
		case core.PodPending, core.PodRunning, core.PodSucceeded:
			if err := r.ensureControllerOwner(ctx, toehold, pod); err != nil {
				return nil, err
			}
			return pod, nil
		}
	}

	sshPublicSecretName, err := toeholdSSHPublicSecretName(toehold)
	if err != nil {
		return nil, liberr.Wrap(err)
	}
	if err = r.ensureSSHPublicSecret(ctx, toehold); err != nil {
		return nil, err
	}
	sshPublicKey, err := r.loadToeholdSSHPublicKey(ctx, toehold)
	if err != nil {
		return nil, liberr.Wrap(err)
	}
	pod := r.buildPod(toehold, credsSecretName(toehold), sshPublicSecretName, sshPublicKey)
	if err := r.setOwner(toehold, pod); err != nil {
		return nil, liberr.Wrap(err)
	}
	if err := r.Create(ctx, pod); err != nil {
		return nil, liberr.Wrap(err)
	}
	return pod, nil
}

func (r Reconciler) deleteBuildPod(ctx context.Context, toehold *api.ToeholdTemplate) error {
	podList := &core.PodList{}
	err := r.List(ctx, podList, &client.ListOptions{
		Namespace:     toehold.TargetNS(),
		LabelSelector: labels.SelectorFromSet(map[string]string{labelToehold: toehold.Name}),
	})
	if err != nil {
		return liberr.Wrap(err)
	}
	for i := range podList.Items {
		if err = r.Delete(ctx, &podList.Items[i]); err != nil && !k8serr.IsNotFound(err) {
			return liberr.Wrap(err)
		}
	}
	return nil
}

// ensureSSHPublicSecret copies the provider's toehold SSH public key into the
// build namespace. Pods can only mount secrets from their own namespace.
func (r Reconciler) ensureSSHPublicSecret(ctx context.Context, toehold *api.ToeholdTemplate) error {
	name, err := toeholdSSHPublicSecretName(toehold)
	if err != nil {
		return liberr.Wrap(err)
	}
	source := &core.Secret{}
	err = r.Get(ctx, client.ObjectKey{
		Namespace: toehold.Spec.Provider.Namespace,
		Name:      name,
	}, source)
	if err != nil {
		return liberr.Wrap(err, "secret", name)
	}
	publicKey, ok := source.Data["public-key"]
	if !ok || len(publicKey) == 0 {
		return liberr.New("toehold SSH public key secret is missing public-key data", "secret", name)
	}

	targetNS := toehold.TargetNS()
	if targetNS == toehold.Spec.Provider.Namespace {
		return nil
	}

	target := &core.Secret{}
	err = r.Get(ctx, client.ObjectKey{Namespace: targetNS, Name: name}, target)
	if k8serr.IsNotFound(err) {
		target = &core.Secret{
			ObjectMeta: meta.ObjectMeta{
				Name:      name,
				Namespace: targetNS,
			},
			Type: core.SecretTypeOpaque,
			Data: map[string][]byte{
				"public-key": publicKey,
			},
		}
		if err = r.setOwner(toehold, target); err != nil {
			return liberr.Wrap(err)
		}
		return liberr.Wrap(r.Create(ctx, target))
	}
	if err != nil {
		return liberr.Wrap(err)
	}
	target.Data = map[string][]byte{
		"public-key": publicKey,
	}
	if err = r.setOwner(toehold, target); err != nil {
		return liberr.Wrap(err)
	}
	return liberr.Wrap(r.Update(ctx, target))
}

func (r Reconciler) loadToeholdSSHPublicKey(ctx context.Context, toehold *api.ToeholdTemplate) (string, error) {
	secretName, err := toeholdSSHPublicSecretName(toehold)
	if err != nil {
		return "", liberr.Wrap(err)
	}
	secret := &core.Secret{}
	err = r.Get(ctx, client.ObjectKey{
		Namespace: toehold.Spec.Provider.Namespace,
		Name:      secretName,
	}, secret)
	if err != nil {
		return "", liberr.Wrap(err, "secret", secretName)
	}
	publicKey, ok := secret.Data["public-key"]
	if !ok || len(publicKey) == 0 {
		return "", liberr.New("toehold SSH public key secret is missing public-key data", "secret", secretName)
	}
	return string(publicKey), nil
}

func (r Reconciler) buildPod(toehold *api.ToeholdTemplate, secretName, sshPublicSecretName, sshPublicKey string) *core.Pod {
	builderImage := Settings.Toehold.BuilderImage
	if toehold.Spec.Images.ToeholdBuilder != "" {
		builderImage = toehold.Spec.Images.ToeholdBuilder
	}
	activeDeadline := int64(7200)
	nodeSelector := map[string]string{"node-role.kubernetes.io/worker": ""}
	for k, v := range toehold.Spec.NodeSelector {
		nodeSelector[k] = v
	}
	nodeSelector["kubevirt.io/schedulable"] = "true"

	workDir := core.EmptyDirVolumeSource{}
	if giB := toehold.Spec.BaseDisk.WorkGiB; giB > 0 {
		workDir.SizeLimit = resource.NewQuantity(giB*1024*1024*1024, resource.BinarySI)
	}

	buildEnv := []core.EnvVar{
		{Name: settings.ToeholdTemplateName, Value: toehold.Spec.TemplateName},
		{Name: settings.ToeholdDatastore, Value: toehold.Spec.Datastore},
		{Name: settings.ToeholdFolder, Value: toehold.Spec.Folder},
		{Name: settings.ToeholdNetwork, Value: toehold.Spec.Network},
		{Name: settings.ToeholdBuildPodCPUs, Value: fmt.Sprint(cpuCount(toehold))},
		{Name: settings.ToeholdBuildPodMemoryMiB, Value: fmt.Sprint(memoryMiB(toehold))},
		{Name: settings.ToeholdTemplateContentHash, Value: version.DiskHash(toehold.Spec, sshPublicKey)},
		{Name: settings.ToeholdTemplateConfigHash, Value: version.ConfigHash(toehold.Spec)},
		{Name: settings.ToeholdBaseContainerImage, Value: toehold.Spec.BaseDisk.ContainerImage},
	}
	if password := toehold.Spec.Customize.RootPassword; password != "" {
		buildEnv = append(buildEnv, core.EnvVar{Name: "TOEHOLD_ROOT_PASSWORD", Value: password})
	}
	buildEnv = append(buildEnv, core.EnvVar{
		Name:  "TOEHOLD_SSH_PUBLIC_KEY_FILE",
		Value: "/etc/toehold/ssh/public-key",
	})

	buildResources := core.ResourceRequirements{
		Requests: core.ResourceList{
			core.ResourceCPU:    resource.MustParse("500m"),
			core.ResourceMemory: resource.MustParse("2Gi"),
			core.ResourceName("devices.kubevirt.io/kvm"): resource.MustParse("1"),
		},
		Limits: core.ResourceList{
			core.ResourceCPU:    resource.MustParse("4"),
			core.ResourceMemory: resource.MustParse("8Gi"),
			core.ResourceName("devices.kubevirt.io/kvm"): resource.MustParse("1"),
		},
	}

	podSpec := core.PodSpec{
		RestartPolicy:         core.RestartPolicyNever,
		ServiceAccountName:    saName,
		NodeSelector:          nodeSelector,
		ActiveDeadlineSeconds: &activeDeadline,
		SecurityContext:       &core.PodSecurityContext{SELinuxOptions: &core.SELinuxOptions{Type: "unconfined_t"}},
		Volumes: []core.Volume{
			{
				Name: "base-disk",
				VolumeSource: core.VolumeSource{
					Image: &core.ImageVolumeSource{
						Reference:  toehold.Spec.BaseDisk.ContainerImage,
						PullPolicy: core.PullIfNotPresent,
					},
				},
			},
			{
				Name:         "work",
				VolumeSource: core.VolumeSource{EmptyDir: &workDir},
			},
			{
				Name: "ssh-public-key",
				VolumeSource: core.VolumeSource{
					Secret: &core.SecretVolumeSource{
						SecretName: sshPublicSecretName,
						Items: []core.KeyToPath{
							{Key: "public-key", Path: "public-key"},
						},
					},
				},
			},
		},
		Containers: []core.Container{
			{
				Name:            "build",
				Image:           builderImage,
				ImagePullPolicy: core.PullAlways,
				SecurityContext: &core.SecurityContext{Privileged: boolPtr(true), RunAsUser: int64Ptr(0), SELinuxOptions: &core.SELinuxOptions{Type: "unconfined_t"}},
				EnvFrom: []core.EnvFromSource{{SecretRef: &core.SecretEnvSource{
					LocalObjectReference: core.LocalObjectReference{Name: secretName},
				}}},
				Env: buildEnv,
				VolumeMounts: []core.VolumeMount{
					{Name: "base-disk", MountPath: "/disk", ReadOnly: true},
					{Name: "work", MountPath: "/work"},
					{Name: "ssh-public-key", MountPath: "/etc/toehold/ssh", ReadOnly: true},
				},
				Resources: buildResources,
			},
		},
	}
	if toehold.Spec.BaseDisk.ImagePullSecret != nil {
		podSpec.ImagePullSecrets = []core.LocalObjectReference{*toehold.Spec.BaseDisk.ImagePullSecret}
	}

	return &core.Pod{
		ObjectMeta: meta.ObjectMeta{
			GenerateName: toehold.Name + "-build-",
			Namespace:    toehold.TargetNS(),
			Labels: map[string]string{
				labelToehold: toehold.Name,
			},
		},
		Spec: podSpec,
	}
}

func cpuCount(toehold *api.ToeholdTemplate) int32 {
	if toehold.Spec.Resources.CPU > 0 {
		return toehold.Spec.Resources.CPU
	}
	return 2
}

func memoryMiB(toehold *api.ToeholdTemplate) int32 {
	if toehold.Spec.Resources.MemoryMiB > 0 {
		return toehold.Spec.Resources.MemoryMiB
	}
	return 4096
}

func boolPtr(v bool) *bool    { return &v }
func int64Ptr(v int64) *int64 { return &v }
