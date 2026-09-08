package toehold

import (
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	core "k8s.io/api/core/v1"
)

func TestBuildJob(t *testing.T) {
	r := Reconciler{}
	th := &api.Toehold{
		Spec: api.ToeholdSpec{
			BootcImage:   "quay.io/example/bootc:poc",
			TemplateName: "tpl",
			VMName:       "vm",
			Datastore:    "ds",
			Folder:       "/dc/vm",
			Network:      "VM Network",
		},
	}
	th.Name = "test"
	th.Namespace = "default"
	th.Status.Template.ContentHash = "abc"
	th.Status.Template.BootcImageID = "id"
	Settings.Toehold.BibImage = "bib:latest"
	Settings.Toehold.UploaderImage = "uploader:latest"
	job := r.buildJob(th, "creds")
	if job == nil {
		t.Fatal("expected job")
	}
	if len(job.Spec.Template.Spec.InitContainers) != 1 {
		t.Fatalf("expected bib initContainer, got %d", len(job.Spec.Template.Spec.InitContainers))
	}
	if job.Spec.Template.Spec.Containers[0].Name != "upload" {
		t.Fatalf("unexpected container name %q", job.Spec.Template.Spec.Containers[0].Name)
	}
	if job.Spec.Template.Spec.Containers[0].Image != "uploader:latest" {
		t.Fatalf("unexpected uploader image %q", job.Spec.Template.Spec.Containers[0].Image)
	}
}

func TestBuildJobWithoutRegistrySecret(t *testing.T) {
	r := Reconciler{}
	th := &api.Toehold{
		Spec: api.ToeholdSpec{
			BootcImage:   "quay.io/example/bootc:poc",
			TemplateName: "tpl",
			VMName:       "vm",
			Datastore:    "ds",
			Folder:       "/dc/vm",
			Network:      "VM Network",
		},
	}
	th.Name = "test"
	th.Namespace = "default"
	Settings.Toehold.BibImage = "bib:latest"
	Settings.Toehold.UploaderImage = "uploader:latest"

	job := r.buildJob(th, "creds")
	for _, vol := range job.Spec.Template.Spec.Volumes {
		if vol.Name == "quay-auth" {
			t.Fatal("expected no quay-auth volume without registrySecret")
		}
	}
	for _, mount := range job.Spec.Template.Spec.InitContainers[0].VolumeMounts {
		if mount.Name == "quay-auth" {
			t.Fatal("expected no quay-auth mount on initContainer without registrySecret")
		}
	}
}

func TestBuildJobWithRegistrySecret(t *testing.T) {
	r := Reconciler{}
	th := &api.Toehold{
		Spec: api.ToeholdSpec{
			BootcImage:   "quay.io/example/bootc:poc",
			TemplateName: "tpl",
			VMName:       "vm",
			Datastore:    "ds",
			Folder:       "/dc/vm",
			Network:      "VM Network",
			RegistrySecret: &core.LocalObjectReference{
				Name: "nbdkit-appliance-quay",
			},
		},
	}
	th.Name = "test"
	th.Namespace = "default"
	Settings.Toehold.BibImage = "bib:latest"
	Settings.Toehold.UploaderImage = "uploader:latest"

	job := r.buildJob(th, "creds")
	foundVolume := false
	for _, vol := range job.Spec.Template.Spec.Volumes {
		if vol.Name == "quay-auth" {
			foundVolume = true
			if vol.Secret == nil || vol.Secret.SecretName != "nbdkit-appliance-quay" {
				t.Fatalf("unexpected quay-auth volume: %#v", vol)
			}
		}
	}
	if !foundVolume {
		t.Fatal("expected quay-auth volume with registrySecret")
	}
	foundMount := false
	for _, mount := range job.Spec.Template.Spec.InitContainers[0].VolumeMounts {
		if mount.Name == "quay-auth" {
			foundMount = true
		}
	}
	if !foundMount {
		t.Fatal("expected quay-auth mount on initContainer with registrySecret")
	}
}
