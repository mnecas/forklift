package provider

import (
	"context"
	"strings"
	"testing"
	"time"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/controller/base"
	"github.com/kubev2v/forklift/pkg/controller/copyappliance"
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	"github.com/kubev2v/forklift/pkg/lib/logging"
	"github.com/kubev2v/forklift/pkg/settings"
	core "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const checkName = "vcenter-toehold-check"

// withToeholdSettings configures the feature for the duration of a test. The
// settings are global, and other tests in this package leave them set.
func withToeholdSettings(t *testing.T) {
	t.Helper()
	previous := settings.Settings
	t.Cleanup(func() { settings.Settings = previous })
	Settings.Features.Toehold = true
	Settings.Toehold.BaseDiskContainerImage = "registry.example/rhel:9"
	Settings.CopyAppliance.ContainerImage = "copy-appliance:latest"
}

// checkProvider is a vSphere provider. It carries no conditions: they are set
// during the pass, by pass() below.
func checkProvider() *api.Provider {
	vsphere := api.VSphere
	return &api.Provider{
		ObjectMeta: meta.ObjectMeta{Namespace: "forklift", Name: "vcenter", UID: "provider-uid"},
		Spec: api.ProviderSpec{
			Type: &vsphere,
			Settings: map[string]string{
				api.ToeholdDatastore: "ds1",
				api.ToeholdFolder:    "/DC0/vm",
				api.ToeholdNetwork:   "VM Network",
			},
		},
		Status: api.ProviderStatus{
			ToeholdSSHPrivateSecret: "toehold-ssh-keys-vcenter-private",
			ToeholdSSHPublicSecret:  "toehold-ssh-keys-vcenter-public",
		},
	}
}

// checkTemplate is the provider's toehold template, built and imported.
func checkTemplate() *api.ToeholdTemplate {
	return &api.ToeholdTemplate{
		ObjectMeta: meta.ObjectMeta{Namespace: "forklift", Name: "vcenter-toehold"},
		Spec:       api.ToeholdTemplateSpec{TemplateName: "vcenter-toehold", Folder: "/DC0/vm"},
		Status: api.ToeholdTemplateStatus{
			Phase: api.ToeholdTemplatePhaseSucceeded,
			Template: api.TemplateStatus{
				Moref:      "vm-900",
				DiskHash:   "disk-1",
				ConfigHash: "config-1",
			},
		},
	}
}

// checkAppliance is a check appliance in the given phase, carrying the
// finalizer the copy appliance controller adds.
func checkAppliance(phase string) *api.CopyAppliance {
	appliance := &api.CopyAppliance{
		ObjectMeta: meta.ObjectMeta{
			Namespace:         "forklift",
			Name:              checkName,
			Finalizers:        []string{api.CopyApplianceFinalizer},
			CreationTimestamp: meta.Now(),
		},
	}
	appliance.Status.Phase = phase
	return appliance
}

func testCheckReconciler(t *testing.T, objs ...client.Object) *Reconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := core.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme core: %v", err)
	}
	if err := api.SchemeBuilder.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme forklift: %v", err)
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&api.Provider{}, &api.CopyAppliance{}, &api.ToeholdTemplate{}).
		Build()
	return &Reconciler{
		Reconciler: base.Reconciler{Client: cl, Log: logging.WithName("test")},
		scheme:     scheme,
		newCheckAppliance: func(provider *api.Provider, toehold *api.ToeholdTemplate) (*api.CopyAppliance, error) {
			// Stands in for the real builder, which reads the inventory
			// service. Only the identity matters to this controller.
			return &api.CopyAppliance{
				ObjectMeta: meta.ObjectMeta{
					Namespace: provider.Namespace,
					Name:      copyappliance.CheckName(provider.Name),
				},
			}, nil
		},
	}
}

// pass runs the check the way Reconcile does, inside a staging window. The
// staging is the point: it is what decides which conditions survive to the
// next pass. The two conditions the check gates on are re-set here because
// that is what validate and updateContainer do earlier in the same pass —
// neither is durable, so neither is visible until it is set again.
func (r *Reconciler) pass(provider *api.Provider) {
	provider.Status.BeginStagingConditions()
	provider.Status.SetCondition(
		libcnd.Condition{Type: ConnectionTestSucceeded, Status: True, Category: Required},
		libcnd.Condition{Type: InventoryCreated, Status: True, Category: Required})
	r.ensureToeholdApplianceCheck(context.TODO(), provider)
	provider.Status.EndStagingConditions()
}

