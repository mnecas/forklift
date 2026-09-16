package v1beta1

import (
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const ToeholdTemplateFinalizer = "forklift/toehold-template"

// ToeholdTemplatePhase is the high-level lifecycle state of a ToeholdTemplate resource.
type ToeholdTemplatePhase string

const (
	ToeholdTemplatePhasePending   ToeholdTemplatePhase = "Pending"
	ToeholdTemplatePhaseRunning   ToeholdTemplatePhase = "Running"
	ToeholdTemplatePhaseSucceeded ToeholdTemplatePhase = "Succeeded"
	ToeholdTemplatePhaseFailed    ToeholdTemplatePhase = "Failed"
)

// ToeholdTemplateStage is the fine-grained pipeline position within the Running phase.
type ToeholdTemplateStage string

const (
	StageEnsurePrerequisites ToeholdTemplateStage = "EnsurePrerequisites"
	StageEnsureTemplate      ToeholdTemplateStage = "EnsureTemplate"
	StageBuildAndUpload      ToeholdTemplateStage = "BuildAndUpload"
	StageToeholdFinished     ToeholdTemplateStage = "Finished"
)

// Condition types set on ToeholdTemplate status.
const (
	ToeholdTemplateFailed          = "ToeholdTemplateFailed"
	ToeholdTemplateUpToDate        = "TemplateUpToDate"
	ToeholdTemplateRebuildRequired = "RebuildRequired"
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

// ToeholdBaseDisk configures the read-only containerdisk base image.
type ToeholdBaseDisk struct {
	// OCI image embedding the base qcow2 (KubeVirt containerdisk layout under /disk).
	ContainerImage string `json:"containerImage"`
	// Optional dockerconfigjson secret for pulling containerImage.
	// +optional
	ImagePullSecret *core.LocalObjectReference `json:"imagePullSecret,omitempty"`
	// emptyDir size limit for the qcow2 overlay workspace. Unset means no limit.
	// +optional
	WorkGiB int64 `json:"workGiB,omitempty"`
}

// ToeholdCustomize configures virt-customize on the overlay disk.
type ToeholdCustomize struct {
	// Root password set on the guest disk via virt-customize --root-password.
	// +optional
	RootPassword string `json:"rootPassword,omitempty"`
}

// ToeholdImages overrides container images used by the toehold template pipeline.
type ToeholdImages struct {
	// +optional
	ToeholdBuilder string `json:"toeholdBuilder,omitempty"`
}

// ToeholdTemplateSpec defines the desired state of ToeholdTemplate.
type ToeholdTemplateSpec struct {
	// Reference to a vSphere Provider.
	Provider core.ObjectReference `json:"provider"`
	// Base containerdisk image for overlay customization.
	BaseDisk ToeholdBaseDisk `json:"baseDisk"`
	// Optional virt-customize inputs applied to the overlay disk.
	// +optional
	Customize ToeholdCustomize `json:"customize,omitempty"`
	// vCenter template name after OVF import.
	TemplateName string `json:"templateName"`
	// Target vCenter datastore.
	Datastore string `json:"datastore"`
	// vCenter folder path (e.g. /Datacenter/vm).
	Folder string `json:"folder"`
	// Port group for OVF network mapping.
	Network string `json:"network"`
	// Namespace where the build pod runs. Defaults to the CR namespace.
	// +optional
	TargetNamespace string `json:"targetNamespace,omitempty"`
	// Optional node selector for the build pod.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Skip reuse checks and always rebuild the template.
	// +optional
	ForceRebuild bool `json:"forceRebuild,omitempty"`
	// Keep the vCenter template on ToeholdTemplate deletion.
	// +optional
	// +kubebuilder:default:=true
	RetainTemplate *bool `json:"retainTemplate,omitempty"`
	// OVF hardware descriptor overrides.
	// +optional
	Resources ToeholdResources `json:"resources,omitempty"`
	// Container image overrides.
	// +optional
	Images ToeholdImages `json:"images,omitempty"`
}

// TemplateStatus tracks the vCenter template artifact.
type TemplateStatus struct {
	// True when an existing vCenter template was reused.
	// +optional
	Reused bool `json:"reused,omitempty"`
	// Hash of the base containerdisk image.
	// +optional
	DiskHash string `json:"diskHash,omitempty"`
	// Hash of CPU/memory/network configuration.
	// +optional
	ConfigHash string `json:"configHash,omitempty"`
	// containerImage used for the base disk at build time.
	// +optional
	BaseContainerImage string `json:"baseContainerImage,omitempty"`
	// +optional
	ImportedAt *meta.Time `json:"importedAt,omitempty"`
	// vSphere managed object reference.
	// +optional
	Moref string `json:"moref,omitempty"`
}

// ToeholdTemplateStatus defines the observed state of ToeholdTemplate.
type ToeholdTemplateStatus struct {
	libcnd.Conditions `json:",inline"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
	Phase ToeholdTemplatePhase `json:"phase,omitempty"`
	// +optional
	Stage ToeholdTemplateStage `json:"stage,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// Reference to the build pod when a rebuild ran.
	// +optional
	BuildPod *core.ObjectReference `json:"buildPod,omitempty"`
	// +optional
	Template TemplateStatus `json:"template,omitempty"`
	// +optional
	CompletionTime *meta.Time `json:"completionTime,omitempty"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:resource:shortName=ttpl
// +kubebuilder:printcolumn:name="PHASE",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="STAGE",type=string,JSONPath=".status.stage"
// +kubebuilder:printcolumn:name="READY",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
type ToeholdTemplate struct {
	meta.TypeMeta   `json:",inline"`
	meta.ObjectMeta `json:"metadata,omitempty"`
	Spec            ToeholdTemplateSpec   `json:"spec,omitempty"`
	Status          ToeholdTemplateStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ToeholdTemplateList struct {
	meta.TypeMeta `json:",inline"`
	meta.ListMeta `json:"metadata,omitempty"`
	Items         []ToeholdTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ToeholdTemplate{}, &ToeholdTemplateList{})
}

// RetainTemplateEnabled returns whether to keep the template on delete.
func (s *ToeholdTemplateSpec) RetainTemplateEnabled() bool {
	if s.RetainTemplate == nil {
		return true
	}
	return *s.RetainTemplate
}

// TargetNS returns the namespace for managed resources.
func (t *ToeholdTemplate) TargetNS() string {
	if t.Spec.TargetNamespace != "" {
		return t.Spec.TargetNamespace
	}
	return t.Namespace
}
