package copyappliance

import (
	"context"
	"slices"
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/controller/base"
	"github.com/kubev2v/forklift/pkg/lib/logging"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testLog() logging.LevelLogger {
	return logging.WithName("copy-appliance-test")
}

func testReconciler(t *testing.T, objs ...runtime.Object) *Reconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	err := api.SchemeBuilder.AddToScheme(scheme)
	if err != nil {
		t.Fatalf("AddToScheme forklift: %v", err)
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		Build()
	return &Reconciler{
		Reconciler: base.Reconciler{
			Client: cl,
			Log:    testLog(),
		},
	}
}

func testAppliance() *api.CopyAppliance {
	return &api.CopyAppliance{
		ObjectMeta: meta.ObjectMeta{
			Name:      "appliance",
			Namespace: "forklift",
			UID:       types.UID("11111111-2222-3333-4444-555555555555"),
		},
		Spec: api.CopyApplianceSpec{
			Provider:       core.ObjectReference{Namespace: "forklift", Name: "vsphere"},
			Secret:         core.ObjectReference{Namespace: "forklift", Name: "appliance-secret"},
			ContainerImage: "copy-appliance:latest",
			Datacenter:     "DC0",
			Datastore:      "datastore1",
			ResourcePool:   "/DC0/host/cluster/Resources",
			Folder:         "/DC0/vm",
			Template:       "/DC0/vm/appliance-template",
			AttachDisks: []api.AttachedDisk{
				{VMDKPath: "[datastore13] vm-a/disk-0.vmdk", Serial: "wwn-abc", DiskKey: 2000},
				{VMDKPath: "[datastore13] vm-b/disk-0.vmdk", Serial: "wwn-def", DiskKey: 2001},
			},
		},
	}
}

// Every phase has a row so that a phase added without a case here is visible.
func TestApplianceRequeueFor(t *testing.T) {
	tests := []struct {
		name  string
		phase string
		want  string
	}{
		{"an appliance waiting on a clone is polled", PhaseWaitForClone, "slow"},
		// Waiting on a guest to boot, not on a vSphere task, so it is polled
		// at the slower cadence.
		{"an appliance waiting on its network is polled slowly", PhaseWaitForNetwork, "long"},
		// The first step to log in, so this is where an appliance sits while
		// sshd is still coming up, which is a matter of seconds. The load
		// itself runs to completion inside the pass.
		{"an appliance waiting to load its image is polled", PhaseLoadImage, "slow"},
		// Observable while sshd is coming up and again while the supervisor
		// is. A pass that finds the install already in place is two short
		// commands, which is what makes this cadence affordable.
		{"an appliance waiting to be configured is polled", PhaseConfigure, "slow"},
		// Waiting on the guest to enumerate its disks and start a container
		// for each, which is tens of seconds.
		{"an appliance waiting on its exports is polled slowly", PhaseWaitForExports, "long"},
		{"an appliance waiting on a power off is polled", PhaseWaitForPowerOff, "slow"},
		{"an appliance waiting on a disk detach is polled", PhaseWaitForDetachDisks, "slow"},
		{"an appliance waiting on a release detach is polled", PhaseWaitForReleaseDisks, "slow"},
		{"an appliance waiting on a disk attach is polled", PhaseWaitForAttachDisks, "slow"},
		{"an appliance waiting on orchestrator restart is polled", PhaseRestartOrchestrator, "slow"},
		{"an appliance waiting on a destroy is polled", PhaseWaitForDestroyVM, "slow"},
		{"a failed deployment backs off", PhaseDeployFailed, "long"},
		{"a failed teardown backs off", PhaseTeardownFailed, "long"},
		{"a deployed appliance waits for a watch event", PhaseDeployCompleted, "none"},
		{"a torn down appliance waits for a watch event", PhaseTeardownCompleted, "none"},
		{"an unstarted appliance waits for a watch event", "", "none"},
		// The action phases are never observed: ExecutePhase falls through
		// them within the pass that entered them.
		{"a clone is passed through", PhaseCloneVM, "none"},
		{"a power off is passed through", PhasePowerOff, "none"},
		{"a disk detach is passed through", PhaseDetachDisks, "none"},
		{"a destroy is passed through", PhaseDestroyVM, "none"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
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

func TestApplianceForgetVM(t *testing.T) {
	r := &Reconciler{}

	t.Run("every field describing the VM is cleared together", func(t *testing.T) {
		appliance := testAppliance()
		appliance.Status.MoRef = "vm-42"
		appliance.Status.VCenterInstanceUUID = "uuid-a"
		appliance.Status.TaskRef = "task-7"
		appliance.Status.Phase = PhaseWaitForClone
		appliance.Status.Addresses = []api.ApplianceAddress{
			{Network: "VM Network", MAC: "00:50:56:01:02:03", IP: "192.0.2.10"},
		}
		// Exports name ports on the VM being forgotten, so they describe it as
		// surely as its address does.
		appliance.Status.Exports = []api.ApplianceExport{
			{WWID: "wwn-abc", Port: 10809, Device: "/dev/sdb"},
		}

		r.forgetVM(appliance)

		status := appliance.Status
		if status.MoRef != "" ||
			status.VCenterInstanceUUID != "" ||
			status.TaskRef != "" ||
			status.Phase != "" ||
			status.Addresses != nil ||
			status.Exports != nil {
			t.Errorf("identity not fully cleared: %+v", status)
		}
	})
}

// The finalizer is the only thing keeping the CopyAppliance in the cluster
// while its VM is still up. Releasing it early leaves an appliance holding read
// locks on the source vmdks with nothing left to point at it.
func TestRemoveFinalizer(t *testing.T) {
	tests := []struct {
		name     string
		phase    string
		wantHeld bool
	}{
		{"the finalizer is held while teardown is in flight", PhaseWaitForDestroyVM, true},
		{"the finalizer is held when teardown has failed", PhaseTeardownFailed, true},
		{"the finalizer is released once teardown completes", PhaseTeardownCompleted, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			appliance := testAppliance()
			appliance.Finalizers = []string{api.CopyApplianceFinalizer}
			appliance.Status.Phase = tc.phase
			r := testReconciler(t, appliance)

			err := r.RemoveFinalizer(context.TODO(), appliance)
			if err != nil {
				t.Fatalf("RemoveFinalizer: %v", err)
			}

			held := slices.Contains(appliance.Finalizers, api.CopyApplianceFinalizer)
			if held != tc.wantHeld {
				t.Errorf("finalizer held = %v, want %v", held, tc.wantHeld)
			}
		})
	}
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
			appliance.Status.MoRef = tc.vmID
			appliance.Status.VCenterInstanceUUID = tc.recorded

			r.forgetForeignVM(appliance, tc.connected)

			forgot := appliance.Status.MoRef == "" && tc.vmID != ""
			if forgot != tc.wantForgot {
				t.Errorf("forgot = %v, want %v. %s", forgot, tc.wantForgot, tc.description)
			}
		})
	}
}
