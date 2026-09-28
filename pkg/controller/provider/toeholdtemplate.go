package provider

import (
	"context"
	"reflect"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	v1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	k8sutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// toeholdSync keeps a provider's toehold template in step with the provider's
// settings and the controller's own image settings. The template itself is
// created by the console or the API; this only maintains one that is already
// there. Built for a single reconcile pass and discarded.
type toeholdSync struct {
	client    client.Client
	provider  *api.Provider
	datastore string
	folder    string
	network   string
}

// newToeholdSync reads the placement settings once, so that the gate in Run and
// the write in apply cannot disagree about them.
func newToeholdSync(client client.Client, provider *api.Provider) *toeholdSync {
	return &toeholdSync{
		client:    client,
		provider:  provider,
		datastore: provider.Setting(api.ToeholdDatastore),
		folder:    provider.Setting(api.ToeholdFolder),
		network:   provider.Setting(api.ToeholdNetwork),
	}
}

// Run applies the provider's settings to its toehold template.
func (s *toeholdSync) Run(ctx context.Context) (err error) {
	if !inventoryReady(s.provider) {
		return
	}
	if Settings.Toehold.BaseDiskContainerImage == "" ||
		s.datastore == "" || s.folder == "" || s.network == "" {
		return
	}

	template := &api.ToeholdTemplate{}
	key := client.ObjectKey{
		Namespace: s.provider.Namespace,
		Name:      s.provider.ToeholdTemplateName(),
	}
	err = s.client.Get(ctx, key, template)
	if k8serr.IsNotFound(err) {
		// Created explicitly by the console or the API; not ours to make.
		err = nil
		return
	}
	if err != nil {
		err = liberr.Wrap(err, "template", key.Name)
		return
	}

	found := template.DeepCopy()
	s.apply(&template.Spec)
	err = k8sutil.SetControllerReference(s.provider, template, s.client.Scheme())
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	if reflect.DeepEqual(found.Spec, template.Spec) &&
		reflect.DeepEqual(found.OwnerReferences, template.OwnerReferences) {
		return
	}
	// Update rather than Patch: a patch is sent even when the diff is empty,
	// which would turn every pass into a write.
	err = s.client.Update(ctx, template)
	if err != nil {
		err = liberr.Wrap(err, "template", key.Name)
	}
	return
}

// apply writes the fields of the spec that the provider dictates. The rest —
// Customize, TargetNamespace, NodeSelector and RetainTemplate — belong to
// whoever created the template, and are left as they were found.
func (s *toeholdSync) apply(spec *api.ToeholdTemplateSpec) {
	spec.Provider = v1.ObjectReference{
		Name:      s.provider.Name,
		Namespace: s.provider.Namespace,
	}
	spec.TemplateName = s.provider.ToeholdTemplateName()
	spec.BaseDisk = api.ToeholdBaseDisk{
		ContainerImage: Settings.Toehold.BaseDiskContainerImage,
	}
	spec.Resources = api.ToeholdResources{
		CPU:       Settings.Toehold.TemplateCPU,
		MemoryMiB: Settings.Toehold.TemplateMemoryMiB,
	}
	spec.Datastore = s.datastore
	spec.Folder = s.folder
	spec.Network = s.network
	spec.Images = api.ToeholdImages{ToeholdBuilder: Settings.Toehold.BuilderImage}
}