// applianceState reports what became of the check appliance.
func applianceState(t *testing.T, r *Reconciler, namespace string) string {
	t.Helper()
	found := &api.CopyAppliance{}
	err := r.Get(context.TODO(), client.ObjectKey{Namespace: namespace, Name: checkName}, found)
	switch {
	case k8serr.IsNotFound(err):
		return "gone"
	case err != nil:
		t.Fatalf("get appliance: %v", err)
		return ""
	case found.DeletionTimestamp != nil:
		return "terminating"
	default:
		return "present"
	}
}

func TestToeholdApplianceCheck(t *testing.T) {
	failed := checkAppliance(copyappliance.PhaseDeployFailed)
	failed.Status.SetCondition(libcnd.Condition{
		Type:     libcnd.Ready,
		Status:   False,
		Category: Error,
		Message:  "the guest never reported an address",
	})

	stale := checkAppliance(copyappliance.PhaseWaitForNetwork)
	stale.CreationTimestamp = meta.NewTime(time.Now().Add(-2 * toeholdCheckDeadline))

	terminating := checkAppliance(copyappliance.PhaseWaitForClone)
	deleted := meta.Now()
	terminating.DeletionTimestamp = &deleted

	tests := []struct {
		name string
		// recorded is a verdict left by an earlier pass.
		recorded *libcnd.Condition
		// objects beyond the provider and its template.
		objects []client.Object
		// template replaces the built and imported one.
		template *api.ToeholdTemplate
		// noTemplate skips creating a ToeholdTemplate in the fake client.
		noTemplate bool

		wantBlockedBy string // reason of ToeholdApplianceNotReady, "" for ready
		wantRecorded  string // reason of ToeholdApplianceChecked, "" for none
		wantMessage   string // substring of whichever condition was set
		wantAppliance string
	}{
		{
			name:          "a provider that has never been checked deploys an appliance",
			wantBlockedBy: ToeholdCheckPending,
			wantMessage:   "started",
			wantAppliance: "present",
		},
		{
			name:          "an appliance that came up records a pass and is torn down",
			objects:       []client.Object{checkAppliance(copyappliance.PhaseDeployCompleted)},
			wantRecorded:  ToeholdCheckPassed,
			wantMessage:   "passed",
			wantAppliance: "terminating",
		},
		{
			name:          "an appliance that failed records why",
			objects:       []client.Object{failed},
			wantBlockedBy: ToeholdCheckFailed,
			wantRecorded:  ToeholdCheckFailed,
			wantMessage:   "check failed",
			wantAppliance: "terminating",
		},
		{
			name:          "an appliance still converging is waited on",
			objects:       []client.Object{checkAppliance(copyappliance.PhaseWaitForNetwork)},
			wantBlockedBy: ToeholdCheckPending,
			wantMessage:   copyappliance.PhaseWaitForNetwork,
			wantAppliance: "present",
		},
		{
			name:          "an appliance that never converged is failed rather than waited on forever",
			objects:       []client.Object{stale},
			wantBlockedBy: ToeholdCheckFailed,
			wantRecorded:  ToeholdCheckFailed,
			wantMessage:   "check failed",
			wantAppliance: "terminating",
		},
		{
			name:          "a teardown in progress is waited out rather than forced",
			objects:       []client.Object{terminating},
			wantBlockedBy: ToeholdCheckPending,
			wantMessage:   "teardown",
			wantAppliance: "terminating",
		},
		{
			name: "a recorded pass for the same inputs deploys nothing",
			recorded: &libcnd.Condition{
				Reason: ToeholdCheckPassed,
				Items:  []string{"vm-900", "disk-1", "config-1", "copy-appliance:latest"},
			},
			wantRecorded:  ToeholdCheckPassed,
			wantAppliance: "gone",
		},
		{
			name: "a recorded failure for the same inputs keeps blocking without redeploying",
			recorded: &libcnd.Condition{
				Reason:  ToeholdCheckFailed,
				Message: "the guest never reported an address",
				Items:   []string{"vm-900", "disk-1", "config-1", "copy-appliance:latest"},
			},
			wantBlockedBy: ToeholdCheckFailed,
			wantRecorded:  ToeholdCheckFailed,
			wantMessage:   "the guest never reported an address",
			wantAppliance: "gone",
		},
		{
			name: "a rebuilt template invalidates the recorded pass",
			recorded: &libcnd.Condition{
				Reason: ToeholdCheckPassed,
				Items:  []string{"vm-800", "disk-0", "config-1", "copy-appliance:latest"},
			},
			wantBlockedBy: ToeholdCheckPending,
			wantMessage:   "started",
			wantAppliance: "present",
		},
		{
			name:          "a template that has not been built yet is waited on",
			template:      &api.ToeholdTemplate{ObjectMeta: meta.ObjectMeta{Namespace: "forklift", Name: "vcenter-toehold"}},
			wantBlockedBy: ToeholdCheckPending,
			wantMessage:   "Waiting for the toehold template",
			wantAppliance: "gone",
		},
		{
			name:          "a missing template is waited on",
			noTemplate:    true,
			wantBlockedBy: ToeholdCheckPending,
			wantMessage:   "Waiting for the toehold template",
			wantAppliance: "gone",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withToeholdSettings(t)
			provider := checkProvider()
			if tt.recorded != nil {
				recorded := *tt.recorded
				recorded.Type = ToeholdApplianceChecked
				recorded.Status = True
				recorded.Category = Advisory
				recorded.Durable = true
				provider.Status.SetCondition(recorded)
			}
			objects := []client.Object{provider}
			if !tt.noTemplate {
				template := tt.template
				if template == nil {
					template = checkTemplate()
				}
				objects = append(objects, template)
			}
			objects = append(objects, tt.objects...)
			r := testCheckReconciler(t, objects...)

			r.pass(provider)

			blocker := provider.Status.FindCondition(ToeholdApplianceNotReady)
			switch {
			case tt.wantBlockedBy == "" && blocker != nil:
				t.Errorf("blocked by %s: %s, want the provider unblocked", blocker.Reason, blocker.Message)
			case tt.wantBlockedBy != "" && blocker == nil:
				t.Errorf("not blocked, want %s", tt.wantBlockedBy)
			case blocker != nil:
				if blocker.Reason != tt.wantBlockedBy {
					t.Errorf("blocked by %s, want %s", blocker.Reason, tt.wantBlockedBy)
				}
				if blocker.Category != Critical {
					t.Errorf("blocker category = %s, want %s", blocker.Category, Critical)
				}
			}
			if (blocker != nil) != provider.Status.HasBlockerCondition() {
				t.Errorf("HasBlockerCondition = %v, want %v",
					provider.Status.HasBlockerCondition(), blocker != nil)
			}

			record := provider.Status.FindCondition(ToeholdApplianceChecked)
			switch {
			case tt.wantRecorded == "" && record != nil:
				t.Errorf("recorded %s, want no record", record.Reason)
			case tt.wantRecorded != "" && record == nil:
				t.Errorf("no record, want %s", tt.wantRecorded)
			case record != nil:
				if record.Reason != tt.wantRecorded {
					t.Errorf("recorded %s, want %s", record.Reason, tt.wantRecorded)
				}
				// Advisory and durable, or it would block the next pass
				// before updateContainer could run.
				if record.Category != Advisory || !record.Durable {
					t.Errorf("record is %s durable=%v, want %s durable=true",
						record.Category, record.Durable, Advisory)
				}
			}

			if tt.wantMessage != "" {
				message := ""
				if record != nil {
					message = record.Message
				}
				if blocker != nil {
					message = blocker.Message
				}
				if !strings.Contains(message, tt.wantMessage) {
					t.Errorf("message = %q, want it to contain %q", message, tt.wantMessage)
				}
			}

			if got := applianceState(t, r, provider.Namespace); got != tt.wantAppliance {
				t.Errorf("appliance is %s, want %s", got, tt.wantAppliance)
			}
		})
	}
}

