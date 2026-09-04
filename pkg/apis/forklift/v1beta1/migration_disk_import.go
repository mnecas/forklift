package v1beta1

import (
	libcnd "github.com/kubev2v/forklift/pkg/lib/condition"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var MigrationDiskImportKind = "MigrationDiskImport"
var MigrationDiskImportResource = "migrationdiskimports"

// MigrationDiskImportPhase is the lifecycle phase of a MigrationDiskImport.
type MigrationDiskImportPhase string

const (
	MigrationDiskImportPending           MigrationDiskImportPhase = "Pending"
	MigrationDiskImportImportScheduled   MigrationDiskImportPhase = "ImportScheduled"
	MigrationDiskImportImportInProgress  MigrationDiskImportPhase = "ImportInProgress"
	MigrationDiskImportPaused            MigrationDiskImportPhase = "Paused"
	MigrationDiskImportSucceeded         MigrationDiskImportPhase = "Succeeded"
	MigrationDiskImportFailed            MigrationDiskImportPhase = "Failed"
)

// DiskTransferType selects the population backend.
// +kubebuilder:validation:Enum=VDDK;NFC;Toehold
type DiskTransferType string

const (
	TransferVDDK    DiskTransferType = "VDDK"
	TransferNFC     DiskTransferType = "NFC"
	TransferToehold DiskTransferType = "Toehold"
)

// DiskCheckpoint defines a stage in a warm migration import.
type DiskCheckpoint struct {
	// Previous is the identifier of the snapshot from the previous checkpoint.
	Previous string `json:"previous"`
	// Current is the identifier of the snapshot created for this checkpoint.
	Current string `json:"current"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:openapi-gen=true
// +kubebuilder:resource:shortName={mdi,mdis}
// +kubebuilder:subresource:status
type MigrationDiskImport struct {
	meta.TypeMeta   `json:",inline"`
	meta.ObjectMeta `json:"metadata,omitempty"`

	Spec   MigrationDiskImportSpec   `json:"spec"`
	Status MigrationDiskImportStatus `json:"status,omitempty"`
}

type MigrationDiskImportSpec struct {
	// Target is the PVC created by a blank CDI DataVolume.
	Target core.ObjectReference `json:"target"`

	// Transfer describes how data is read from the source.
	Transfer DiskTransfer `json:"transfer"`

	// Checkpoints represent stages in a multistage warm import.
	Checkpoints []DiskCheckpoint `json:"checkpoints,omitempty"`

	// FinalCheckpoint indicates whether the current checkpoint is the final one.
	FinalCheckpoint bool `json:"finalCheckpoint,omitempty"`

	// TransferNetwork optionally overrides the network used by importer pods.
	// +optional
	TransferNetwork *string `json:"transferNetwork,omitempty"`
}

type DiskTransfer struct {
	Type DiskTransferType `json:"type"`

	// VDDK configures nbdkit-vddk based transfer.
	// +optional
	VDDK *VDDKTransfer `json:"vddk,omitempty"`

	// NFC configures HTTP NFC lease based transfer.
	// +optional
	NFC *NFCTransfer `json:"nfc,omitempty"`

	// Toehold configures appliance-side transfer.
	// +optional
	Toehold *ToeholdTransfer `json:"toehold,omitempty"`
}

type VDDKTransfer struct {
	URL          string `json:"url"`
	UUID         string `json:"uuid"`
	BackingFile  string `json:"backingFile"`
	Thumbprint   string `json:"thumbprint"`
	SecretRef    string `json:"secretRef"`
	InitImageURL string `json:"initImageURL,omitempty"`
	// +optional
	ExtraArgsConfigMap *core.ObjectReference `json:"extraArgsConfigMap,omitempty"`
}

type NFCTransfer struct {
	URL       string `json:"url"`
	SecretRef string `json:"secretRef"`
}

type ToeholdTransfer struct {
	Endpoint  string `json:"endpoint"`
	SecretRef string `json:"secretRef"`
}

type MigrationDiskImportStatus struct {
	Phase             MigrationDiskImportPhase `json:"phase,omitempty"`
	TransferType      DiskTransferType         `json:"transferType,omitempty"`
	Progress            string                   `json:"progress,omitempty"`
	CurrentCheckpoint string                   `json:"currentCheckpoint,omitempty"`
	FinalCheckpoint   bool                     `json:"finalCheckpoint,omitempty"`
	ClaimName         string                   `json:"claimName,omitempty"`
	ImporterPodName   string                   `json:"importerPodName,omitempty"`
	// CompletedCheckpoints is the number of spec checkpoints successfully imported.
	CompletedCheckpoints int `json:"completedCheckpoints,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	libcnd.Conditions  `json:",inline"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type MigrationDiskImportList struct {
	meta.TypeMeta `json:",inline"`
	meta.ListMeta `json:"metadata,omitempty"`
	Items         []MigrationDiskImport `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MigrationDiskImport{}, &MigrationDiskImportList{})
}

// CurrentCheckpoint returns the latest checkpoint current value, if any.
func (s *MigrationDiskImportSpec) CurrentCheckpoint() string {
	if len(s.Checkpoints) == 0 {
		return ""
	}
	return s.Checkpoints[len(s.Checkpoints)-1].Current
}
