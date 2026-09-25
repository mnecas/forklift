package copyappliance

import (
	"strings"
	"testing"
)

func TestBuildCheck(t *testing.T) {
	withSettings(t, testSettings())
	inventory := testInventory()
	provider := testProvider()
	toehold := testToehold()

	appliance, err := buildCheck(inventory, provider, toehold)
	if err != nil {
		t.Fatalf("buildCheck: %v", err)
	}

	// There is no source VM to take disks from, and the template's own root
	// vmdk is not something a check appliance serves.
	if len(appliance.Spec.AttachDisks) != 0 {
		t.Errorf("AttachDisks = %+v, want none", appliance.Spec.AttachDisks)
	}
	if appliance.Spec.Template != "/DC0/vm/templates/vcenter-toehold" {
		t.Errorf("Template = %q, want the toehold template's inventory path", appliance.Spec.Template)
	}
	// The same placement a migration appliance gets, which is the point: the
	// check proves that placement works.
	if appliance.Spec.Folder != "/DC0/vm/templates" || appliance.Spec.Datastore != "/DC0/datastore/templates" {
		t.Errorf("placement = (%q, %q), want the template's folder and datastore",
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
	inventory := testInventory()
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
