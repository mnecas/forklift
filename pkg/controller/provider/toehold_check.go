package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/controller/copyappliance"
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	"github.com/kubev2v/forklift/pkg/settings"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	k8sutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// toeholdCheckDeadline bounds how long a check appliance is given to converge.
// The copy appliance controller polls its wait phases forever, by design: a
// migration would rather wait than give up. A check has nobody waiting on it,
// so one that is stuck has to be called a failure or the provider sits
// in-progress indefinitely behind a message that never becomes true.
const toeholdCheckDeadline = 30 * time.Minute

// ensureToeholdApplianceCheck proves that this provider's copy appliance works,
// before a migration finds out that it does not. It deploys one appliance with
// no disks attached, which exercises the clone, the boot, the guest network,
// the login, the orchestrator install and the mutual-TLS export endpoint, and
// then tears it down. Until that has passed, the provider is not ready.
//
// The check costs a clone and a boot, so it runs once and is then remembered.
// The memo is the durable ToeholdApplianceChecked condition, which carries the
// inputs it was a check of in its Items; the check re-runs when those move.
//
// The verdict is split across two conditions because they need opposite
// lifetimes. A durable condition is staged from the moment
// BeginStagingConditions runs, so a durable Critical would be visible to
// HasBlockerCondition at the top of every later pass — and both updateContainer
// and ensureToeholdTemplate bail on that, which would stop the inventory from
// updating and stop the template from ever being rebuilt to fix the failure.
// So the record is durable and Advisory, and the blocker is Critical and
// re-derived each pass, after those two have run.
//
// Errors are folded into the conditions rather than returned. Reconcile only
// writes the status when the pass returns no error, so returning one here
// would discard the verdict this pass just recorded and re-clone next time.
func (r *Reconciler) ensureToeholdApplianceCheck(ctx context.Context, provider *api.Provider) {
	if provider.DeletionTimestamp != nil {
		return
	}
	if provider.Status.HasBlockerCondition() ||
		!provider.Status.HasCondition(ConnectionTestSucceeded, InventoryCreated) {
		// The provider is already not ready, for a reason that names itself
		// and that has to be fixed before an appliance could deploy anyway.
		return
	}

	toehold, pending := r.toeholdCheckTemplate(ctx, provider)
	if toehold == nil {
		r.setToeholdApplianceNotReady(provider, ToeholdCheckPending, pending)
		return
	}

	inputs := toeholdCheckInputs(toehold)
	recorded := provider.Status.FindCondition(ToeholdApplianceChecked)
	if recorded != nil && slices.Equal(recorded.Items, inputs) {
		// An appliance still here was left behind by a crash between recording
		// the verdict and deleting it.
		r.deleteToeholdCheck(ctx, provider)
		if recorded.Reason != ToeholdCheckPassed {
			r.setToeholdApplianceNotReady(provider, ToeholdCheckFailed, recorded.Message)
		}
		return
	}
	// The record is about something that is no longer deployed. Drop it rather
	// than let the status keep advertising a pass that was about something
	// else; during staging this marks it unstaged, and EndStagingConditions
	// removes it.
	provider.Status.DeleteCondition(ToeholdApplianceChecked)

	r.runToeholdApplianceCheck(ctx, provider, toehold, inputs)
}

// runToeholdApplianceCheck drives the check appliance one pass at a time. The
// appliance has its own controller and its own requeue; all this does is create
// it, read the phase it reached, and clean up once it has settled.
func (r *Reconciler) runToeholdApplianceCheck(
	ctx context.Context,
	provider *api.Provider,
	toehold *api.ToeholdTemplate,
	inputs []string) {
	check := &api.CopyAppliance{}
	err := r.Get(ctx, toeholdCheckKey(provider), check)
	if err != nil {
		if !k8serr.IsNotFound(err) {
			r.setToeholdApplianceNotReady(provider, ToeholdCheckPending,
				fmt.Sprintf("Could not read the check appliance: %s", err))
			return
		}
		r.createToeholdCheck(ctx, provider, toehold, inputs)
		return
	}

	switch {
	case check.DeletionTimestamp != nil:
		// The previous check is still being torn down. Its finalizer is held
		// until the appliance VM is gone, and must never be forced: releasing
		// it early leaks a VM holding vSphere resources with nothing left in
		// the cluster pointing at it.
		r.setToeholdApplianceNotReady(provider, ToeholdCheckPending,
			"Waiting for the previous check appliance to be torn down.")
	case check.Status.Phase == copyappliance.PhaseDeployCompleted:
		r.recordToeholdCheck(ctx, provider, inputs, ToeholdCheckPassed,
			"The copy appliance deployed and served its export endpoint.")
	case check.Status.Phase == copyappliance.PhaseDeployFailed:
		r.recordToeholdCheck(ctx, provider, inputs, ToeholdCheckFailed,
			fmt.Sprintf("The copy appliance failed to deploy: %s", applianceFailure(check)))
	case time.Since(check.CreationTimestamp.Time) > toeholdCheckDeadline:
		r.recordToeholdCheck(ctx, provider, inputs, ToeholdCheckFailed,
			fmt.Sprintf("The copy appliance did not deploy within %s; it is stuck in %s.",
				toeholdCheckDeadline, phaseOf(check)))
	default:
		r.setToeholdApplianceNotReady(provider, ToeholdCheckPending,
			fmt.Sprintf("The copy appliance check is running; it is in %s.", phaseOf(check)))
	}
}

