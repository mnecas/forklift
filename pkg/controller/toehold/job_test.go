package toehold

import (
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
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
	if job.Spec.Template.Spec.Containers[0].Image != "uploader:latest" {
		t.Fatalf("unexpected uploader image %q", job.Spec.Template.Spec.Containers[0].Image)
	}
}
