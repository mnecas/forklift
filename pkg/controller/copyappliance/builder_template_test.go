package copyappliance

import (
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
)

func TestTemplateInventoryPath(t *testing.T) {
	toehold := &api.ToeholdTemplate{
		Spec: api.ToeholdTemplateSpec{
			Folder:       "/Datacenter/vm",
			TemplateName: "vcenter-toehold",
		},
	}
	if got := TemplateInventoryPath(toehold); got != "/Datacenter/vm/vcenter-toehold" {
		t.Fatalf("TemplateInventoryPath() = %q", got)
	}
}

func TestWithTemplate(t *testing.T) {
	appliance := &api.CopyAppliance{}
	WithTemplate(appliance, "/Datacenter/vm/vcenter-toehold")
	if appliance.Spec.Template != "/Datacenter/vm/vcenter-toehold" {
		t.Fatalf("template = %q", appliance.Spec.Template)
	}
}