// The whole point of the record is that the check does not run again. This
// drives two passes through staging, which is what would drop a record that was
// not durable.
func TestToeholdApplianceCheckRunsOnce(t *testing.T) {
	withToeholdSettings(t)
	provider := checkProvider()
	r := testCheckReconciler(t, provider, checkTemplate(),
		checkAppliance(copyappliance.PhaseDeployCompleted))

	r.pass(provider)

	record := provider.Status.FindCondition(ToeholdApplianceChecked)
	if record == nil || record.Reason != ToeholdCheckPassed {
		t.Fatalf("first pass recorded %v, want a pass", record)
	}
	if provider.Status.HasBlockerCondition() {
		t.Fatal("the provider is blocked after a passing check")
	}

	// The appliance is terminating rather than gone, because the fake client
	// honours its finalizer. Release it the way its own controller would.
	appliance := &api.CopyAppliance{}
	key := client.ObjectKey{Namespace: provider.Namespace, Name: checkName}
	if err := r.Get(context.TODO(), key, appliance); err != nil {
		t.Fatalf("get appliance: %v", err)
	}
	appliance.Finalizers = nil
	if err := r.Update(context.TODO(), appliance); err != nil {
		t.Fatalf("release finalizer: %v", err)
	}

	r.pass(provider)

	if provider.Status.FindCondition(ToeholdApplianceChecked) == nil {
		t.Error("the record did not survive the second pass; is it durable?")
	}
	if provider.Status.HasBlockerCondition() {
		t.Error("the provider is blocked on the second pass")
	}
	if got := applianceState(t, r, provider.Namespace); got != "gone" {
		t.Errorf("appliance is %s on the second pass, want it not redeployed", got)
	}
}