// createToeholdCheck builds and creates the check appliance. It is owned by the
// provider, so deleting the provider takes the appliance with it.
func (r *Reconciler) createToeholdCheck(
	ctx context.Context,
	provider *api.Provider,
	toehold *api.ToeholdTemplate,
	inputs []string) {
	newCheckAppliance := r.newCheckAppliance
	if newCheckAppliance == nil {
		newCheckAppliance = copyappliance.BuildCheck
	}
	check, err := newCheckAppliance(provider, toehold)
	if err == nil {
		err = k8sutil.SetControllerReference(provider, check, r.scheme)
	}
	if err == nil {
		err = r.Create(ctx, check)
		if k8serr.IsAlreadyExists(err) {
			// Created by a pass whose status write did not land. The next pass
			// finds it and reads its phase.
			err = nil
		}
	}
	if err != nil {
		// Nothing was deployed, so there is nothing to be wrong with the
		// appliance itself. This is a failed check all the same: whatever
		// stopped it would stop a migration's appliance too.
		r.Log.Error(err, "Failed to create the toehold check appliance.", "provider", provider.Name)
		r.recordToeholdCheck(ctx, provider, inputs, ToeholdCheckFailed,
			fmt.Sprintf("The copy appliance could not be built: %s", err))
		return
	}
	r.setToeholdApplianceNotReady(provider, ToeholdCheckPending,
		"The copy appliance check has started.")
}

// recordToeholdCheck stores a settled verdict in the durable condition and
// takes the appliance down. A failure is mirrored into the blocking condition
// in the same pass, so a provider does not go ready between reaching a verdict
// and reading it back.
func (r *Reconciler) recordToeholdCheck(
	ctx context.Context,
	provider *api.Provider,
	inputs []string,
	reason string,
	message string) {
	provider.Status.SetCondition(libcnd.Condition{
		Type:     ToeholdApplianceChecked,
		Status:   True,
		Reason:   reason,
		Category: Advisory,
		Message:  message,
		Items:    inputs,
		Durable:  true,
	})
	r.deleteToeholdCheck(ctx, provider)
	if reason != ToeholdCheckPassed {
		r.setToeholdApplianceNotReady(provider, reason, message)
	}
}

// deleteToeholdCheck takes the check appliance down, if there is one and it is
// not already going. The read comes first because this runs on every pass of an
// already-checked provider, where there is usually nothing there; asking the
// cache is cheaper than a delete that always answers NotFound.
//
// A failure is logged rather than reported: the verdict is already recorded,
// and an appliance left behind is found and deleted by the next pass.
func (r *Reconciler) deleteToeholdCheck(ctx context.Context, provider *api.Provider) {
	check := &api.CopyAppliance{}
	err := r.Get(ctx, toeholdCheckKey(provider), check)
	if err != nil {
		if !k8serr.IsNotFound(err) {
			r.Log.Error(err, "Failed to read the toehold check appliance.",
				"provider", provider.Name)
		}
		return
	}
	if check.DeletionTimestamp != nil {
		return
	}
	err = r.Delete(ctx, check)
	if err != nil && !k8serr.IsNotFound(err) {
		r.Log.Error(err, "Failed to delete the toehold check appliance.",
			"provider", provider.Name,
			"appliance", check.Name)
	}
}

