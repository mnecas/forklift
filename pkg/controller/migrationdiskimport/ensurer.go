package migrationdiskimport

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	planbase "github.com/kubev2v/forklift/pkg/controller/plan/adapter/base"
	"github.com/kubev2v/forklift/pkg/settings"
	core "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	importerContainerName = "disk-importer"
	importerMountPath     = "/data"
	vddkMountPath         = "/opt"
	vddkVolumeName        = "vddk-vol-mount"
	vddkInitContainerName = "vddk-side-car"
	importerSecretVolume  = "importer-secret"
	vddkExtraArgsVolume   = "vddk-extra-args"
	vddkExtraArgsMount    = "/vddk-args"
	secretAccessKey       = "accessKeyId"
	secretSecretKey       = "secretKey"
)

// Ensurer manages importer pod lifecycle for a MigrationDiskImport.
type Ensurer struct {
	Client client.Client
}

func (e *Ensurer) EnsureImporterPod(ctx context.Context, mdi *api.MigrationDiskImport, checkpoint string, previous string) (*core.Pod, error) {
	podName := importerPodName(mdi.Name, checkpoint)
	pod := &core.Pod{}
	err := e.Client.Get(ctx, types.NamespacedName{Namespace: mdi.Namespace, Name: podName}, pod)
	if err == nil {
		return pod, nil
	}
	if !k8serr.IsNotFound(err) {
		return nil, err
	}

	targetNS := mdi.Namespace
	if mdi.Spec.Target.Namespace != "" {
		targetNS = mdi.Spec.Target.Namespace
	}
	pvc := &core.PersistentVolumeClaim{}
	err = e.Client.Get(ctx, types.NamespacedName{Namespace: targetNS, Name: mdi.Spec.Target.Name}, pvc)
	if err != nil {
		return nil, err
	}

	pod = e.buildPod(mdi, pvc, podName, checkpoint, previous)
	err = e.Client.Create(ctx, pod)
	if err != nil {
		return nil, err
	}
	return pod, nil
}

func importerPodName(mdiName, checkpoint string) string {
	safe := strings.ReplaceAll(checkpoint, "/", "-")
	if len(safe) > 40 {
		safe = safe[len(safe)-40:]
	}
	return fmt.Sprintf("%s-checkpoint-%s", mdiName, safe)
}

