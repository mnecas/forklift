package copyappliance

import (
	"slices"
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/controller/base"
	"github.com/kubev2v/forklift/pkg/lib/logging"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func testLog() logging.LevelLogger {
	return logging.WithName("copy-appliance-test")
}

func testAppliance() *api.CopyAppliance {
	return &api.CopyAppliance{
		ObjectMeta: meta.ObjectMeta{
			Name:      "appliance",
			Namespace: "forklift",
			UID:       types.UID("11111111-2222-3333-4444-555555555555"),
		},
		Spec: api.CopyApplianceSpec{
			Provider:          core.ObjectReference{Namespace: "forklift", Name: "vsphere"},
			GuestId:           "otherGuest64",
			NumCPUs:           2,
			MemoryMB:          4096,
			Datacenter:        "DC0",
			Datastore:         "datastore1",
			ResourcePool:      "/DC0/host/cluster/Resources",
			Folder:            "/DC0/vm",
			ManagementNetwork: "VM Network",
			TransferNetwork:   "Transfer Network",
			RootDiskPath:      "[datastore1] images/appliance-root.vmdk",
			AttachDiskPaths: []string{
				"[datastore13] vm-a/disk-0.vmdk",
				"[datastore13] vm-b/disk-0.vmdk",
			},
		},
	}
}

// The VM name is the only thing tying a vSphere VM back to its CopyAppliance,
// so it must follow from the CR alone and never from anything the controller
// has to remember.
func TestApplianceVMName(t *testing.T) {
	t.Run("deterministic and UID-scoped", func(t *testing.T) {
		appliance := testAppliance()
		first := applianceVMName(appliance)
		if first != applianceVMName(testAppliance()) {
			t.Error("name is not deterministic")
		}
		appliance.UID = types.UID("99999999-2222-3333-4444-555555555555")
		if applianceVMName(appliance) == first {
			t.Error("name does not vary with the CR UID")
		}
	})

	t.Run("does not depend on renameable or optional fields", func(t *testing.T) {
		appliance := testAppliance()
		want := applianceVMName(appliance)
		appliance.Namespace = "elsewhere"
		appliance.Labels = map[string]string{"k": "v"}
		appliance.Spec.Folder = "/DC0/vm/other"
		if got := applianceVMName(appliance); got != want {
			t.Errorf("applianceVMName = %q, want %q", got, want)
		}
	})

	t.Run("fits in a vSphere VM name", func(t *testing.T) {
		// vSphere caps a VM name at 80 characters. A UID is 36, so the
		// prefixed name has room to spare -- but the prefix is editable.
		const maxVSphereVMName = 80
		if got := applianceVMName(testAppliance()); len(got) > maxVSphereVMName {
			t.Errorf("applianceVMName is %d characters (%q), vSphere allows %d",
				len(got), got, maxVSphereVMName)
		}
	})
}

func TestApplianceRequeueFor(t *testing.T) {
	tests := []struct {
		phase string
		want  string
	}{
		{api.CopyAppliancePhaseProvisioning, "slow"},
		{api.CopyAppliancePhaseCreated, "slow"},
		{api.CopyAppliancePhasePoweringOn, "slow"},
		{api.CopyAppliancePhaseDeleting, "slow"},
		{api.CopyAppliancePhaseFailed, "long"},
		{api.CopyAppliancePhaseReady, "none"},
		{"", "none"},
	}
	for _, tc := range tests {
		t.Run(tc.phase, func(t *testing.T) {
			got := requeueFor(tc.phase)
			switch tc.want {
			case "slow":
				if got != base.SlowReQ {
					t.Errorf("requeueFor(%q) = %v, want SlowReQ", tc.phase, got)
				}
			case "long":
				if got != base.LongReQ {
					t.Errorf("requeueFor(%q) = %v, want LongReQ", tc.phase, got)
				}
			default:
				if got != 0 {
					t.Errorf("requeueFor(%q) = %v, want no requeue", tc.phase, got)
				}
			}
			// Polling vCenter twice a second per CR is never right: every
			// pass opens and closes a session.
			if got != 0 && got < base.SlowReQ {
				t.Errorf("requeueFor(%q) = %v, faster than SlowReQ", tc.phase, got)
			}
		})
	}
}

// A converging appliance must always come back on its own. Only Ready may
// stop, and only because a watch event will bring it back.
func TestApplianceNonTerminalPhasesRequeue(t *testing.T) {
	for _, phase := range []string{
		api.CopyAppliancePhaseProvisioning,
		api.CopyAppliancePhaseCreated,
		api.CopyAppliancePhasePoweringOn,
		api.CopyAppliancePhaseDeleting,
		api.CopyAppliancePhaseFailed,
	} {
		if requeueFor(phase) == 0 {
			t.Errorf("phase %q does not requeue; the appliance would stall", phase)
		}
	}
}

// The client looks the VM up by the Name in this spec and by the recorded
// VMID, so both have to survive the translation from the CR.
func TestApplianceVMSpecCarriesIdentityAndNetworks(t *testing.T) {
	appliance := testAppliance()
	appliance.Status.VMID = "vm-42"

	spec := applianceVMSpec(appliance)
	if spec.Name != applianceVMName(appliance) {
		t.Errorf("spec.Name = %q, want %q", spec.Name, applianceVMName(appliance))
	}
	if spec.VMID != "vm-42" {
		t.Errorf("spec.VMID = %q, want %q", spec.VMID, "vm-42")
	}
	// The adapter attaches one NIC per entry, in order, so management has to
	// come first.
	want := []string{appliance.Spec.ManagementNetwork, appliance.Spec.TransferNetwork}
	if !slices.Equal(spec.Networks, want) {
		t.Errorf("spec.Networks = %v, want %v", spec.Networks, want)
	}
}

// A deployment with no transfer network gets an appliance with one NIC, not one
// with a second card on the finder's default network.
func TestApplianceNetworksWithoutATransferNetwork(t *testing.T) {
	appliance := testAppliance()
	appliance.Spec.TransferNetwork = ""

	networks := applianceNetworks(appliance)
	if !slices.Equal(networks, []string{appliance.Spec.ManagementNetwork}) {
		t.Errorf("networks = %v, want just the management network", networks)
	}
}

func TestApplianceForgetVM(t *testing.T) {
	r := &Reconciler{}

	t.Run("clears the whole identity", func(t *testing.T) {
		appliance := testAppliance()
		appliance.Status.VMID = "vm-42"
		appliance.Status.VCenterInstanceUUID = "uuid-a"

		r.forgetVM(appliance)

		if appliance.Status.VMID != "" || appliance.Status.VCenterInstanceUUID != "" {
			t.Errorf("identity not fully cleared: %+v", appliance.Status)
		}
	})
}

// A moRef means nothing outside the vCenter it came from. Acting on one from
// another vCenter would power on, or destroy, an unrelated VM.
func TestApplianceForgetForeignVM(t *testing.T) {
	r := &Reconciler{Reconciler: base.Reconciler{Log: testLog()}}

	tests := []struct {
		name        string
		vmID        string
		recorded    string
		connected   string
		wantForgot  bool
		description string
	}{
		{"same vCenter is kept", "vm-42", "uuid-a", "uuid-a", false, ""},
		{"different vCenter is forgotten", "vm-42", "uuid-a", "uuid-b", true, ""},
		{"unknown recorded UUID is kept", "vm-42", "", "uuid-b", false,
			"a VM recorded before the UUID was tracked must be adopted, not abandoned"},
		{"unknown connected UUID is kept", "vm-42", "uuid-a", "", false,
			"an unreadable connection UUID is not evidence of a different vCenter"},
		{"no VM recorded", "", "uuid-a", "uuid-b", false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			appliance := testAppliance()
			appliance.Status.VMID = tc.vmID
			appliance.Status.VCenterInstanceUUID = tc.recorded

			r.forgetForeignVM(appliance, tc.connected)

			forgot := appliance.Status.VMID == "" && tc.vmID != ""
			if forgot != tc.wantForgot {
				t.Errorf("forgot = %v, want %v. %s", forgot, tc.wantForgot, tc.description)
			}
		})
	}
}
