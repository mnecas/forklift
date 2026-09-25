package copyappliance

import (
	"crypto/sha256"
	"encoding/hex"
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
	"k8s.io/apimachinery/pkg/types"
)

// Labels.
const (
	LabelApp      = "app"
	LabelProvider = "provider"
	LabelVM       = "vmID"
	AppForklift   = "forklift"
)

// ApplianceName returns the stable CopyAppliance name for a migration VM.
func ApplianceName(migrationUID types.UID, vmID string) string {
	sum := sha256.Sum256([]byte(vmID))
	vmShort := hex.EncodeToString(sum[:4])
	migShort := string(migrationUID)
	if len(migShort) > 8 {
		migShort = migShort[:8]
	}
	return fmt.Sprintf("copy-appliance-%s-%s", migShort, vmShort)
}

// rootResourcePool is the name vSphere gives the root resource pool of every
// cluster and standalone host.
const rootResourcePool = "Resources"

// checkNameSuffix is appended to a provider's name to name its check appliance.
const checkNameSuffix = "-toehold-check"

// maxVMNameLength is what vSphere accepts as a virtual machine name. A built
// appliance's own name is what its VM is cloned as, so a CopyAppliance cannot
// be named anything vCenter would refuse.
const maxVMNameLength = 80

// Build returns a CopyAppliance for an appliance VM that will read the given
// source VM's disks. Everything about the appliance comes from the toehold
// template it is cloned from — where it is placed, its shape, its root disk
// and its network — except the disks to attach, which are the source VM's. The
// returned resource has not been created; the caller creates it and the
// reconciler builds the VM from it.
func Build(provider *api.Provider, toehold *api.ToeholdTemplate, vmRef ref.Ref) (appliance *api.CopyAppliance, err error) {
	inventory, err := web.NewClient(provider)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	return build(inventory, provider, toehold, vmRef)
}