// setToeholdApplianceNotReady blocks the provider. Both "the check has not
// passed yet" and "the check failed" are blockers: a provider whose appliance
// is unproven is not one a plan should run against.
func (r *Reconciler) setToeholdApplianceNotReady(provider *api.Provider, reason, message string) {
	provider.Status.SetCondition(libcnd.Condition{
		Type:     ToeholdApplianceNotReady,
		Status:   True,
		Reason:   reason,
		Category: Critical,
		Message:  message,
	})
}

// toeholdCheckInputs is what a verdict is a verdict about. Change any of them
// and the last check says nothing about what would be deployed now. They are
// stored raw rather than hashed together so that a reader of the condition can
// see which one moved.
//
// The template's DiskHash already covers its spec and the appliance's SSH
// public key, and ConfigHash covers its CPU, memory and network, so the
// appliance secret does not need to be read here.
func toeholdCheckInputs(toehold *api.ToeholdTemplate) []string {
	return []string{
		toehold.Status.Template.Moref,
		toehold.Status.Template.DiskHash,
		toehold.Status.Template.ConfigHash,
		Settings.CopyAppliance.ContainerImage,
	}
}

// toeholdCheckTemplate resolves the template the check appliance is cloned
// from. A template that is not there yet is something to wait for rather than a
// failed check, so it is reported as a message and no template.
func (r *Reconciler) toeholdCheckTemplate(
	ctx context.Context,
	provider *api.Provider) (toehold *api.ToeholdTemplate, pending string) {
	if missing := unsetToeholdSettings(); len(missing) > 0 {
		// ensureToeholdTemplate returns silently in this case, so no template
		// will ever appear. Saying so is better than waiting forever.
		pending = fmt.Sprintf(
			"The toehold feature is enabled but not configured; set %s on the ForkliftController.",
			strings.Join(missing, ", "))
		return
	}
	found := &api.ToeholdTemplate{}
	key := client.ObjectKey{
		Namespace: provider.Namespace,
		Name:      toeholdTemplateName(provider),
	}
	err := r.Get(ctx, key, found)
	if err != nil {
		if k8serr.IsNotFound(err) {
			pending = "Waiting for the toehold template to be created."
			return
		}
		pending = fmt.Sprintf("Could not read the toehold template: %s", err)
		return
	}
	if found.Status.Phase != api.ToeholdTemplatePhaseSucceeded || found.Status.Template.Moref == "" {
		pending = fmt.Sprintf("Waiting for the toehold template %s to be built; it is %s.",
			found.Name, templatePhaseOf(found))
		return
	}
	toehold = found
	return
}

// unsetToeholdSettings names the settings the toehold template controller needs
// and does not have, in the form an administrator sets them.
func unsetToeholdSettings() (missing []string) {
	for name, value := range map[string]string{
		settings.ToeholdBaseDiskContainerImage: Settings.Toehold.BaseDiskContainerImage,
		settings.ToeholdDatastore:              Settings.Toehold.Datastore,
		settings.ToeholdFolder:                 Settings.Toehold.Folder,
		settings.ToeholdNetwork:                Settings.Toehold.Network,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	slices.Sort(missing)
	return
}

// toeholdCheckKey is where a provider's check appliance lives.
func toeholdCheckKey(provider *api.Provider) client.ObjectKey {
	return client.ObjectKey{
		Namespace: provider.Namespace,
		Name:      copyappliance.CheckName(provider.Name),
	}
}

// applianceFailure is what the appliance said went wrong.
func applianceFailure(check *api.CopyAppliance) string {
	condition := check.Status.Conditions.FindCondition(libcnd.Ready)
	if condition == nil || condition.Message == "" {
		return "no reason was reported"
	}
	return condition.Message
}

// phaseOf describes where an appliance got to. An appliance the controller has
// not reached yet has no phase at all.
func phaseOf(check *api.CopyAppliance) string {
	if check.Status.Phase == "" {
		return "no phase yet"
	}
	return check.Status.Phase
}

// templatePhaseOf describes where a toehold template got to.
func templatePhaseOf(toehold *api.ToeholdTemplate) string {
	if toehold.Status.Phase == "" {
		return "not started"
	}
	if toehold.Status.Message != "" {
		return fmt.Sprintf("%s: %s", toehold.Status.Phase, toehold.Status.Message)
	}
	return string(toehold.Status.Phase)
}
