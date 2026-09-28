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
	"github.com/kubev2v/forklift/pkg/labeler"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/kubev2v/forklift/pkg/settings"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Labels. An appliance is identified by its labels rather than by its name,
// which is generated. LabelMigration and LabelVM repeat the keys the plan
// package declares as convctx.LabelMigration and convctx.LabelVM; the values
// have to agree, and plan imports this package rather than the reverse.
const (
	LabelApp       = "app"
	LabelSubapp    = "subapp"
	LabelProvider  = "provider"
	LabelMigration = "migration"
	LabelVM        = "vmID"
	AppForklift    = "forklift"

	SubappAppliance = "copy-appliance"
	SubappCheck     = "copy-appliance-check"
)

type Labeler struct {
	labeler.Labeler
}

// ApplianceLabels identify the appliance serving one VM of one migration.
func (r *Labeler) ApplianceLabels(provider *api.Provider, migrationUID types.UID, vmID string) map[string]string {
	return map[string]string{
		LabelApp:       AppForklift,
		LabelSubapp:    SubappAppliance,
		LabelProvider:  string(provider.UID),
		LabelMigration: string(migrationUID),
		LabelVM:        vmID,
	}
}

// CheckLabels identify a provider's check appliance.
func (r *Labeler) CheckLabels(provider *api.Provider) map[string]string {
	return map[string]string{
		LabelApp:      AppForklift,
		LabelSubapp:   SubappCheck,
		LabelProvider: string(provider.UID),
	}
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

// generatedNameSuffixLength is what the API server appends to a GenerateName.
const generatedNameSuffixLength = 5

// maxPrefixLength is what a GenerateName may be and still leave a name vCenter
// accepts.
const maxPrefixLength = maxVMNameLength - generatedNameSuffixLength

// Builder builds CopyAppliance CRs from a provider's toehold template.
type Builder struct {
	Provider *api.Provider
	// Inventory reads the provider's inventory service, which placement and
	// disk attachment are resolved against.
	Inventory web.Client
	Labeler   Labeler
}

// NewBuilder returns a Builder over the provider's inventory service.
func NewBuilder(provider *api.Provider) (builder *Builder, err error) {
	inventory, err := web.NewClient(provider)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	builder = &Builder{
		Provider:  provider,
		Inventory: inventory,
	}
	return
}

// prefix is the generated name of the appliance serving one VM of a migration.
// The VM ID is hashed because it is a source-side identifier and is not
// guaranteed to be legal in a resource name.
func (r *Builder) prefix(migrationUID types.UID, vmID string) string {
	sum := sha256.Sum256([]byte(vmID))
	vmShort := hex.EncodeToString(sum[:4])
	migShort := string(migrationUID)
	if len(migShort) > 8 {
		migShort = migShort[:8]
	}
	return fmt.Sprintf("copy-appliance-%s-%s-", migShort, vmShort)
}

// checkPrefix is the generated name of the provider's check appliance.
func (r *Builder) checkPrefix() string {
	name := r.Provider.Name
	if len(name)+len(checkNameSuffix)+1 > maxPrefixLength {
		name = name[:maxPrefixLength-len(checkNameSuffix)-1]
	}
	return name + checkNameSuffix + "-"
}

// Appliance returns a CopyAppliance for an appliance VM that will read the
// given source VM's disks. Everything about the appliance comes from the
// toehold template it is cloned from — where it is placed, its shape, its root
// disk and its network — except the disks to attach, which are the source
// VM's. The returned resource has not been created; the caller creates it and
// the reconciler builds the VM from it.
func (r *Builder) Appliance(toehold *api.ToeholdTemplate, vmRef ref.Ref, migrationUID types.UID) (appliance *api.CopyAppliance, err error) {
	appliance, err = r.build(toehold)
	if err != nil {
		return
	}
	err = r.attachDisks(vmRef, &appliance.Spec)
	if err != nil {
		return
	}
	appliance.GenerateName = r.prefix(migrationUID, vmRef.ID)
	appliance.Labels = r.Labeler.ApplianceLabels(r.Provider, migrationUID, vmRef.ID)
	return
}

// Check returns a CopyAppliance for an appliance VM that reads no disks at
// all. Deploying one exercises everything the appliance path needs — the clone,
// the boot, the guest network, the login, the orchestrator install and the
// export endpoint — against nothing a migration depends on, so a provider can
// be told its appliance works before a plan relies on it.
func (r *Builder) Check(toehold *api.ToeholdTemplate) (appliance *api.CopyAppliance, err error) {
	appliance, err = r.build(toehold)
	if err != nil {
		return
	}
	appliance.GenerateName = r.checkPrefix()
	appliance.Labels = r.Labeler.CheckLabels(r.Provider)
	return
}

// build is what an appliance has regardless of which VM, if any, it serves.
// The caller names and labels it.
func (r *Builder) build(toehold *api.ToeholdTemplate) (appliance *api.CopyAppliance, err error) {
	provider := r.Provider
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
	err = r.placement(toehold, &spec)
	if err != nil {
		return
	}

	appliance = &api.CopyAppliance{
		ObjectMeta: meta.ObjectMeta{
			Namespace: provider.Namespace,
		},
		Spec: spec,
	}
	return
}

// placement fills in the placement fields of spec. The appliance is cloned
// into the template's own folder and datastore, which is the one placement
// known to work for a VM of this shape. Folder, datastore and the path of the
// template to clone are the ToeholdTemplate's own spec — the values the import
// was given. Datacenter and resource pool are not on that spec and are
// resolved from the inventory, in the Path form the govmomi finder expects.
//
// The template is resolved there by moref rather than by the path recorded in
// toehold.Spec: the inventory serves templates through the get-by-moref
// handler and filters them out of its listings, so a path lookup finds
// nothing.
func (r *Builder) placement(toehold *api.ToeholdTemplate, spec *api.CopyApplianceSpec) (err error) {
	inventory := r.Inventory
	spec.Folder = toehold.Spec.Folder
	spec.Datastore = toehold.Spec.Datastore
	spec.Template = path.Join(toehold.Spec.Folder, toehold.Spec.TemplateName)

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

	// The folder is only how the datacenter the template sits in is found.
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
	if pool := r.Provider.Setting(api.CopyApplianceResourcePool); pool != "" {
		spec.ResourcePool = pool
	} else {
		spec.ResourcePool = cluster.Path + "/" + rootResourcePool
	}
	return
}

// attachDisks fills in the disks of spec from the source VM. This is all the
// appliance takes from the VM it serves; where it runs is the template's
// business. Shared and RDM disks are skipped; the copy appliance targets flat
// VMDKs.
func (r *Builder) attachDisks(vmRef ref.Ref, spec *api.CopyApplianceSpec) (err error) {
	vm := &model.VM{}
	err = r.Inventory.Find(vm, vmRef)
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
