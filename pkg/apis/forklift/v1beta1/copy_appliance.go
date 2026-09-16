package v1beta1

import (
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const CopyApplianceFinalizer = "forklift/copy-appliance"

// CopyAppliance specification.
//
// The appliance VM is a clone of Template, named after the CopyAppliance
// itself, and that name is how an operator recognizes the VM in the vSphere
// inventory. The controller finds it again by the moRef recorded in the status.
type CopyApplianceSpec struct {
	// Source provider in which the appliance VM is created.
	Provider core.ObjectReference `json:"provider" ref:"Provider"`
	// Guest OS identifier for the appliance VM (e.g. "otherGuest64").
	GuestId string `json:"guestId"`
	// Number of virtual CPUs for the appliance VM.
	// +kubebuilder:validation:Minimum=1
	NumCPUs int32 `json:"numCpus"`
	// Memory for the appliance VM, in MiB.
	// +kubebuilder:validation:Minimum=1
	MemoryMB int64 `json:"memoryMB"`
	// Datacenter in which the appliance VM is created.
	// +optional
	Datacenter string `json:"datacenter,omitempty"`
	// Datastore that holds the appliance VM home directory.
	Datastore string `json:"datastore"`
	// Resource pool in which the appliance VM is created.
	ResourcePool string `json:"resourcePool"`
	// Host on which the appliance VM is placed. When empty, placement is
	// left to DRS.
	// +optional
	Host string `json:"host,omitempty"`
	// Inventory folder in which the appliance VM is created.
	Folder string `json:"folder"`
	// Network the appliance VM is reached on, attached as its first NIC.
	// Either a standard portgroup or a distributed portgroup.
	// +kubebuilder:validation:MinLength=1
	ManagementNetwork string `json:"managementNetwork"`
	// Network the appliance VM moves disk data over, attached as its second
	// NIC. When empty the appliance is given only a management NIC.
	// +kubebuilder:validation:MinLength=1
	// +optional
	TransferNetwork string `json:"transferNetwork,omitempty"`
	// Datastore path of the existing root disk image vmdk to boot the
	// appliance from (e.g. "[datastore1] images/appliance-root.vmdk").
	// The image must support the paravirtual SCSI controller the appliance
	// VM is built with, or it will not find its boot disk.
	// +kubebuilder:validation:Pattern=`^\[[^\]]+\]\s*.+\.vmdk$`
	RootDiskPath string `json:"rootDiskPath"`
	// Datastore paths of existing vmdks (belonging to other VMs) to attach
	// to the appliance VM (e.g. "[datastore13] some-vm/disk-0.vmdk").
	// Capped so that the root disk plus the attached disks fit within the
	// four SCSI controllers vSphere permits per VM (4 x 15 addressable
	// units = 60 disks).
	// +kubebuilder:validation:MaxItems=59
	// +kubebuilder:validation:items:Pattern=`^\[[^\]]+\]\s*.+\.vmdk$`
	// +optional
	AttachDiskPaths []string `json:"attachDiskPaths,omitempty"`
	// Inventory path of the VM template the appliance is cloned from. The
	// template supplies the root disk, so it must support the controller its
	// disks are attached to.
	// +kubebuilder:validation:MinLength=1
	Template string `json:"template"`
}

// ApplianceAddress is an address the appliance VM's guest reports on one of its
// network adapters.
type ApplianceAddress struct {
	// Name of the portgroup the guest reports the adapter is attached to.
	Network string `json:"network"`
	// MAC address of the adapter.
	MAC string `json:"mac"`
	// IP address.
	IP string `json:"ip"`
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
	MoRef string `json:"moREF,omitempty"`
	// The addresses the appliance VM's guest reports, one entry per address
	// per adapter, in the order the guest reports them. Empty until the guest
	// has booted far enough to answer.
	// +optional
	Addresses []ApplianceAddress `json:"addresses,omitempty"`
	// The instance UUID of the vCenter the appliance VM was created in. A
	// managed object reference is only unique within one vCenter, so MoRef
	// must not be trusted when this does not match the connected instance.
	// +optional
	VCenterInstanceUUID string `json:"vcenterInstanceUUID,omitempty"`
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
	TaskRef string `json:"taskREF,omitempty"`
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
