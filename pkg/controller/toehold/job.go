package toehold

import (
	"fmt"
	"strings"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	batch "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	labelToehold = "forklift.konveyor.io/toehold"
	saName       = "toehold-builder"
)

func jobLabels(toehold *api.Toehold) map[string]string {
	return map[string]string{
		labelToehold: toehold.Name,
	}
}

func (r Reconciler) buildJob(toehold *api.Toehold, secretName string) *batch.Job {
	bibImage := Settings.Toehold.BibImage
	uploaderImage := Settings.Toehold.UploaderImage
	if toehold.Spec.Images.BootcImageBuilder != "" {
		bibImage = toehold.Spec.Images.BootcImageBuilder
	}
	if toehold.Spec.Images.ToeholdUploader != "" {
		uploaderImage = toehold.Spec.Images.ToeholdUploader
	}
	backoff := int32(0)
	activeDeadline := int64(7200)
	ttl := int32(86400)
	nodeSelector := map[string]string{"node-role.kubernetes.io/worker": ""}
	for k, v := range toehold.Spec.NodeSelector {
		nodeSelector[k] = v
	}

	volumes := []core.Volume{
		{Name: "work", VolumeSource: core.VolumeSource{EmptyDir: &core.EmptyDirVolumeSource{SizeLimit: resource.NewQuantity(55<<30, resource.BinarySI)}}},
		{Name: "bib-store", VolumeSource: core.VolumeSource{HostPath: &core.HostPathVolumeSource{Path: "/var/tmp/nbdkit-bib-store", Type: hostPathDirOrCreate()}}},
		{Name: "bib-rpmmd", VolumeSource: core.VolumeSource{HostPath: &core.HostPathVolumeSource{Path: "/var/tmp/nbdkit-bib-rpmmd", Type: hostPathDirOrCreate()}}},
		{Name: "bib-containers", VolumeSource: core.VolumeSource{HostPath: &core.HostPathVolumeSource{Path: "/var/tmp/nbdkit-bib-containers", Type: hostPathDirOrCreate()}}},
		{Name: "dev-kvm", VolumeSource: core.VolumeSource{HostPath: &core.HostPathVolumeSource{Path: "/dev/kvm", Type: hostPathCharDevice()}}},
	}
	volumeMounts := []core.VolumeMount{
		{Name: "work", MountPath: "/work"},
		{Name: "bib-store", MountPath: "/store"},
		{Name: "bib-rpmmd", MountPath: "/rpmmd"},
		{Name: "bib-containers", MountPath: "/var/lib/containers/storage"},
		{Name: "dev-kvm", MountPath: "/dev/kvm"},
	}

	bibInitMounts := volumeMounts
	bibInitEnv := []core.EnvVar{
		{Name: "BOOTC_IMAGE", Value: toehold.Spec.BootcImage},
		{Name: "BOOTC_OUTPUT_TYPE", Value: "vmdk"},
	}
	if toehold.Spec.RegistrySecret != nil && toehold.Spec.RegistrySecret.Name != "" {
		bibInitEnv = append(bibInitEnv,
			core.EnvVar{Name: "REGISTRY_AUTH_FILE", Value: "/etc/bib-auth/auth.json"},
			core.EnvVar{Name: "CONTAINERS_AUTH_FILE", Value: "/etc/bib-auth/auth.json"},
		)
		bibInitMounts = append(bibInitMounts, core.VolumeMount{
			Name: "quay-auth", MountPath: "/etc/bib-auth", ReadOnly: true,
		})
	}

	podSpec := core.PodSpec{
		RestartPolicy:      core.RestartPolicyNever,
		ServiceAccountName: saName,
		NodeSelector:       nodeSelector,
		SecurityContext:    &core.PodSecurityContext{SELinuxOptions: &core.SELinuxOptions{Type: "unconfined_t"}},
		Volumes:            volumes,
		InitContainers: []core.Container{
			{
				Name:            "bootc-vmdk",
				Image:           bibImage,
				ImagePullPolicy: core.PullAlways,
				SecurityContext: &core.SecurityContext{Privileged: boolPtr(true), RunAsUser: int64Ptr(0), SELinuxOptions: &core.SELinuxOptions{Type: "unconfined_t"}},
				Env:             bibInitEnv,
				Command:         []string{"/bin/bash", "-c", bibScript(toehold.Spec.RegistrySecret != nil && toehold.Spec.RegistrySecret.Name != "")},
				VolumeMounts:    bibInitMounts,
				Resources: core.ResourceRequirements{
					Requests: core.ResourceList{
						core.ResourceCPU:              resource.MustParse("1"),
						core.ResourceMemory:           resource.MustParse("2Gi"),
						core.ResourceEphemeralStorage: resource.MustParse("20Gi"),
					},
					Limits: core.ResourceList{
						core.ResourceCPU:              resource.MustParse("8"),
						core.ResourceMemory:           resource.MustParse("16Gi"),
						core.ResourceEphemeralStorage: resource.MustParse("80Gi"),
					},
				},
			},
		},
		Containers: []core.Container{
			{
				Name:            "upload",
				Image:           uploaderImage,
				ImagePullPolicy: core.PullAlways,
				SecurityContext: &core.SecurityContext{Privileged: boolPtr(true), RunAsUser: int64Ptr(0)},
				EnvFrom: []core.EnvFromSource{{SecretRef: &core.SecretEnvSource{
					LocalObjectReference: core.LocalObjectReference{Name: secretName},
				}}},
				Env: []core.EnvVar{
					{Name: "TOEHOLD_TEMPLATE_NAME", Value: toehold.Spec.TemplateName},
					{Name: "TOEHOLD_DATASTORE", Value: toehold.Spec.Datastore},
					{Name: "TOEHOLD_FOLDER", Value: toehold.Spec.Folder},
					{Name: "TOEHOLD_NETWORK", Value: toehold.Spec.Network},
					{Name: "TOEHOLD_BOOTC_IMAGE", Value: toehold.Spec.BootcImage},
					{Name: "TOEHOLD_CONTENT_HASH", Value: toehold.Status.Template.ContentHash},
					{Name: "TOEHOLD_BOOTC_IMAGE_ID", Value: toehold.Status.Template.BootcImageID},
					{Name: "TOEHOLD_CPUS", Value: fmt.Sprint(cpuCount(toehold))},
					{Name: "TOEHOLD_MEMORY_MIB", Value: fmt.Sprint(memoryMiB(toehold))},
				},
				VolumeMounts: []core.VolumeMount{{Name: "work", MountPath: "/work"}},
				Resources: core.ResourceRequirements{
					Requests: core.ResourceList{
						core.ResourceCPU:              resource.MustParse("500m"),
						core.ResourceMemory:           resource.MustParse("1Gi"),
						core.ResourceEphemeralStorage: resource.MustParse("10Gi"),
					},
					Limits: core.ResourceList{
						core.ResourceCPU:              resource.MustParse("2"),
						core.ResourceMemory:           resource.MustParse("4Gi"),
						core.ResourceEphemeralStorage: resource.MustParse("30Gi"),
					},
				},
			},
		},
	}
	if toehold.Spec.RegistrySecret != nil && toehold.Spec.RegistrySecret.Name != "" {
		podSpec.Volumes = append(podSpec.Volumes, core.Volume{
			Name: "quay-auth",
			VolumeSource: core.VolumeSource{
				Secret: &core.SecretVolumeSource{
					SecretName: toehold.Spec.RegistrySecret.Name,
					Items:      []core.KeyToPath{{Key: ".dockerconfigjson", Path: "auth.json"}},
				},
			},
		})
	}

	return &batch.Job{
		ObjectMeta: meta.ObjectMeta{
			Name:      fmt.Sprintf("%s-build", toehold.Name),
			Namespace: toehold.TargetNS(),
			Labels:    jobLabels(toehold),
		},
		Spec: batch.JobSpec{
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   &activeDeadline,
			TTLSecondsAfterFinished: &ttl,
			Template: core.PodTemplateSpec{
				ObjectMeta: meta.ObjectMeta{Labels: jobLabels(toehold)},
				Spec:       podSpec,
			},
		},
	}
}

