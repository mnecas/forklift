package toehold

import (
	"testing"

	"github.com/kubev2v/forklift/pkg/apis"
	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

func TestSetOwnerOnBuildPod(t *testing.T) {
	s := runtime.NewScheme()
	if err := apis.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	th := &api.ToeholdTemplate{
		Spec: api.ToeholdTemplateSpec{
			BaseDisk:     api.ToeholdBaseDisk{ContainerImage: "registry.example/rhel:9"},
			TemplateName: "tpl",
			Datastore:    "ds",
			Folder:       "/dc/vm",
			Network:      "VM Network",
		},
	}
	th.Name = "test"
	th.Namespace = "toehold-e2e"
	th.UID = types.UID("toehold-uid")
	th.APIVersion = api.SchemeGroupVersion.String()
	th.Kind = "ToeholdTemplate"

	r := Reconciler{Scheme: s}
	Settings.Toehold.BuilderImage = "builder:latest"
	pod := r.buildPod(th, "test-vcenter-creds", "toehold-ssh-keys-vcenter-public", "ssh-rsa AAAAB3NzaC1yc2E")
	if err := r.setOwner(th, pod); err != nil {
		t.Fatal(err)
	}
	if len(pod.OwnerReferences) != 1 {
		t.Fatalf("expected one owner reference, got %d", len(pod.OwnerReferences))
	}
	owner := pod.OwnerReferences[0]
	if owner.Kind != "ToeholdTemplate" || owner.Name != "test" {
		t.Fatalf("unexpected owner %#v", owner)
	}
}
