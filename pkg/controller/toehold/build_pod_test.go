package toehold

import (
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	core "k8s.io/api/core/v1"
)

func templateSpec() api.ToeholdTemplateSpec {
	return api.ToeholdTemplateSpec{
		BaseDisk: api.ToeholdBaseDisk{
			ContainerImage: "registry.example/rhel-guest-image:9.8",
			WorkGiB:        30,
		},
		TemplateName: "tpl",
		Datastore:    "ds",
		Folder:       "/dc/vm",
		Network:      "VM Network",
	}
}

func TestBuildPod(t *testing.T) {
	r := Reconciler{}
	th := &api.ToeholdTemplate{Spec: templateSpec()}
	th.Name = "test"
	th.Namespace = "default"
	Settings.Toehold.BuilderImage = "builder:latest"
	pod := r.buildPod(th, "creds", "toehold-ssh-keys-provider-public", "ssh-rsa AAAAB3NzaC1yc2E")
	if pod == nil {
		t.Fatal("expected pod")
	}
	if pod.GenerateName != "test-build-" {
		t.Fatalf("unexpected pod generateName %q", pod.GenerateName)
	}
	if pod.Labels[labelToehold] != "test" {
		t.Fatalf("unexpected toehold label %q", pod.Labels[labelToehold])
	}
	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("expected single build container, got %d", len(pod.Spec.Containers))
	}
	build := pod.Spec.Containers[0]
	if _, ok := build.Resources.Limits[core.ResourceName("devices.kubevirt.io/kvm")]; !ok {
		t.Fatal("expected KVM resource limit on build container")
	}
	foundSSH := false
	for _, env := range build.Env {
		if env.Name == "TOEHOLD_SSH_PUBLIC_KEY_FILE" {
			foundSSH = true
		}
	}
	if !foundSSH {
		t.Fatal("expected TOEHOLD_SSH_PUBLIC_KEY_FILE env")
	}
}

func TestBuildPodRootPassword(t *testing.T) {
	r := Reconciler{}
	spec := templateSpec()
	spec.BaseDisk.ContainerImage = "registry.example/rhel:9"
	spec.Customize.RootPassword = "qum5net"
	th := &api.ToeholdTemplate{Spec: spec}
	th.Name = "test"
	Settings.Toehold.BuilderImage = "builder:latest"
	pod := r.buildPod(th, "creds", "toehold-ssh-keys-provider-public", "ssh-rsa AAAAB3NzaC1yc2E")
	for _, env := range pod.Spec.Containers[0].Env {
		if env.Name == "TOEHOLD_ROOT_PASSWORD" && env.Value == "qum5net" {
			return
		}
	}
	t.Fatal("expected TOEHOLD_ROOT_PASSWORD env")
}

func TestBuildPodImagePullSecret(t *testing.T) {
	r := Reconciler{}
	spec := templateSpec()
	spec.BaseDisk.ImagePullSecret = &core.LocalObjectReference{Name: "pull"}
	th := &api.ToeholdTemplate{Spec: spec}
	th.Name = "test"
	th.Namespace = "default"
	Settings.Toehold.BuilderImage = "builder:latest"
	pod := r.buildPod(th, "creds", "toehold-ssh-keys-provider-public", "ssh-rsa AAAAB3NzaC1yc2E")
	if len(pod.Spec.ImagePullSecrets) != 1 || pod.Spec.ImagePullSecrets[0].Name != "pull" {
		t.Fatal("expected imagePullSecrets")
	}
}
