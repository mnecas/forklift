package copyappliance

import (
	"fmt"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/ref"
	vspheremodel "github.com/kubev2v/forklift/pkg/controller/provider/model/vsphere"
	"github.com/kubev2v/forklift/pkg/controller/provider/web"
	model "github.com/kubev2v/forklift/pkg/controller/provider/web/vsphere"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/kubev2v/forklift/pkg/settings"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Labels.
const (
	LabelApp      = "app"
	LabelProvider = "provider"
	LabelVM       = "vmID"
	AppForklift   = "forklift"
)

// generateNamePrefix is the metadata.generateName of a built CopyAppliance.
const generateNamePrefix = "copy-appliance-"

// rootResourcePool is the name vSphere gives the root resource pool of every
// cluster and standalone host.
const rootResourcePool = "Resources"

// folderWalkLimit bounds the walk from a VM's folder up to its datacenter, so
// that a cycle in the inventory cannot hang a reconcile.
const folderWalkLimit = 100

// Build returns a CopyAppliance for an appliance VM that will read the given
// source VM's disks. The appliance is placed where the source VM already is:
// the same folder, datacenter and datastore. The shape of the appliance itself,
// its root disk and the networks it is attached to come from the template it is
// cloned from. The returned resource has not been created; the caller creates
// it and the reconciler builds the VM from it.
func Build(provider *api.Provider, vmRef ref.Ref) (appliance *api.CopyAppliance, err error) {
	inventory, err := web.NewClient(provider)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	return build(inventory, provider, vmRef)
}

// build is Build over an already-resolved inventory client.
func build(inventory web.Client, provider *api.Provider, vmRef ref.Ref) (appliance *api.CopyAppliance, err error) {
	if provider.Type() != api.VSphere {
		err = liberr.New(fmt.Sprintf(
			"copy appliances are only supported for vSphere providers; %s is %s",
			provider.Name, provider.Type()))
		return
	}
	if Settings.CopyAppliance.SSHKeySecret == "" {
		// No default: it names an object that only exists in this deployment.
		// Without it the appliance is cloned and then never configured. In the
		// message rather than a key/value pair, because liberr renders only the
		// message and this is the one thing the operator has to go fix.
		err = liberr.New(
			"the copy appliance SSH key secret is not configured; set " +
				settings.CopyApplianceSSHKeySecret)
		return
	}
	if Settings.CopyAppliance.ContainerImage == "" {
		// Also deployment-specific: it names an image stream built into this
		// cluster's registry. Without it the appliance boots with nothing to
		// serve exports with.
		err = liberr.New(
			"the copy appliance container image is not configured; set " +
				settings.CopyApplianceContainerImage)
		return
	}

	spec := api.CopyApplianceSpec{
		Provider: core.ObjectReference{
			Namespace: provider.Namespace,
			Name:      provider.Name,
		},
		SSHKey: core.ObjectReference{
			Namespace: provider.Namespace,
			Name:      Settings.CopyAppliance.SSHKeySecret,
		},
		ContainerImage: Settings.CopyAppliance.ContainerImage,
		// The appliance's shape, root disk and network are not configured here,
		// and the network is not the source VM's: the template carries all of
		// them, and the clone inherits them.
	}
	err = placement(inventory, vmRef, &spec)
	if err != nil {
		return
	}

	appliance = &api.CopyAppliance{
		ObjectMeta: meta.ObjectMeta{
			Namespace:    provider.Namespace,
			GenerateName: generateNamePrefix,
			Labels: map[string]string{
				LabelApp:      AppForklift,
				LabelProvider: string(provider.UID),
				LabelVM:       vmRef.ID,
			},
		},
		Spec: spec,
	}
	return
}

// placement fills in the placement fields of spec from where the source VM
// lives. Every value is an inventory Path, which is built from the object's
// real parent chain and so is in the form the govmomi finder expects.
func placement(inventory web.Client, vmRef ref.Ref, spec *api.CopyApplianceSpec) (err error) {
	vm := &model.VM{}
	err = inventory.Find(vm, vmRef)
	if err != nil {
		err = liberr.Wrap(err, "vm", vmRef.String())
		return
	}

	folder, err := vmFolder(inventory, vm)
	if err != nil {
		return
	}
	spec.Folder = folder.Path

	datacenter, err := folderDatacenter(inventory, folder)
	if err != nil {
		return
	}
	spec.Datacenter = datacenter.Path

	// The host is not placed on directly; it is how the compute resource the
	// appliance's pool belongs to is found.
	host := &model.Host{}
	err = get(inventory, host, &host.Path, vm.Host, "host")
	if err != nil {
		return
	}

	// The inventory does not model resource pools, so the source VM's own pool
	// is unknowable. Its compute resource's root pool is the closest answer.
	cluster := &model.Cluster{}
	err = get(inventory, cluster, &cluster.Path, host.Cluster, "cluster")
	if err != nil {
		return
	}
	spec.ResourcePool = cluster.Path + "/" + rootResourcePool

	datastore, err := vmDatastore(inventory, vm)
	if err != nil {
		return
	}
	spec.Datastore = datastore.Path

	return
}

// vmFolder returns the inventory folder the VM lives in.
func vmFolder(inventory web.Client, vm *model.VM) (folder *model.Folder, err error) {
	if vm.Parent.Kind != vspheremodel.FolderKind {
		err = liberr.New(fmt.Sprintf(
			"VM %s is not in an inventory folder; its parent is a %s",
			vm.ID, vm.Parent.Kind))
		return
	}
	folder = &model.Folder{}
	err = get(inventory, folder, &folder.Path, vm.Parent.ID, "folder")
	if err != nil {
		folder = nil
	}
	return
}

// folderDatacenter returns the datacenter the folder belongs to. Only the
// folder directly beneath a datacenter records it, so a folder nested any
// deeper is walked upward until one does.
func folderDatacenter(inventory web.Client, folder *model.Folder) (datacenter *model.Datacenter, err error) {
	current := folder
	for hop := 0; current.Datacenter == "" && hop < folderWalkLimit; hop++ {
		if current.Folder == "" {
			err = liberr.New(fmt.Sprintf(
				"folder %s is not under a datacenter", folder.ID))
			return
		}
		parent := &model.Folder{}
		err = get(inventory, parent, &parent.Path, current.Folder, "folder")
		if err != nil {
			return
		}
		current = parent
	}
	if current.Datacenter == "" {
		err = liberr.New(fmt.Sprintf(
			"gave up walking folder %s up to a datacenter after %d hops",
			folder.ID, folderWalkLimit))
		return
	}
	datacenter = &model.Datacenter{}
	err = get(inventory, datacenter, &datacenter.Path, current.Datacenter, "datacenter")
	if err != nil {
		datacenter = nil
	}
	return
}

// vmDatastore returns the datastore holding the VM's first disk.
func vmDatastore(inventory web.Client, vm *model.VM) (datastore *model.Datastore, err error) {
	if len(vm.Disks) == 0 {
		err = liberr.New(fmt.Sprintf(
			"VM %s has no disks to take a datastore from", vm.ID))
		return
	}
	datastore = &model.Datastore{}
	err = get(inventory, datastore, &datastore.Path, vm.Disks[0].Datastore.ID, "datastore")
	if err != nil {
		datastore = nil
	}
	return
}

// get reads a resource by ID and rejects an empty path. PathBuilder returns an
// empty path rather than reporting the error behind it, and an empty path is
// not inert: the govmomi finder reads it as "the default", which is a different
// object in a different place. `path` points at the resource's own Path field.
func get(inventory web.Client, resource interface{}, path *string, id, kind string) (err error) {
	if id == "" {
		return liberr.New("the VM has no " + kind)
	}
	err = inventory.Get(resource, id)
	if err != nil {
		return liberr.Wrap(err, kind, id)
	}
	if *path == "" {
		return liberr.New(fmt.Sprintf("the inventory has no path for the %s %s", kind, id))
	}
	return
}
