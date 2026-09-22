package provider

import (
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	core "k8s.io/api/core/v1"
)

func TestToeholdTemplateDesiredSpec(t *testing.T) {
	Settings.Features.Toehold = true
	Settings.Toehold.BaseDiskContainerImage = "registry.example/rhel:9"
	Settings.Toehold.TemplateCPU = 4
	Settings.Toehold.TemplateMemoryMiB = 8192
	Settings.Toehold.BuilderImage = "builder:latest"

	provider := &api.Provider{}
	provider.Name = "vcenter"
	provider.Namespace = "openshift-mtv"
	provider.Spec.Settings = map[string]string{
		api.ToeholdDatastore: "ds1",
		api.ToeholdFolder:    "/dc/vm",
		api.ToeholdNetwork:   "VM Network",
	}
	datastore, folder, network := toeholdPlacement(provider)

	desired := api.ToeholdTemplateSpec{
		Provider:     core.ObjectReference{Name: provider.Name, Namespace: provider.Namespace},
		TemplateName: provider.Name + "-toehold",
		BaseDisk:     api.ToeholdBaseDisk{ContainerImage: Settings.Toehold.BaseDiskContainerImage},
		Resources: api.ToeholdResources{
			CPU:       Settings.Toehold.TemplateCPU,
			MemoryMiB: Settings.Toehold.TemplateMemoryMiB,
		},
		Datastore: datastore,
		Folder:    folder,
		Network:   network,
		Images:    api.ToeholdImages{ToeholdBuilder: Settings.Toehold.BuilderImage},
	}
	if desired.TemplateName != "vcenter-toehold" {
		t.Fatalf("unexpected template name %q", desired.TemplateName)
	}
	if desired.Resources.CPU != 4 {
		t.Fatalf("unexpected cpu %d", desired.Resources.CPU)
	}
	if desired.Datastore != "ds1" || desired.Folder != "/dc/vm" || desired.Network != "VM Network" {
		t.Fatalf("unexpected placement datastore=%q folder=%q network=%q",
			desired.Datastore, desired.Folder, desired.Network)
	}
}
