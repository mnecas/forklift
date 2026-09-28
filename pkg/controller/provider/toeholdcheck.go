package provider

import (
	"context"
	"fmt"
	"slices"
	"time"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/controller/copyappliance"
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	k8sutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// toeholdCheckDeadline is how long an appliance has to come up before the check
// gives up on it. Generous: a clone, a boot, a login and a container pull.
const toeholdCheckDeadline = 30 * time.Minute

// applianceCheck proves, once per provider, that a copy appliance can be
// deployed from the provider's toehold template — the clone, the boot, the
// guest network, the login, the orchestrator install and the export endpoint —
// so that a migration is not the first thing to find out that it cannot.
//
// The verdict is recorded on ToeholdApplianceChecked and the provider is held
// on ToeholdApplianceNotReady until that verdict is a pass. Persisting the
// conditions is the caller's Reconcile. Built for a single pass and discarded.
type applianceCheck struct {
	client     client.Client
	appliances copyappliance.Ensurer
	provider   *api.Provider
	// build returns the appliance to deploy. A field because the real one reads
	// the provider's inventory service over the network, which a unit test
	// cannot do.
	build func(*api.Provider, *api.ToeholdTemplate) (*api.CopyAppliance, error)
}

func newApplianceCheck(client client.Client, provider *api.Provider) *applianceCheck {
	return &applianceCheck{
		client:     client,
		appliances: copyappliance.Ensurer{Client: client},
		provider:   provider,
		build: func(provider *api.Provider, toehold *api.ToeholdTemplate) (*api.CopyAppliance, error) {
			builder, err := copyappliance.NewBuilder(provider)
			if err != nil {
				return nil, liberr.Wrap(err)
			}
			return builder.Check(toehold)
		},
	}
}

// Run advances the check by one step. Errors are reported to the user as a
// blocking condition before they are returned, so the caller is free to log the
// error rather than propagate it.
func (c *applianceCheck) Run(ctx context.Context) (err error) {
	if c.provider.DeletionTimestamp != nil || !inventoryReady(c.provider) {
		return
	}

	toehold := &api.ToeholdTemplate{}
	key := client.ObjectKey{
		Namespace: c.provider.Namespace,
		Name:      c.provider.ToeholdTemplateName(),
	}
	err = c.client.Get(ctx, key, toehold)
	if k8serr.IsNotFound(err) {
		// The template is created explicitly by the console or the API. Until
		// there is one there is nothing to check and nothing to hold up.
		err = nil
		return
	}
	if err != nil {
		c.block(ToeholdCheckPending,
			fmt.Sprintf("Could not read the toehold template: %s.", err))
		err = liberr.Wrap(err, "template", key.Name)
		return
	}
	if toehold.Status.Phase != api.ToeholdTemplatePhaseSucceeded ||
		toehold.Status.Template.Moref == "" {
		c.block(ToeholdCheckPending, "Waiting for the toehold template.")
		return
	}

	inputs := inputsOf(toehold)
	appliance, err := c.appliances.Find(ctx,
		c.provider.Namespace,
		c.appliances.Labeler.CheckLabels(c.provider),
		true)
	if err != nil {
		c.block(ToeholdCheckPending,
			fmt.Sprintf("Could not read the check appliance: %s.", err))
		return
	}

	// A verdict already reached for these inputs stands, and the appliance it
	// was reached with has served its purpose.
	if recorded := c.provider.Status.FindCondition(ToeholdApplianceChecked); inputs.Match(recorded) {
		if recorded.Reason != ToeholdCheckPassed {
			c.block(ToeholdCheckFailed, recorded.Message)
		}
		return c.teardown(ctx, appliance)
	}
	// The inputs have changed, so any verdict on record is about something else.
	c.provider.Status.DeleteCondition(ToeholdApplianceChecked)

	switch {
	case appliance == nil:
		err = c.deploy(ctx, toehold)
	case appliance.Status.Phase == copyappliance.PhaseDeployCompleted:
		c.pass(inputs)
		err = c.teardown(ctx, appliance)
	case appliance.Status.Phase == copyappliance.PhaseDeployFailed:
		c.fail(inputs, copyappliance.FailureReason(appliance))
		err = c.teardown(ctx, appliance)
	case time.Since(appliance.CreationTimestamp.Time) > toeholdCheckDeadline:
		c.fail(inputs, fmt.Sprintf(
			"the appliance did not come up within %s", toeholdCheckDeadline))
		err = c.teardown(ctx, appliance)
	default:
		c.block(ToeholdCheckPending,
			fmt.Sprintf("Check running (%s).", appliance.Status.Phase))
	}
	return
}

// deploy creates the check appliance and blocks the provider while it comes up.
func (c *applianceCheck) deploy(ctx context.Context, toehold *api.ToeholdTemplate) (err error) {
	defer func() {
		if err != nil {
			c.block(ToeholdCheckFailed,
				fmt.Sprintf("Could not deploy the check appliance: %s.", err))
		}
	}()

	appliance, err := c.build(c.provider, toehold)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	// Owned by the provider, so deleting the provider takes the appliance with
	// it even if the check never settles.
	err = k8sutil.SetControllerReference(c.provider, appliance, c.client.Scheme())
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	err = c.client.Create(ctx, appliance)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	c.block(ToeholdCheckPending, "Copy appliance check started.")
	return
}

// teardown deletes the check appliance, if there is one still to delete. The
// check is a one-off: once the verdict is recorded the appliance has no further
// purpose, and it holds a vCenter VM open until it goes.
func (c *applianceCheck) teardown(ctx context.Context, appliance *api.CopyAppliance) (err error) {
	if appliance == nil || appliance.DeletionTimestamp != nil {
		return
	}
	err = c.client.Delete(ctx, appliance)
	if k8serr.IsNotFound(err) {
		err = nil
	}
	if err != nil {
		err = liberr.Wrap(err, "appliance", appliance.Name)
	}
	return
}

// block records that the provider may not be used until the appliance has been
// shown to work. Deliberately not durable: updateContainer and the template
// sync both bail on a blocker and both run before the check, so a blocker that
// survived staging would stop the inventory updating and stop the template
// being rebuilt to fix the very failure it records.
func (c *applianceCheck) block(reason, message string) {
	c.provider.Status.SetCondition(libcnd.Condition{
		Type:     ToeholdApplianceNotReady,
		Status:   True,
		Reason:   reason,
		Category: Critical,
		Message:  message,
	})
}

// pass records that the check succeeded, and what it was a check of. Durable,
// so that it survives staging: it is the only reason a check that has already
// run is not run again. Advisory, so that it never blocks a pass.
func (c *applianceCheck) pass(inputs checkInputs) {
	c.provider.Status.SetCondition(libcnd.Condition{
		Type:     ToeholdApplianceChecked,
		Status:   True,
		Reason:   ToeholdCheckPassed,
		Category: Advisory,
		Durable:  true,
		Items:    inputs.Items(),
		Message:  fmt.Sprintf("Copy appliance check passed: %s.", inputs.Describe()),
	})
}

// fail records the failure and blocks on it in one call, so that the two cannot
// drift apart. The record keeps the check from running again until the inputs
// change; the blocker keeps the provider out of use until it does.
func (c *applianceCheck) fail(inputs checkInputs, reason string) {
	message := fmt.Sprintf("Copy appliance check failed: %s (%s).", reason, inputs.Describe())
	c.provider.Status.SetCondition(
		libcnd.Condition{
			Type:     ToeholdApplianceChecked,
			Status:   True,
			Reason:   ToeholdCheckFailed,
			Category: Advisory,
			Durable:  true,
			Items:    inputs.Items(),
			Message:  message,
		},
		libcnd.Condition{
			Type:     ToeholdApplianceNotReady,
			Status:   True,
			Reason:   ToeholdCheckFailed,
			Category: Critical,
			Message:  message,
		})
}

// checkInputs is what a check is a check of. A verdict is only good for the
// inputs it was reached with: rebuild the template or change the appliance
// image and the last verdict says nothing about what is there now.
type checkInputs struct {
	moref      string
	diskHash   string
	configHash string
	image      string
}

func inputsOf(toehold *api.ToeholdTemplate) checkInputs {
	return checkInputs{
		moref:      toehold.Status.Template.Moref,
		diskHash:   toehold.Status.Template.DiskHash,
		configHash: toehold.Status.Template.ConfigHash,
		image:      Settings.CopyAppliance.ContainerImage,
	}
}

// Items is the inputs as the condition carries them, and the only place their
// order is written. Match is the only place it is read.
func (i checkInputs) Items() []string {
	return []string{i.moref, i.diskHash, i.configHash, i.image}
}

// Match reports whether a recorded verdict was reached with these inputs.
func (i checkInputs) Match(recorded *libcnd.Condition) bool {
	return recorded != nil && slices.Equal(recorded.Items, i.Items())
}

// Describe names the inputs for the condition message. Items is documented as
// a list of the items referenced in the message, so the message references them.
func (i checkInputs) Describe() string {
	return fmt.Sprintf("template %s (disk %s, config %s), appliance image %s",
		i.moref, i.diskHash, i.configHash, i.image)
}
