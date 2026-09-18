package copyappliance

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/ref"
	vspheremodel "github.com/kubev2v/forklift/pkg/controller/provider/model/vsphere"
	"github.com/kubev2v/forklift/pkg/controller/provider/web"
	model "github.com/kubev2v/forklift/pkg/controller/provider/web/vsphere"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/kubev2v/forklift/pkg/lib/util"
	"github.com/kubev2v/forklift/pkg/settings"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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

// ApplianceName returns the stable CopyAppliance name for a migration VM.
func ApplianceName(migrationUID types.UID, vmID string) string {
	sum := sha256.Sum256([]byte(vmID))
	vmShort := hex.EncodeToString(sum[:4])
	migShort := string(migrationUID)
	if len(migShort) > 8 {
		migShort = migShort[:8]
	}
	return fmt.Sprintf("%s%s-%s", generateNamePrefix, migShort, vmShort)
}

// rootResourcePool is the name vSphere gives the root resource pool of every
// cluster and standalone host.
const rootResourcePool = "Resources"

// folderWalkLimit bounds the walk from a VM's folder up to its datacenter, so
// that a cycle in the inventory cannot hang a reconcile.
const folderWalkLimit = 100

// checkNameSuffix is appended to a provider's name to name its check appliance.
const checkNameSuffix = "-toehold-check"

// maxVMNameLength is what vSphere accepts as a virtual machine name. A built
// appliance's own name is what its VM is cloned as, so a CopyAppliance cannot
// be named anything vCenter would refuse.
const maxVMNameLength = 80

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
	secretName, err := util.GenerateToeholdSSHPrivateSecretName(provider.Name)
	if err != nil {
		err = liberr.Wrap(err)
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
		Secret: core.ObjectReference{
			Namespace: provider.Namespace,
			Name:      secretName,
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

// BuildCheck returns a CopyAppliance for an appliance VM that reads no disks at
// all. Deploying one exercises everything the appliance path needs — the clone,
// the boot, the guest network, the login, the orchestrator install and the
// export endpoint — against nothing a migration depends on, so a provider can
// be told its appliance works before a plan relies on it.
func BuildCheck(provider *api.Provider, toehold *api.ToeholdTemplate) (appliance *api.CopyAppliance, err error) {
	inventory, err := web.NewClient(provider)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	return buildCheck(inventory, provider, toehold)
}

// buildCheck is BuildCheck over an already-resolved inventory client.
//
// There is no source VM to place the appliance from, so it is placed from the
// toehold template, which is a VM in the datacenter, folder and datastore the
// clone lands in anyway. The inventory collects templates, and reading one by
// moRef reaches the Get handler rather than the List handler that filters them
// out, so placement resolves it like any other VM.
func buildCheck(inventory web.Client, provider *api.Provider, toehold *api.ToeholdTemplate) (appliance *api.CopyAppliance, err error) {
	moRef := toehold.Status.Template.Moref
	if moRef == "" {
		err = liberr.New(fmt.Sprintf(
			"toehold template %s has no moref to place the check appliance from",
			toehold.Name))
		return
	}
	appliance, err = build(inventory, provider, ref.Ref{ID: moRef})
	if err != nil {
		return
	}
	// Placement takes the attach list from the disks of the VM it was pointed
	// at, which here is the template. Leaving it would hand the appliance the
	// template's own root vmdk; a check appliance exports nothing.
	appliance.Spec.AttachDisks = nil
	WithTemplate(appliance, TemplateInventoryPath(toehold))
	// There is no source VM. The label would carry the template's ID, which
	// reads as an appliance serving a VM that is not being migrated.
	delete(appliance.Labels, LabelVM)
	// Named rather than generated: there is one check appliance per provider,
	// and the next pass has to find this one rather than create another.
	appliance.GenerateName = ""
	appliance.Name = CheckName(provider.Name)
	return
}

// CheckName returns the name of a provider's check appliance.
func CheckName(providerName string) string {
	name := providerName + checkNameSuffix
	if len(name) <= maxVMNameLength {
		return name
	}
	return providerName[:maxVMNameLength-len(checkNameSuffix)] + checkNameSuffix
}

// TemplateInventoryPath returns the vSphere inventory path of the toehold template.
func TemplateInventoryPath(toehold *api.ToeholdTemplate) string {
	return path.Join(toehold.Spec.Folder, toehold.Spec.TemplateName)
}

// WithTemplate sets the clone template inventory path on a built appliance.
func WithTemplate(appliance *api.CopyAppliance, template string) {
	appliance.Spec.Template = template
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
	if Settings.CopyAppliance.ResourcePool != "" {
		spec.ResourcePool = Settings.CopyAppliance.ResourcePool
	} else {
		spec.ResourcePool = cluster.Path + "/" + rootResourcePool
	}

	datastore, err := vmDatastore(inventory, vm)
	if err != nil {
		return
	}
	spec.Datastore = datastore.Path
	spec.AttachDisks = attachedDisks(vm)

	return
}

// attachedDisks builds the VMDK attach list from VMware inventory. Shared and
// RDM disks are skipped because the copy appliance path targets flat VMDKs.
func attachedDisks(vm *model.VM) []api.AttachedDisk {
	disks := make([]api.AttachedDisk, 0, len(vm.Disks))
	for _, disk := range vm.Disks {
		if disk.Shared || disk.RDM || disk.File == "" {
			continue
		}
		disks = append(disks, api.AttachedDisk{
			VMDKPath: disk.File,
			DiskKey:  disk.Key,
			Serial:   disk.Serial,
			Capacity: disk.Capacity,
		})
	}
	return disks
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
