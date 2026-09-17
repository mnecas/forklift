package v1beta1

import (
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const CopyApplianceFinalizer = "forklift/copy-appliance"

// Export targets for CopyApplianceSpec.ExportRequest.
const (
	ExportTargetExport  = "Export"
	ExportTargetRelease = "Release"
)

// ExportRequest asks the controller to converge disk attachment and NBD exports.
// Increment generation on every change; the controller copies it to status when done.
type ExportRequest struct {
	// Export attaches disks and publishes exports. Release detaches disks and clears exports.
	// +kubebuilder:validation:Enum=Export;Release
	Target string `json:"target"`
	// Monotonic counter bumped by the consumer on every target change.
	Generation int64 `json:"generation"`
}

// CopyAppliance specification.
//
// The appliance VM is a clone of Template, named after the CopyAppliance
// itself, and that name is how an operator recognizes the VM in the vSphere
// inventory. The controller finds it again by the moRef recorded in the status.
type CopyApplianceSpec struct {
	// Source provider in which the appliance VM is created.
	Provider core.ObjectReference `json:"provider" ref:"Provider"`
	// Secret holding the SSH key pair the controller logs in to the appliance
	// with. The private key is read from the "private-key" data key; the
	// matching public key is expected to be installed in the appliance image
	// already.
	SSHKey core.ObjectReference `json:"sshKey" ref:"Secret"`
	// Secret holding the mutual-TLS material the appliance serves its exports
	// with. The controller installs the CA and the server half on the appliance
	// and keeps the client half to query the exports with. The data keys are the
	// file names the appliance expects: "ca-cert.pem", "server-cert.pem",
	// "server-key.pem", "client-cert.pem" and "client-key.pem".
	//
	// The server certificate must be issued for the logical name "nbd-server"
	// rather than for an address. The appliance is cloned on demand and its
	// address is not known when the certificate is issued, so the client
	// verifies the name instead of where it reached it.
	TLSSecret core.ObjectReference `json:"tlsSecret" ref:"Secret"`
	// ImageStreamTag naming the container image loaded into the appliance's
	// podman store, resolved in the controller's own namespace. The controller
	// reads the image from the cluster's internal registry and streams it to
	// the appliance over SSH, so the appliance needs no registry access of its
	// own.
	// +kubebuilder:validation:MinLength=1
	ContainerImage string `json:"containerImage"`
	// Datacenter in which the appliance VM is created.
	// +optional
	Datacenter string `json:"datacenter,omitempty"`
	// Datastore that holds the appliance VM home directory.
	Datastore string `json:"datastore"`
	// Resource pool in which the appliance VM is created. When omitted, the
	// controller uses ForkliftController.spec.copy_appliance_resource_pool.
	// +optional
	ResourcePool string `json:"resourcePool,omitempty"`
	// Inventory folder in which the appliance VM is created.
	Folder string `json:"folder"`
	// Disks to attach to the appliance VM for export. Each entry names an
	// existing VMDK and carries the VMware identifiers needed to correlate
	// guest exports with source inventory.
	// Capped so that the root disk plus the attached disks fit within the
	// four SCSI controllers vSphere permits per VM (4 x 15 addressable
	// units = 60 disks).
	// +kubebuilder:validation:MaxItems=59
	// +optional
	AttachDisks []AttachedDisk `json:"attachDisks,omitempty"`
	// Deprecated: use attachDisks instead.
	// +kubebuilder:validation:MaxItems=59
	// +kubebuilder:validation:items:Pattern=`^\[[^\]]+\]\s*.+\.vmdk$`
	// +optional
	AttachDiskPaths []string `json:"attachDiskPaths,omitempty"`
	// Inventory path of the VM template the appliance is cloned from. The
	// template supplies the root disk, so it must support the controller its
	// disks are attached to, and the one network the appliance is reached on,
	// which the clone inherits as-is.
	// +kubebuilder:validation:MinLength=1
	Template string `json:"template"`
	// ExportRequest asks the controller to attach or release source disks and
	// refresh NBD exports on an already-deployed appliance.
	// +optional
	ExportRequest *ExportRequest `json:"exportRequest,omitempty"`
}

// AttachedDisk is an existing VMDK to attach to the copy appliance.
type AttachedDisk struct {
	// Datastore path of the VMDK (e.g. "[datastore13] some-vm/disk-0.vmdk").
	// +kubebuilder:validation:Pattern=`^\[[^\]]+\]\s*.+\.vmdk$`
	VMDKPath string `json:"vmdkPath"`
	// VMware virtual device key from inventory.
	// +optional
	DiskKey int32 `json:"diskKey,omitempty"`
	// backing.Uuid from inventory. Used to match guest exports.
	// +optional
	Serial string `json:"serial,omitempty"`
	// Disk capacity in bytes. Used as a secondary match key when serial is empty.
	// +optional
	Capacity int64 `json:"capacity,omitempty"`
}

// AttachedDisks returns the disks to attach, synthesizing attachDisks entries
// from the deprecated attachDiskPaths field when needed.
func (s CopyApplianceSpec) AttachedDisks() []AttachedDisk {
	if len(s.AttachDisks) > 0 {
		return s.AttachDisks
	}
	disks := make([]AttachedDisk, 0, len(s.AttachDiskPaths))
	for _, path := range s.AttachDiskPaths {
		disks = append(disks, AttachedDisk{VMDKPath: path})
	}
	return disks
}

// ApplianceAddress is an address the appliance VM's guest reports on its
// network adapter. The template gives the appliance one network, and an adapter
// can hold more than one address on it.
type ApplianceAddress struct {
	// Name of the portgroup the guest reports the adapter is attached to.
	Network string `json:"network"`
	// MAC address of the adapter.
	MAC string `json:"mac"`
	// IP address.
	IP string `json:"ip"`
}

// ApplianceExport is one disk the appliance publishes over NBD.
type ApplianceExport struct {
	// Stable identifier the appliance's guest resolved for the disk. It falls
	// back to the device path when the guest can report nothing better.
	WWID string `json:"wwid"`
	// Port on the appliance the export is served on.
	Port int32 `json:"port"`
	// Device node the export reads, as the appliance's guest sees it.
	Device string `json:"device"`
	// VMware virtual device key of the attached source disk.
	// +optional
	DiskKey int32 `json:"diskKey,omitempty"`
	// Datastore path of the attached source VMDK.
	// +optional
	VMDKPath string `json:"vmdkPath,omitempty"`
	// backing.Uuid of the attached source disk from inventory.
	// +optional
	SourceSerial string `json:"sourceSerial,omitempty"`
}

// CopyAppliance status.
type CopyApplianceStatus struct {
	// Conditions.
	libcnd.Conditions `json:",inline"`
	// The most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// The managed object reference ID of the created appliance VM.
	// +optional
	MoRef string `json:"moRef,omitempty"`
	// The addresses the appliance VM's guest reports on its network, one entry
	// per address, in the order the guest reports them. Empty until the guest
	// has booted far enough to answer.
	// +optional
	Addresses []ApplianceAddress `json:"addresses,omitempty"`
	// The instance UUID of the vCenter the appliance VM was created in. A
	// managed object reference is only unique within one vCenter, so MoRef
	// must not be trusted when this does not match the connected instance.
	// +optional
	VCenterInstanceUUID string `json:"vcenterInstanceUUID,omitempty"`
	// The reference the container image is loaded under in the appliance's
	// podman store. It carries the digest of the image that was loaded, so an
	// appliance holding an earlier build of the same tag does not match.
	// +optional
	LoadedImage string `json:"loadedImage,omitempty"`
	// The disk exports the appliance publishes, one per attached disk. Empty
	// until the appliance is serving all of them.
	// +optional
	Exports []ApplianceExport `json:"exports,omitempty"`
	// The step of the deploy or teardown itinerary the appliance has reached.
	// Every phase here is one the appliance can actually be observed in: a
	// step that need not wait on vSphere is passed through within a single
	// reconcile. The terminal phases are DeployCompleted, DeployFailed,
	// TeardownCompleted and TeardownFailed.
	// +optional
	Phase string `json:"phase,omitempty"`
	// The managed object reference ID of the vSphere task the current phase is
	// waiting on. Empty when the phase has nothing outstanding.
	// +optional
	TaskRef string `json:"taskRef,omitempty"`
	// ExportRequest the controller has converged to.
	// +optional
	ObservedExportRequest *ExportRequest `json:"observedExportRequest,omitempty"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// CopyAppliance is the Schema for API which causes copy appliances to be created
// in source hypervisors to facilitate disk transfer.
// +k8s:openapi-gen=true
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
type CopyAppliance struct {
	meta.TypeMeta   `json:",inline"`
	meta.ObjectMeta `json:"metadata,omitempty"`
	Spec            CopyApplianceSpec   `json:"spec,omitempty"`
	Status          CopyApplianceStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// CopyApplianceList contains a list of CopyAppliances
type CopyApplianceList struct {
	meta.TypeMeta `json:",inline"`
	meta.ListMeta `json:"metadata,omitempty"`
	Items         []CopyAppliance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CopyAppliance{}, &CopyApplianceList{})
}