// Neither condition may block at the top of a pass. updateContainer and
// ensureToeholdTemplate both bail on HasBlockerCondition and both run before
// the check does, so a recorded failure that blocked would stop the inventory
// from updating and stop the template from ever being rebuilt to fix the very
// failure that was recorded. The record stays out of the way by being
// Advisory; the blocker stays out of the way by not being durable.
func TestToeholdCheckDoesNotBlockTheStartOfTheNextPass(t *testing.T) {
	provider := checkProvider()
	provider.Status.SetCondition(
		libcnd.Condition{
			Type:     ToeholdApplianceChecked,
			Status:   True,
			Reason:   ToeholdCheckFailed,
			Category: Advisory,
			Durable:  true,
			Message:  "the guest never reported an address",
			Items:    []string{"vm-900", "disk-1", "config-1", "copy-appliance:latest"},
		},
		libcnd.Condition{
			Type:     ToeholdApplianceNotReady,
			Status:   True,
			Reason:   ToeholdCheckFailed,
			Category: Critical,
			Message:  "the guest never reported an address",
		})
	if !provider.Status.HasBlockerCondition() {
		t.Fatal("precondition: the failed provider is not blocked")
	}

	provider.Status.BeginStagingConditions()

	if provider.Status.HasBlockerCondition() {
		t.Error("the provider is blocked before the check has re-run; " +
			"updateContainer and ensureToeholdTemplate will not run")
	}
	// The record itself has to still be readable, or the check would run again
	// every pass.
	if provider.Status.FindCondition(ToeholdApplianceChecked) == nil {
		t.Error("the record is not readable during staging; is it durable?")
	}
}

// A provider being deleted has nothing to validate, and adding a blocker to it
// would only get in the way of the finalizers running.
func TestToeholdApplianceCheckSkipsADeletedProvider(t *testing.T) {
	withToeholdSettings(t)
	provider := checkProvider()
	deleted := meta.Now()
	provider.DeletionTimestamp = &deleted
	provider.Finalizers = []string{"forklift"}
	r := testCheckReconciler(t, provider, checkTemplate())

	r.pass(provider)

	if provider.Status.HasBlockerCondition() {
		t.Error("a provider being deleted was blocked by the appliance check")
	}
	if got := applianceState(t, r, provider.Namespace); got != "gone" {
		t.Errorf("appliance is %s, want none deployed for a deleted provider", got)
	}
}

// The appliance is owned by the provider, so deleting the provider takes any
// appliance it left behind with it.
func TestToeholdCheckApplianceIsOwnedByTheProvider(t *testing.T) {
	withToeholdSettings(t)
	provider := checkProvider()
	r := testCheckReconciler(t, provider, checkTemplate())

	r.pass(provider)

	appliance := &api.CopyAppliance{}
	key := client.ObjectKey{Namespace: provider.Namespace, Name: checkName}
	if err := r.Get(context.TODO(), key, appliance); err != nil {
		t.Fatalf("get appliance: %v", err)
	}
	owner := meta.GetControllerOf(appliance)
	if owner == nil || owner.UID != provider.UID {
		t.Errorf("owner = %v, want the provider", owner)
	}
}