// build is Build over an already-resolved inventory client. An empty vmRef
// builds an appliance with no source VM, and so with nothing to attach.
func build(inventory web.Client, provider *api.Provider, toehold *api.ToeholdTemplate, vmRef ref.Ref) (appliance *api.CopyAppliance, err error) {
	if provider.Type() != api.VSphere {
		err = liberr.New(fmt.Sprintf(
			"copy appliances are only supported for vSphere providers; %s is %s",
			provider.Name, provider.Type()))
		return
	}
	if Settings.CopyAppliance.ContainerImage == "" {
		// Also deployment-specific: it names the nbd-container FQIN (or an
		// ImageStreamTag in this cluster). Without it the appliance boots with
		// nothing to serve exports with.
		err = liberr.New(
			"the copy appliance container image is not configured; set " +
				settings.CopyApplianceContainerImage)
		return
	}
	if provider.Status.ToeholdSSHPrivateSecret == "" {
		err = liberr.New(
			"provider has no toehold SSH private secret yet",
			"provider", provider.Name)
		return
	}
	spec := api.CopyApplianceSpec{
		Provider: core.ObjectReference{
			Namespace: provider.Namespace,
			Name:      provider.Name,
		},
		Secret: core.ObjectReference{
			Namespace: provider.Namespace,
			Name:      provider.Status.ToeholdSSHPrivateSecret,
		},
		ContainerImage: Settings.CopyAppliance.ContainerImage,
		// The appliance's shape, root disk and network are not configured here,
		// and the network is not the source VM's: the template carries all of
		// them, and the clone inherits them. Placement below comes from the
		// template as well.
	}
	err = placement(inventory, provider, toehold, &spec)
	if err != nil {
		return
	}

	labels := map[string]string{
		LabelApp:      AppForklift,
		LabelProvider: string(provider.UID),
	}
	if vmRef.ID != "" {
		err = attachDisks(inventory, vmRef, &spec)
		if err != nil {
			return
		}
		labels[LabelVM] = vmRef.ID
	}

	appliance = &api.CopyAppliance{
		ObjectMeta: meta.ObjectMeta{
			Namespace: provider.Namespace,
			Labels:    labels,
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
func buildCheck(inventory web.Client, provider *api.Provider, toehold *api.ToeholdTemplate) (appliance *api.CopyAppliance, err error) {
	appliance, err = build(inventory, provider, toehold, ref.Ref{})
	if err != nil {
		return
	}
	// Named rather than generated: there is one check appliance per provider,
	// and the next pass has to find this one rather than create another.
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

// placement fills in the placement fields of spec from where the toehold
// template VM lives. The appliance is cloned into the template's own folder,
// datacenter and datastore, which is the one placement known to work for a VM
// of this shape. Every value is an inventory Path in the form the govmomi
// finder expects.
//
// The template is resolved by moref rather than by the path recorded in
// toehold.Spec: the inventory serves templates through the get-by-moref
// handler and filters them out of its listings, so a path lookup finds
// nothing.
func placement(inventory web.Client, provider *api.Provider, toehold *api.ToeholdTemplate, spec *api.CopyApplianceSpec) (err error) {
	moRef := toehold.Status.Template.Moref
	if moRef == "" {
		err = liberr.New(fmt.Sprintf(
			"toehold template %s has no moref to place the appliance from",
			toehold.Name))
		return
	}
	vm := &model.VM{}
	err = inventory.Find(vm, ref.Ref{ID: moRef})
	if err != nil {
		err = liberr.Wrap(err, "template", moRef)
		return
	}
	spec.Template = vm.Path

	if vm.Parent.Kind != vspheremodel.FolderKind {
		err = liberr.New(fmt.Sprintf(
			"VM %s is not in an inventory folder; its parent is a %s",
			vm.ID, vm.Parent.Kind))
		return
	}
	folder := &model.Folder{}
	if err = inventory.Get(folder, vm.Parent.ID); err != nil {
		return
	}
	spec.Folder = folder.Path

	if folder.Datacenter == "" {
		err = liberr.New(fmt.Sprintf("folder %s is not under a datacenter", folder.ID))
		return
	}
	datacenter := &model.Datacenter{}
	if err = inventory.Get(datacenter, folder.Datacenter); err != nil {
		return
	}
	spec.Datacenter = datacenter.Path

	// Host is only used to find the compute resource's root pool.
	host := &model.Host{}
	if err = inventory.Get(host, vm.Host); err != nil {
		return
	}
	cluster := &model.Cluster{}
	if err = inventory.Get(cluster, host.Cluster); err != nil {
		return
	}
	if pool := provider.Setting(api.CopyApplianceResourcePool); pool != "" {
		spec.ResourcePool = pool
	} else {
		spec.ResourcePool = cluster.Path + "/" + rootResourcePool
	}

	if len(vm.Disks) == 0 {
		err = liberr.New(fmt.Sprintf("VM %s has no disks to take a datastore from", vm.ID))
		return
	}
	datastore := &model.Datastore{}
	if err = inventory.Get(datastore, vm.Disks[0].Datastore.ID); err != nil {
		return
	}
	spec.Datastore = datastore.Path
	return
}

// attachDisks fills in the disks of spec from the source VM. This is all the
// appliance takes from the VM it serves; where it runs is the template's
// business. Shared and RDM disks are skipped; the copy appliance targets flat
// VMDKs.
func attachDisks(inventory web.Client, vmRef ref.Ref, spec *api.CopyApplianceSpec) (err error) {
	vm := &model.VM{}
	err = inventory.Find(vm, vmRef)
	if err != nil {
		err = liberr.Wrap(err, "vm", vmRef.String())
		return
	}
	for _, disk := range vm.Disks {
		if disk.Shared || disk.RDM || disk.File == "" {
			continue
		}
		spec.AttachDisks = append(spec.AttachDisks, api.AttachedDisk{
			VMDKPath: disk.File,
			DiskKey:  disk.Key,
			Serial:   disk.Serial,
			Capacity: disk.Capacity,
		})
	}
	return
}
