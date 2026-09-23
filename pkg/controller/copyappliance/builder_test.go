package copyappliance

import (
	"fmt"
	"strings"
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	"github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1/ref"
	vspheremodel "github.com/kubev2v/forklift/pkg/controller/provider/model/vsphere"
	"github.com/kubev2v/forklift/pkg/controller/provider/web"
	model "github.com/kubev2v/forklift/pkg/controller/provider/web/vsphere"
	"github.com/kubev2v/forklift/pkg/settings"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// fakeInventory serves an inventory from in-memory maps. Only Find and Get are
// implemented; the embedded interface is nil, so a lookup through any other
// method of web.Client panics rather than quietly returning a zero value.
type fakeInventory struct {
	web.Client
	vm          model.VM
	folders     map[string]model.Folder
	datacenters map[string]model.Datacenter
	hosts       map[string]model.Host
	clusters    map[string]model.Cluster
	datastores  map[string]model.Datastore
	// There is deliberately no network map. Placement does not resolve
	// networks, and Get rejects the attempt, so a regression to taking the
	// appliance's network from the source VM fails rather than passes.
}

func (r *fakeInventory) Find(resource interface{}, _ ref.Ref) error {
	out, ok := resource.(*model.VM)
	if !ok {
		return fmt.Errorf("unexpected Find for %T", resource)
	}
	*out = r.vm
	return nil
}

func (r *fakeInventory) Get(resource interface{}, id string) error {
	switch out := resource.(type) {
	case *model.Folder:
		return lookup(r.folders, id, out, "folder")
	case *model.Datacenter:
		return lookup(r.datacenters, id, out, "datacenter")
	case *model.Host:
		return lookup(r.hosts, id, out, "host")
	case *model.Cluster:
		return lookup(r.clusters, id, out, "cluster")
	case *model.Datastore:
		return lookup(r.datastores, id, out, "datastore")
	}
	return fmt.Errorf("unexpected Get for %T", resource)
}

func lookup[T any](from map[string]T, id string, out *T, kind string) error {
	found, ok := from[id]
	if !ok {
		return fmt.Errorf("%s %q not found", kind, id)
	}
	*out = found
	return nil
}

// testRef identifies the source VM. The fake ignores it: which VM is returned
// is decided by the fixture, not by the ref.
var testRef = ref.Ref{ID: "vm-101", Name: "web-01"}

// testInventory is a VM on a clustered host, two folders deep, with one disk.
// Every path is what PathBuilder would produce for that topology, hidden
// vm/host/network/datastore folders included.
func testInventory() *fakeInventory {
	resource := func(id, path string) model.Resource {
		return model.Resource{ID: id, Path: path}
	}
	return &fakeInventory{
		vm: model.VM{
			VM1: model.VM1{
				VM0:  resource("vm-101", "/DC0/vm/apps/web-01"),
				Host: "host-1",
				Disks: []vspheremodel.Disk{
					{
						Key:       2000,
						File:      "[datastore1] web-01/disk-0.vmdk",
						Serial:    "6000C297-7d53-fad7-e8b4-5194193802f7",
						Capacity:  16 << 30,
						Datastore: vspheremodel.Ref{Kind: vspheremodel.DsKind, ID: "ds-1"},
					},
				},
			},
			// The source VM's own NIC. Nothing reads it; it is here so that
			// a build taking its network from the source VM would be caught.
			NICs: []vspheremodel.NIC{
				{Network: vspheremodel.Ref{Kind: vspheremodel.NetKind, ID: "net-1"}, Index: 0},
			},
		},
		folders: map[string]model.Folder{
			"folder-apps": {
				Resource: resource("folder-apps", "/DC0/vm/apps"),
				Folder:   "folder-vm",
			},
			"folder-vm": {
				Resource:   resource("folder-vm", "/DC0/vm"),
				Datacenter: "dc-1",
			},
		},
		datacenters: map[string]model.Datacenter{
			"dc-1": {Resource: resource("dc-1", "/DC0")},
		},
		hosts: map[string]model.Host{
			"host-1": {
				Resource: resource("host-1", "/DC0/host/Cluster0/esx1.example.com"),
				Cluster:  "cluster-1",
			},
		},
		clusters: map[string]model.Cluster{
			"cluster-1": {Resource: resource("cluster-1", "/DC0/host/Cluster0")},
		},
		datastores: map[string]model.Datastore{
			"ds-1": {Resource: resource("ds-1", "/DC0/datastore/datastore1")},
		},
	}
}

// vmParent sets the VM's parent, which is how the folder is found.
func (r *fakeInventory) vmParent(kind, id string) *fakeInventory {
	r.vm.Parent = vspheremodel.Ref{Kind: kind, ID: id}
	return r
}

func TestPlacementFollowsTheSourceVM(t *testing.T) {
	inventory := testInventory().vmParent(vspheremodel.FolderKind, "folder-apps")
	spec := api.CopyApplianceSpec{}
	if err := placement(inventory, testProvider(), testRef, &spec); err != nil {
		t.Fatalf("placement: %v", err)
	}
	tests := []struct {
		field string
		got   string
		want  string
	}{
		{"Folder", spec.Folder, "/DC0/vm/apps"},
		{"Datacenter", spec.Datacenter, "/DC0"},
		// The appliance is not placed on the source VM's host; the host is
		// only how its compute resource, and so the pool below, is found.
		{"ResourcePool", spec.ResourcePool, "/DC0/host/Cluster0/Resources"},
		{"Datastore", spec.Datastore, "/DC0/datastore/datastore1"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}
}

// Only the folder directly beneath a datacenter records it, so a VM nested any
// deeper is found by walking up.
func TestPlacementWalksUpToTheDatacenter(t *testing.T) {
	inventory := testInventory().vmParent(vspheremodel.FolderKind, "folder-team")
	inventory.folders["folder-team"] = model.Folder{
		Resource: model.Resource{ID: "folder-team", Path: "/DC0/vm/apps/team"},
		Folder:   "folder-apps",
	}
	spec := api.CopyApplianceSpec{}
	if err := placement(inventory, testProvider(), testRef, &spec); err != nil {
		t.Fatalf("placement: %v", err)
	}
	if spec.Folder != "/DC0/vm/apps/team" {
		t.Errorf("Folder = %q, want the VM's own folder", spec.Folder)
	}
	if spec.Datacenter != "/DC0" {
		t.Errorf("Datacenter = %q, want /DC0", spec.Datacenter)
	}
}

// A datacenter can itself sit in a folder, which is why the datacenter is
// resolved rather than read off the first segment of a path.
func TestPlacementDatacenterInAFolder(t *testing.T) {
	inventory := testInventory().vmParent(vspheremodel.FolderKind, "folder-apps")
	inventory.datacenters["dc-1"] = model.Datacenter{
		Resource: model.Resource{ID: "dc-1", Path: "/east/DC0"},
	}
	spec := api.CopyApplianceSpec{}
	if err := placement(inventory, testProvider(), testRef, &spec); err != nil {
		t.Fatalf("placement: %v", err)
	}
	if spec.Datacenter != "/east/DC0" {
		t.Errorf("Datacenter = %q, want /east/DC0", spec.Datacenter)
	}
}

// A host outside a cluster is collected as a Cluster with the ComputeResource
// variant, so it has a root resource pool like any other.
func TestPlacementStandaloneHost(t *testing.T) {
	inventory := testInventory().vmParent(vspheremodel.FolderKind, "folder-apps")
	inventory.hosts["host-1"] = model.Host{
		Resource: model.Resource{ID: "host-1", Path: "/DC0/host/esx1.example.com/esx1.example.com"},
		Cluster:  "cr-1",
	}
	inventory.clusters["cr-1"] = model.Cluster{
		Resource: model.Resource{
			ID:      "cr-1",
			Variant: vspheremodel.ComputeResource,
			Path:    "/DC0/host/esx1.example.com",
		},
	}
	spec := api.CopyApplianceSpec{}
	if err := placement(inventory, testProvider(), testRef, &spec); err != nil {
		t.Fatalf("placement: %v", err)
	}
	if spec.ResourcePool != "/DC0/host/esx1.example.com/Resources" {
		t.Errorf("ResourcePool = %q, want the compute resource's root pool", spec.ResourcePool)
	}
}

func TestPlacementErrors(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*fakeInventory)
		want  string
	}{
		{
			name:  "VM in a vApp rather than a folder",
			setup: func(i *fakeInventory) { i.vmParent("VirtualApp", "vapp-1") },
			want:  "not in an inventory folder",
		},
		{
			name:  "no disks",
			setup: func(i *fakeInventory) { i.vm.Disks = nil },
			want:  "no disks",
		},
		{
			// Nothing is placed on the host, but the resource pool is found
			// through it, so a VM without one still has nowhere to put the
			// appliance.
			name:  "no host",
			setup: func(i *fakeInventory) { i.vm.Host = "" },
			want:  "no host",
		},
		{
			name: "folder with no path",
			setup: func(i *fakeInventory) {
				i.folders["folder-apps"] = model.Folder{
					Resource: model.Resource{ID: "folder-apps"},
					Folder:   "folder-vm",
				}
			},
			want: "no path for the folder",
		},
		{
			name: "cluster with no path",
			setup: func(i *fakeInventory) {
				i.clusters["cluster-1"] = model.Cluster{
					Resource: model.Resource{ID: "cluster-1"},
				}
			},
			want: "no path for the cluster",
		},
		{
			name: "folder chain never reaches a datacenter",
			setup: func(i *fakeInventory) {
				i.folders["folder-vm"] = model.Folder{
					Resource: model.Resource{ID: "folder-vm", Path: "/DC0/vm"},
				}
			},
			want: "not under a datacenter",
		},
		{
			name: "folder chain loops",
			setup: func(i *fakeInventory) {
				i.folders["folder-vm"] = model.Folder{
					Resource: model.Resource{ID: "folder-vm", Path: "/DC0/vm"},
					Folder:   "folder-apps",
				}
			},
			want: "gave up walking",
		},
		{
			name:  "datastore not in the inventory",
			setup: func(i *fakeInventory) { delete(i.datastores, "ds-1") },
			want:  "not found",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inventory := testInventory().vmParent(vspheremodel.FolderKind, "folder-apps")
			tc.setup(inventory)
			spec := api.CopyApplianceSpec{}
			err := placement(inventory, testProvider(), testRef, &spec)
			if err == nil {
				t.Fatalf("placement succeeded, want an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("placement error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func testProvider() *api.Provider {
	vsphere := api.VSphere
	return &api.Provider{
		ObjectMeta: meta.ObjectMeta{
			Name:      "vcenter",
			Namespace: "forklift",
			UID:       types.UID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		},
		Spec: api.ProviderSpec{Type: &vsphere},
		Status: api.ProviderStatus{
			ToeholdSSHPrivateSecret: "toehold-ssh-keys-vcenter-private",
			ToeholdSSHPublicSecret:  "toehold-ssh-keys-vcenter-public",
		},
	}
}

// withSettings installs appliance settings for the duration of a test.
func withSettings(t *testing.T, applied settings.CopyAppliance) {
	t.Helper()
	previous := Settings.CopyAppliance
	Settings.CopyAppliance = applied
	t.Cleanup(func() { Settings.CopyAppliance = previous })
}

func testSettings() settings.CopyAppliance {
	return settings.CopyAppliance{
		SSHUser:        "root",
		ContainerImage: "copy-appliance:latest",
	}
}

func TestBuild(t *testing.T) {
	withSettings(t, testSettings())
	inventory := testInventory().vmParent(vspheremodel.FolderKind, "folder-apps")
	provider := testProvider()

	appliance, err := build(inventory, provider, testRef)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if appliance.Namespace != "forklift" {
		t.Errorf("Namespace = %q, want the provider's", appliance.Namespace)
	}
	// Callers set metadata.name; the appliance VM is cloned under that name.
	if appliance.Name != "" {
		t.Errorf("Name = %q, want the caller to set it", appliance.Name)
	}
	if appliance.GenerateName != "" {
		t.Errorf("GenerateName = %q, want empty", appliance.GenerateName)
	}
	wantLabels := map[string]string{
		LabelApp:      AppForklift,
		LabelProvider: string(provider.UID),
		LabelVM:       testRef.ID,
	}
	for key, want := range wantLabels {
		if got := appliance.Labels[key]; got != want {
			t.Errorf("label %q = %q, want %q", key, got, want)
		}
	}

	spec := appliance.Spec
	if spec.Provider.Namespace != "forklift" || spec.Provider.Name != "vcenter" {
		t.Errorf("Provider = %v, want forklift/vcenter", spec.Provider)
	}
	if spec.ContainerImage != testSettings().ContainerImage {
		t.Errorf("ContainerImage = %q, want it from settings", spec.ContainerImage)
	}
	// The toehold template build injects the matching public key; the private
	// half and the certificates live with the provider.
	if spec.Secret.Namespace != "forklift" || spec.Secret.Name != "toehold-ssh-keys-vcenter-private" {
		t.Errorf("Secret = %v, want the toehold private secret in the provider namespace", spec.Secret)
	}
	if len(spec.AttachDisks) != 1 {
		t.Fatalf("AttachDisks = %+v, want one disk from inventory", spec.AttachDisks)
	}
	if spec.AttachDisks[0].VMDKPath != "[datastore1] web-01/disk-0.vmdk" {
		t.Errorf("AttachDisks[0].VMDKPath = %q", spec.AttachDisks[0].VMDKPath)
	}
	if spec.AttachDisks[0].Serial != "6000C297-7d53-fad7-e8b4-5194193802f7" {
		t.Errorf("AttachDisks[0].Serial = %q", spec.AttachDisks[0].Serial)
	}
	if spec.Folder != "/DC0/vm/apps" || spec.Datastore != "/DC0/datastore/datastore1" {
		t.Errorf("placement = (%q, %q), want the source VM's", spec.Folder, spec.Datastore)
	}
}

// Without an image the appliance is cloned, configured, and then has nothing
// to serve exports with.
func TestBuildRejectsAnUnconfiguredContainerImage(t *testing.T) {
	applied := testSettings()
	applied.ContainerImage = ""
	withSettings(t, applied)
	inventory := testInventory().vmParent(vspheremodel.FolderKind, "folder-apps")

	_, err := build(inventory, testProvider(), testRef)
	if err == nil {
		t.Fatal("build succeeded without a container image")
	}
	if !strings.Contains(err.Error(), settings.CopyApplianceContainerImage) {
		t.Errorf("error = %q, want it to name %s", err, settings.CopyApplianceContainerImage)
	}
}

func TestBuildRejectsANonVSphereProvider(t *testing.T) {
	withSettings(t, testSettings())
	inventory := testInventory().vmParent(vspheremodel.FolderKind, "folder-apps")
	provider := testProvider()
	ovirt := api.OVirt
	provider.Spec.Type = &ovirt

	_, err := build(inventory, provider, testRef)
	if err == nil {
		t.Fatal("build succeeded for an oVirt provider")
	}
	if !strings.Contains(err.Error(), "only supported for vSphere") {
		t.Errorf("error = %q, want it to say vSphere only", err)
	}
}