func (e *Ensurer) buildPod(mdi *api.MigrationDiskImport, pvc *core.PersistentVolumeClaim, podName, checkpoint, previous string) *core.Pod {
	image := settings.Settings.Migration.DiskImporterImage
	if image == "" {
		image = "quay.io/kubev2v/forklift-disk-importer:latest"
	}

	volumes := []core.Volume{
		{
			Name: "target",
			VolumeSource: core.VolumeSource{
				PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{
					ClaimName: pvc.Name,
					ReadOnly:  false,
				},
			},
		},
	}
	volumeMounts := []core.VolumeMount{
		{Name: "target", MountPath: importerMountPath},
	}

	env := []core.EnvVar{
		{Name: "TRANSFER_TYPE", Value: string(mdi.Spec.Transfer.Type)},
		{Name: "CHECKPOINT_CURRENT", Value: checkpoint},
		{Name: "CHECKPOINT_PREVIOUS", Value: previous},
		{Name: "FINAL_CHECKPOINT", Value: strconv.FormatBool(mdi.Spec.FinalCheckpoint)},
		{Name: "OWNER_UID", Value: string(mdi.UID)},
		{Name: "PREALLOCATION", Value: "false"},
		{Name: "FILESYSTEM_OVERHEAD", Value: "0.055"},
	}
	if q, ok := pvc.Spec.Resources.Requests[core.ResourceStorage]; ok {
		env = append(env, core.EnvVar{Name: "IMPORTER_IMAGE_SIZE", Value: q.String()})
	}

	if mdi.Spec.Transfer.VDDK != nil {
		v := mdi.Spec.Transfer.VDDK
		env = append(env,
			core.EnvVar{Name: "VDDK_URL", Value: v.URL},
			core.EnvVar{Name: "VDDK_UUID", Value: v.UUID},
			core.EnvVar{Name: "VDDK_BACKING_FILE", Value: v.BackingFile},
			core.EnvVar{Name: "VDDK_THUMBPRINT", Value: v.Thumbprint},
		)
		if v.SecretRef != "" {
			volumes = append(volumes, core.Volume{
				Name: importerSecretVolume,
				VolumeSource: core.VolumeSource{
					Secret: &core.SecretVolumeSource{
						SecretName: v.SecretRef,
					},
				},
			})
			env = append(env,
				core.EnvVar{
					Name: "IMPORTER_ACCESS_KEY_ID",
					ValueFrom: &core.EnvVarSource{
						SecretKeyRef: &core.SecretKeySelector{
							LocalObjectReference: core.LocalObjectReference{Name: v.SecretRef},
							Key:                  secretAccessKey,
						},
					},
				},
				core.EnvVar{
					Name: "IMPORTER_SECRET_KEY",
					ValueFrom: &core.EnvVarSource{
						SecretKeyRef: &core.SecretKeySelector{
							LocalObjectReference: core.LocalObjectReference{Name: v.SecretRef},
							Key:                  secretSecretKey,
						},
					},
				},
			)
		}
		if v.InitImageURL != "" {
			volumes = append(volumes, core.Volume{
				Name: vddkVolumeName,
				VolumeSource: core.VolumeSource{
					EmptyDir: &core.EmptyDirVolumeSource{},
				},
			})
			volumeMounts = append(volumeMounts, core.VolumeMount{Name: vddkVolumeName, MountPath: vddkMountPath})
		}
		if v.ExtraArgsConfigMap != nil && v.ExtraArgsConfigMap.Name != "" {
			volumes = append(volumes, core.Volume{
				Name: vddkExtraArgsVolume,
				VolumeSource: core.VolumeSource{
					ConfigMap: &core.ConfigMapVolumeSource{
						LocalObjectReference: core.LocalObjectReference{
							Name: v.ExtraArgsConfigMap.Name,
						},
					},
				},
			})
			volumeMounts = append(volumeMounts, core.VolumeMount{
				Name:      vddkExtraArgsVolume,
				MountPath: vddkExtraArgsMount,
			})
		}
	}

	var initContainers []core.Container
	if mdi.Spec.Transfer.VDDK != nil && mdi.Spec.Transfer.VDDK.InitImageURL != "" {
		initContainers = append(initContainers, core.Container{
			Name:  vddkInitContainerName,
			Image: mdi.Spec.Transfer.VDDK.InitImageURL,
			VolumeMounts: []core.VolumeMount{
				{Name: vddkVolumeName, MountPath: vddkMountPath},
			},
			TerminationMessagePolicy: core.TerminationMessageFallbackToLogsOnError,
		})
	}

	labels := map[string]string{
		planbase.LabelMigrationDiskImport: "true",
		"app":                             Name,
		"migrationDiskImport":             mdi.Name,
	}
	for k, v := range mdi.Labels {
		labels[k] = v
	}

	pod := &core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Namespace: mdi.Namespace,
			Name:      podName,
			Labels:    labels,
			OwnerReferences: []meta.OwnerReference{
				{
					APIVersion: api.SchemeGroupVersion.String(),
					Kind:       api.MigrationDiskImportKind,
					Name:       mdi.Name,
					UID:        mdi.UID,
				},
			},
		},
		Spec: core.PodSpec{
			RestartPolicy:  core.RestartPolicyOnFailure,
			InitContainers: initContainers,
			Containers: []core.Container{
				{
					Name:            importerContainerName,
					Image:           image,
					ImagePullPolicy: core.PullAlways,
					Env:             env,
					VolumeMounts:    volumeMounts,
					SecurityContext: &core.SecurityContext{
						AllowPrivilegeEscalation: ptr(false),
						Capabilities: &core.Capabilities{
							Drop: []core.Capability{"ALL"},
						},
					},
				},
			},
			Volumes: volumes,
		},
	}
	if mdi.Spec.TransferNetwork != nil && *mdi.Spec.TransferNetwork != "" {
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations["v1.multus-cni.io/default-network"] = *mdi.Spec.TransferNetwork
	}
	if settings.Settings.Features.RetainPrecopyImporterPods {
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations[planbase.AnnRetainAfterCompletion] = "true"
	}
	if sa := settings.Settings.Migration.ServiceAccount; sa != "" {
		pod.Spec.ServiceAccountName = sa
	}
	return pod
}

func ptr[T any](v T) *T { return &v }

func podProgress(pod *core.Pod) string {
	if pod == nil {
		return "0.0%"
	}
	switch pod.Status.Phase {
	case core.PodSucceeded:
		return "100.0%"
	case core.PodRunning:
		return "50.0%"
	default:
		return "0.0%"
	}
}

func checkpointIndex(spec *api.MigrationDiskImportSpec, checkpoint string) int {
	for i, c := range spec.Checkpoints {
		if c.Current == checkpoint {
			return i
		}
	}
	return -1
}

func previousForCheckpoint(spec *api.MigrationDiskImportSpec, idx int) string {
	if idx <= 0 {
		return ""
	}
	return spec.Checkpoints[idx-1].Current
}

func targetNamespace(mdi *api.MigrationDiskImport) string {
	if mdi.Spec.Target.Namespace != "" {
		return mdi.Spec.Target.Namespace
	}
	return mdi.Namespace
}

func pvcBound(ctx context.Context, c client.Client, mdi *api.MigrationDiskImport) (bool, error) {
	pvc := &core.PersistentVolumeClaim{}
	err := c.Get(ctx, types.NamespacedName{
		Namespace: targetNamespace(mdi),
		Name:      mdi.Spec.Target.Name,
	}, pvc)
	if err != nil {
		return false, err
	}
	return pvc.Status.Phase == core.ClaimBound, nil
}

func claimPath(mdi *api.MigrationDiskImport) string {
	return path.Join(targetNamespace(mdi), mdi.Spec.Target.Name)
}
