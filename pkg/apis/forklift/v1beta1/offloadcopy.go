package v1beta1

import (
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Hook specification.
// Local hooks require spec.image (playbook is optional if the image runs without an injected playbook).
// AAP hooks require spec.aap (image/playbook omitted for execution).
// Whether the spec is valid for execution is enforced by the hook and plan controllers (not by CRD admission rules).
type OffloadSpec struct {
	SourceVolume core.ObjectReference `json:"sourceVolume"`
	// Service account.
	ServiceAccount string `json:"serviceAccount,omitempty"`
	// Image to run the hook workload (required for local hooks; omit for AAP hooks).
	// +optional
	Image string `json:"image,omitempty"`
	// Hook deadline in seconds.
	Deadline int64 `json:"deadline,omitempty"`
	// SecretRef is the name of the secret with the storage credentials for the plugin.
	// The secret should reside in the same namespace where the source provider is.
	SecretRef string `json:"secretRef"`
}

// Hook status.
type OffloadStatus struct {
	// Conditions.
	libcnd.Conditions `json:",inline"`
	// The most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	DestinationVolume core.ObjectReference `json:"destinationVolume"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Hook is the Schema for the hooks API
// +k8s:openapi-gen=true
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=".spec.image"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
type OffloadCopy struct {
	meta.TypeMeta   `json:",inline"`
	meta.ObjectMeta `json:"metadata,omitempty"`
	Spec            OffloadSpec   `json:"spec,omitempty"`
	Status          OffloadStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// HookList contains a list of MigHook
type OffloadCopyList struct {
	meta.TypeMeta `json:",inline"`
	meta.ListMeta `json:"metadata,omitempty"`
	Items         []OffloadCopy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Hook{}, &HookList{})
}
