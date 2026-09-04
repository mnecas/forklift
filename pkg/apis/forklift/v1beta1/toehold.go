package v1beta1

import (
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const ToeholdFinalizer = "forklift/toehold"

// ToeholdPhase is the high-level lifecycle state of a Toehold resource.
type ToeholdPhase string

const (
	ToeholdPhasePending   ToeholdPhase = "Pending"
	ToeholdPhaseRunning   ToeholdPhase = "Running"
	ToeholdPhaseSucceeded ToeholdPhase = "Succeeded"
	ToeholdPhaseFailed    ToeholdPhase = "Failed"
)

// ToeholdStage is the fine-grained pipeline position within the Running phase.
type ToeholdStage string

const (
	StageEnsurePrerequisites ToeholdStage = "EnsurePrerequisites"
	StageEnsureTemplate      ToeholdStage = "EnsureTemplate"
	StageBuildAndUpload      ToeholdStage = "BuildAndUpload"
	StageEnsureVM            ToeholdStage = "EnsureVM"
	StageCloneVM             ToeholdStage = "CloneVM"
	StageConfigureVM         ToeholdStage = "ConfigureVM"
	StageAttachDisks         ToeholdStage = "AttachDisks"
	StageStartNBD            ToeholdStage = "StartNBD"
	StageVerifyNBD           ToeholdStage = "VerifyNBD"
	StageToeholdFinished     ToeholdStage = "Finished"
)

// Condition types set on Toehold status.
const (
	ToeholdFailed          = "ToeholdFailed"
	ToeholdTemplateUpToDate = "TemplateUpToDate"
	ToeholdVMUpToDate      = "VMUpToDate"
	ToeholdRebuildRequired = "RebuildRequired"
)

// ToeholdResources defines CPU and memory for the OVF descriptor.
type ToeholdResources struct {
	// +optional
	// +kubebuilder:default:=2
	CPU int32 `json:"cpu,omitempty"`
	// +optional
	// +kubebuilder:default:=4096
	MemoryMiB int32 `json:"memoryMiB,omitempty"`
}

// ToeholdDiskSpec describes a migration disk to attach to the appliance and export over NBD.
type ToeholdDiskSpec struct {
	// Logical name used in status and downstream CDI references.
	Name string `json:"name"`
	// Source VM inventory name.
	SourceVM string `json:"sourceVM"`
	// Optional folder for sourceVM when different from spec.folder.
	// +optional
	SourceFolder string `json:"sourceFolder,omitempty"`
	// Path to an existing VMDK backing file, e.g. [datastore1] vm/vm-000001.vmdk.
	VMDKPath string `json:"vmdkPath"`
	// Optional disk capacity in GiB when vCenter cannot infer it from the source VM.
	// +optional
	CapacityGiB int64 `json:"capacityGiB,omitempty"`
	// Disk mode for the attached disk.
	// +optional
	// +kubebuilder:default:=independent_nonpersistent
	DiskMode string `json:"diskMode,omitempty"`
	// NBD listen port on the appliance guest. Defaults to nbd.basePort + disk index.
	// +optional
	Port *int32 `json:"port,omitempty"`
}

// ToeholdNBDConfig configures nbdkit exports on the appliance guest.
type ToeholdNBDConfig struct {
	// Base port for the first disk export.
	// +optional
	// +kubebuilder:default:=10809
	BasePort int32 `json:"basePort,omitempty"`
	// nbdkit thread count per export.
	// +optional
	// +kubebuilder:default:=8
	Threads int32 `json:"threads,omitempty"`
	// SSH user for guest access.
	// +optional
	// +kubebuilder:default:=root
	SSHUser string `json:"sshUser,omitempty"`
}

// ToeholdImages overrides container images used by the build Job.
type ToeholdImages struct {
	// bootc-image-builder initContainer image.
	// +optional
	BootcImageBuilder string `json:"bootcImageBuilder,omitempty"`
	// toehold-uploader main container image.
	// +optional
	ToeholdUploader string `json:"toeholdUploader,omitempty"`
}

// ToeholdSpec defines the desired state of Toehold.
type ToeholdSpec struct {
	// Reference to a vSphere Provider.
	Provider core.ObjectReference `json:"provider"`
	// bootc container image to convert to a VMDK.
	BootcImage string `json:"bootcImage"`
	// vCenter template name after OVF import.
	TemplateName string `json:"templateName"`
	// Name of the cloned toehold VM.
	VMName string `json:"vmName"`
	// Target vCenter datastore.
	Datastore string `json:"datastore"`
	// vCenter folder path (e.g. /Datacenter/vm).
	Folder string `json:"folder"`
	// Port group for OVF network mapping.
	Network string `json:"network"`
	// Optional dockerconfigjson secret for private bootc image pulls.
	// +optional
	RegistrySecret *core.LocalObjectReference `json:"registrySecret,omitempty"`
	// Namespace where the build Job runs. Defaults to the CR namespace.
	// +optional
	TargetNamespace string `json:"targetNamespace,omitempty"`
	// Optional node selector for the bib Job pod.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Power on the VM after clone.
	// +optional
	// +kubebuilder:default:=true
	PowerOn *bool `json:"powerOn,omitempty"`
	// Skip reuse checks and always rebuild the template and reclone the VM.
	// +optional
	ForceRebuild bool `json:"forceRebuild,omitempty"`
	// Keep the vCenter template on Toehold deletion.
	// +optional
	// +kubebuilder:default:=true
	RetainTemplate *bool `json:"retainTemplate,omitempty"`
	// OVF hardware descriptor overrides.
	// +optional
	Resources ToeholdResources `json:"resources,omitempty"`
	// Container image overrides.
	// +optional
	Images ToeholdImages `json:"images,omitempty"`
	// Migration disks to attach to the appliance and export over NBD.
	// +optional
	Disks []ToeholdDiskSpec `json:"disks,omitempty"`
	// NBD export configuration for spec.disks.
	// +optional
	NBD ToeholdNBDConfig `json:"nbd,omitempty"`
}

// ToeholdTemplateStatus tracks the vCenter template artifact.
type ToeholdTemplateStatus struct {
	// True when an existing vCenter template was reused.
	// +optional
	Reused bool `json:"reused,omitempty"`
	// Content hash of the template artifact.
	// +optional
	ContentHash string `json:"contentHash,omitempty"`
	// bootcImage value at import time.
	// +optional
	BootcImage string `json:"bootcImage,omitempty"`
	// Resolved bootc image digest.
	// +optional
	BootcImageID string `json:"bootcImageID,omitempty"`
	// +optional
	ImportedAt *meta.Time `json:"importedAt,omitempty"`
	// vSphere managed object reference.
	// +optional
	Moref string `json:"moref,omitempty"`
}

// ToeholdNBDStatus tracks one attached disk and its NBD export endpoint.
type ToeholdNBDStatus struct {
	// Logical disk name from spec.
	// +optional
	Name string `json:"name,omitempty"`
	// Source VM inventory name.
	// +optional
	SourceVM string `json:"sourceVM,omitempty"`
	// Attached VMDK backing path.
	// +optional
	VMDKPath string `json:"vmdkPath,omitempty"`
	// Guest block device path, e.g. /dev/sdb.
	// +optional
	Device string `json:"device,omitempty"`
	// NBD listen port on the appliance guest.
	// +optional
	Port int32 `json:"port,omitempty"`
	// NBD URI clients should connect to, e.g. nbd://10.0.0.1:10809/.
	// +optional
	URI string `json:"uri,omitempty"`
	// True when the export is attached and nbdkit is listening.
	// +optional
	Ready bool `json:"ready,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
}

// ToeholdVMStatus tracks the cloned toehold VM.
type ToeholdVMStatus struct {
	// True when an existing VM was reused.
	// +optional
	Reused bool `json:"reused,omitempty"`
	// VM content hash.
	// +optional
	ContentHash string `json:"contentHash,omitempty"`
	// vSphere managed object reference.
	// +optional
	Moref string `json:"moref,omitempty"`
	// Guest IP when available.
	// +optional
	IP string `json:"ip,omitempty"`
}

// ToeholdStatus defines the observed state of Toehold.
type ToeholdStatus struct {
	libcnd.Conditions `json:",inline"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
	Phase ToeholdPhase `json:"phase,omitempty"`
	// +optional
	Stage ToeholdStage `json:"stage,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// Reference to the build Job when a rebuild ran.
	// +optional
	Job *core.ObjectReference `json:"job,omitempty"`
	// +optional
	Template ToeholdTemplateStatus `json:"template,omitempty"`
	// +optional
	VM ToeholdVMStatus `json:"vm,omitempty"`
	// NBD export endpoints when all disks are ready.
	// +optional
	NBD []ToeholdNBDStatus `json:"nbd,omitempty"`
	// +optional
	CompletionTime *meta.Time `json:"completionTime,omitempty"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:printcolumn:name="PHASE",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="STAGE",type=string,JSONPath=".status.stage"
// +kubebuilder:printcolumn:name="READY",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
type Toehold struct {
	meta.TypeMeta   `json:",inline"`
	meta.ObjectMeta `json:"metadata,omitempty"`
	Spec            ToeholdSpec   `json:"spec,omitempty"`
	Status          ToeholdStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ToeholdList struct {
	meta.TypeMeta `json:",inline"`
	meta.ListMeta `json:"metadata,omitempty"`
	Items         []Toehold `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Toehold{}, &ToeholdList{})
}

// PowerOnEnabled returns whether the VM should be powered on after clone.
func (s *ToeholdSpec) PowerOnEnabled() bool {
	if s.PowerOn == nil {
		return true
	}
	return *s.PowerOn
}

// RetainTemplateEnabled returns whether to keep the template on delete.
func (s *ToeholdSpec) RetainTemplateEnabled() bool {
	if s.RetainTemplate == nil {
		return true
	}
	return *s.RetainTemplate
}

// TargetNS returns the namespace for managed resources.
func (t *Toehold) TargetNS() string {
	if t.Spec.TargetNamespace != "" {
		return t.Spec.TargetNamespace
	}
	return t.Namespace
}

// ExportsDisks returns whether the toehold should attach disks and start NBD exports.
func (s *ToeholdSpec) ExportsDisks() bool {
	return len(s.Disks) > 0
}

// DiskModeOrDefault returns the disk mode for attach.
func (d *ToeholdDiskSpec) DiskModeOrDefault() string {
	if d.DiskMode != "" {
		return d.DiskMode
	}
	return "independent_nonpersistent"
}

// NBDBasePort returns the first NBD port when not overridden per disk.
func (s *ToeholdSpec) NBDBasePort() int32 {
	if s.NBD.BasePort > 0 {
		return s.NBD.BasePort
	}
	return 10809
}

// NBDThreads returns nbdkit thread count per export.
func (s *ToeholdSpec) NBDThreads() int32 {
	if s.NBD.Threads > 0 {
		return s.NBD.Threads
	}
	return 8
}

// NBDSSHUser returns the SSH user for guest operations.
func (s *ToeholdSpec) NBDSSHUser() string {
	if s.NBD.SSHUser != "" {
		return s.NBD.SSHUser
	}
	return "root"
}

// DiskPort returns the NBD port for a disk at the given index.
func (s *ToeholdSpec) DiskPort(index int, disk ToeholdDiskSpec) int32 {
	if disk.Port != nil {
		return *disk.Port
	}
	return s.NBDBasePort() + int32(index)
}