func bibScript(withAuth bool) string {
	authPull := ""
	if withAuth {
		authPull = ` --authfile "${REGISTRY_AUTH_FILE}"`
	}
	return strings.TrimSpace(fmt.Sprintf(`
set -euo pipefail
mkdir -p /work/output /store /rpmmd /var/lib/containers/storage/overlay
extra=()
if command -v podman >/dev/null; then
  podman pull%s "${BOOTC_IMAGE}"
  extra+=(--local)
elif command -v skopeo >/dev/null; then
  skopeo copy%s "docker://${BOOTC_IMAGE}" "containers-storage:${BOOTC_IMAGE}"
  extra+=(--local)
fi
bootc-image-builder build --output /work/output --store /store --rpmmd /rpmmd --type "${BOOTC_OUTPUT_TYPE}" --progress verbose "${extra[@]+"${extra[@]}"}" "${BOOTC_IMAGE}"
find /work/output -name '*.vmdk' -exec ls -lh {} \;
`, authPull, authPull))
}

func cpuCount(toehold *api.Toehold) int32 {
	if toehold.Spec.Resources.CPU > 0 {
		return toehold.Spec.Resources.CPU
	}
	return 2
}

func memoryMiB(toehold *api.Toehold) int32 {
	if toehold.Spec.Resources.MemoryMiB > 0 {
		return toehold.Spec.Resources.MemoryMiB
	}
	return 4096
}

func hostPathDirOrCreate() *core.HostPathType {
	t := core.HostPathDirectoryOrCreate
	return &t
}

func hostPathCharDevice() *core.HostPathType {
	t := core.HostPathCharDev
	return &t
}

func boolPtr(v bool) *bool    { return &v }
func int64Ptr(v int64) *int64 { return &v }
