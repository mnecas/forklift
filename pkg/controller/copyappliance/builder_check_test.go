package copyappliance

import (
	"strings"
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	vspheremodel "github.com/kubev2v/forklift/pkg/controller/provider/model/vsphere"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// testToehold is the provider's toehold template, imported and placed. The
// fixture inventory is reused as-is: the fake resolves whatever moref it is
// given to the same VM, which stands in for the template here.
func testToehold() *api.ToeholdTemplate {
	return &api.ToeholdTemplate{
		ObjectMeta: meta.ObjectMeta{Namespace: "forklift", Name: "vcenter-toehold"},
		Spec: api.ToeholdTemplateSpec{
			TemplateName: "vcenter-toehold",
			Folder:       "/DC0/vm/templates",
		},
		Status: api.ToeholdTemplateStatus{
			Phase:    api.ToeholdTemplatePhaseSucceeded,
			Template: api.TemplateStatus{Moref: "vm-900"},
		},
	}
}

func TestBuildCheck(t *testing.T) {
	withSettings(t, testSettings())
	inventory := testInventory().vmParent(vspheremodel.FolderKind, "folder-apps")
	provider := testProvider()
	toehold := testToehold()

	appliance, err := buildCheck(inventory, provider, toehold)
	if err != nil {
		t.Fatalf("buildCheck: %v", err)
	}

	// The fixture template has a disk. Attaching it would give the appliance
	// the template's own root vmdk to serve.
	if len(appliance.Spec.AttachDisks) != 0 {
		t.Errorf("AttachDisks = %+v, want none", appliance.Spec.AttachDisks)
	}
	if appliance.Spec.Template != "/DC0/vm/templates/vcenter-toehold" {
		t.Errorf("Template = %q, want the toehold template's inventory path", appliance.Spec.Template)
	}
	// Folder comes from the toehold spec; datastore still from the template VM.
	if appliance.Spec.Folder != "/DC0/vm/templates" || appliance.Spec.Datastore != "/DC0/datastore/datastore1" {
		t.Errorf("placement = (%q, %q), want toehold folder and template datastore",
			appliance.Spec.Folder, appliance.Spec.Datastore)
	}
	if appliance.Spec.Secret.Name != "toehold-ssh-keys-vcenter-private" {
		t.Errorf("Secret = %v, want the toehold private secret", appliance.Spec.Secret)
	}

	// Named, so the next pass finds this appliance rather than cloning another.
	if appliance.Name != "vcenter-toehold-check" {
		t.Errorf("Name = %q, want vcenter-toehold-check", appliance.Name)
	}
	if appliance.GenerateName != "" {
		t.Errorf("GenerateName = %q, want it cleared", appliance.GenerateName)
	}
	if _, found := appliance.Labels[LabelVM]; found {
		t.Errorf("label %q is set, but a check appliance has no source VM", LabelVM)
	}
	if appliance.Labels[LabelProvider] != string(provider.UID) {
		t.Errorf("label %q = %q, want the provider's UID",
			LabelProvider, appliance.Labels[LabelProvider])
	}
}

// The CopyAppliance's name is what its VM is cloned as, and vCenter rejects a
// VM name over 80 characters.
func TestCheckNameFitsAVMName(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		want     string
	}{
		{
			name:     "a short name is used whole",
			provider: "vcenter",
			want:     "vcenter-toehold-check",
		},
		{
			name:     "a long name is truncated",
			provider: strings.Repeat("a", 200),
			want:     strings.Repeat("a", maxVMNameLength-len(checkNameSuffix)) + checkNameSuffix,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckName(tt.provider)
			if got != tt.want {
				t.Errorf("CheckName(%d chars) = %q, want %q", len(tt.provider), got, tt.want)
			}
			if len(got) > maxVMNameLength {
				t.Errorf("CheckName(%d chars) is %d long, want at most %d",
					len(tt.provider), len(got), maxVMNameLength)
			}
		})
	}
}

// Placement has nothing to work from without the template's moref, and a
// blank one would send the finder looking for "the default" object.
func TestBuildCheckRejectsAnUnimportedTemplate(t *testing.T) {
	withSettings(t, testSettings())
	inventory := testInventory().vmParent(vspheremodel.FolderKind, "folder-apps")
	toehold := testToehold()
	toehold.Status.Template.Moref = ""

	_, err := buildCheck(inventory, testProvider(), toehold)
	if err == nil {
		t.Fatal("buildCheck succeeded without a template moref")
	}
	if !strings.Contains(err.Error(), "moref") {
		t.Errorf("error = %v, want it to name the missing moref", err)
	}
}
