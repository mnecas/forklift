package v1beta1

import (
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GuestConversion represents a virt-v2v conversion or inspection operation.
type GuestConversion struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              GuestConversionSpec   `json:"spec"`
	Status            GuestConversionStatus `json:"status,omitempty"`
}

// ──────────────────────────────────────────────
// Spec
// ──────────────────────────────────────────────

type GuestConversionSpec struct {
	// "copy" or "inspect" or "inPlace"
	Mode TransferMode `json:"mode"`
	// Source provider type: "vsphere", "ova", "hyperv", "ec2"
	Source SourceType `json:"source"`
	// VM identification
	VM VMSpec `json:"vm"`
	// Disks to convert (PVC references in the same namespace)
	Disks []DiskRef `json:"disks"`
	// Provider connection details (vSphere only)
	Connection *ConnectionSpec `json:"connection,omitempty"`
	// Secret containing provider credentials
	Secret *core.LocalObjectReference `json:"secret,omitempty"`
	// VDDK configuration (vSphere only)
	VDDK *VDDKSpec `json:"vddk,omitempty"`
	// OVA/HyperV source configuration
	OVFSource *OVFSourceSpec `json:"ovfSource,omitempty"`
	// Conversion tuning options
	Options ConversionOptions `json:"options,omitempty"`
	// Pod scheduling constraints
	Scheduling SchedulingSpec `json:"scheduling,omitempty"`
}

type TransferMode string

const (
	// Disks already populated, convert in place
	TransferModeInPlace TransferMode = "inPlace"
	// Pull from source, convert, write to disks
	TransferModeCopy TransferMode = "copy"
	// Inspect disks only, no conversion
	TransferModeInspect TransferMode = "inspect"
)

type SourceType string

const (
	SourceVSphere SourceType = "vsphere"
	SourceOVA     SourceType = "ova"
	SourceHyperV  SourceType = "hyperv"
	SourceEC2     SourceType = "ec2"
)

// ──────────────────────────────────────────────
// VM identification
// ──────────────────────────────────────────────

type VMSpec struct {
	// Original VM name (passed to virt-v2v)
	Name string `json:"name"`
	// DNS-1123 safe name for virt-v2v output
	NewName string `json:"newName,omitempty"`
}

// ──────────────────────────────────────────────
// Disk references
// ──────────────────────────────────────────────

type DiskRef struct {
	// PVC name (same namespace as GuestConversion)
	Name string `json:"name"`
	// "filesystem" or "block"
	VolumeMode core.PersistentVolumeMode `json:"volumeMode"`
}

// ──────────────────────────────────────────────
// vSphere connection
// ──────────────────────────────────────────────

type ConnectionSpec struct {
	// Libvirt URI (e.g. "vpx://user@vcenter/Datacenter/host/esxi?no_verify=1")
	LibvirtURI string `json:"libvirtURI"`
	// vCenter SSL thumbprint
	Fingerprint string `json:"fingerprint"`
}

// ──────────────────────────────────────────────
// VDDK configuration (vSphere)
// ──────────────────────────────────────────────

type VDDKSpec struct {
	// Init container image containing VDDK library
	InitImage string `json:"initImage"`
	// Optional ConfigMap with additional VDDK config
	ConfigMap *core.LocalObjectReference `json:"configMap,omitempty"`
	// Specific disk files for remote inspection
	RemoteDisks []string `json:"remoteDisks,omitempty"`
}

// ──────────────────────────────────────────────
// OVA / HyperV source
// ──────────────────────────────────────────────
type OVFSourceSpec struct {
	// Path to the OVF/disk within the mounted share
	DiskPath string `json:"diskPath"`
	// Pre-existing PVC with OVA/HyperV source data
	SourcePVC *core.LocalObjectReference `json:"sourcePVC,omitempty"`
	// NFS or SMB source details
	NFS *NFSSource `json:"nfs,omitempty"`
	SMB *SMBSource `json:"smb,omitempty"`
}

type NFSSource struct {
	// NFS server address (e.g. "192.168.1.10")
	Server string `json:"server"`
	// NFS export path (e.g. "/exports/ova-files")
	Path string `json:"path"`
}

type SMBSource struct {
	// SMB share URI (e.g. "//hyperv-host/share")
	Server string `json:"server"`
	// Secret with SMB credentials (username/password)
	Secret core.LocalObjectReference `json:"secret"`
}

// ──────────────────────────────────────────────
// Conversion options
// ──────────────────────────────────────────────

type ConversionOptions struct {
	// Which disk is the root/boot disk ("first" by default)
	RootDisk string `json:"rootDisk,omitempty"`
	// Static IP preservation: MAC-to-IP mappings
	StaticIPs []StaticIPMapping `json:"staticIPs,omitempty"`
	// Use legacy virtio-win drivers (old Windows)
	LegacyDrivers bool `json:"legacyDrivers,omitempty"`
	// LUKS encrypted disk handling
	LUKS *LUKSSpec `json:"luks,omitempty"`
	// Extra CLI args passed to virt-v2v / virt-v2v-in-place
	ExtraArgs []string `json:"extraArgs,omitempty"`
	// Extra CLI args passed to virt-v2v-inspector only
	InspectorExtraArgs []string `json:"inspectorExtraArgs,omitempty"`
	// Customization scripts to apply post-conversion
	CustomizeScripts *core.LocalObjectReference `json:"customizeScripts,omitempty"`
	// ESXi hostname for direct connection
	Hostname string `json:"hostname,omitempty"`
}

type StaticIPMapping struct {
	// MAC address (aa:bb:cc:dd:ee:ff)
	MAC string `json:"mac"`
	// IP address to assign
	IP string `json:"ip"`
	// Gateway (optional)
	Gateway string `json:"gateway,omitempty"`
	// Prefix length (optional, e.g. "24")
	Prefix string `json:"prefix,omitempty"`
	// DNS nameservers (optional)
	DNS []string `json:"dns,omitempty"`
}

type LUKSSpec struct {
	// Secret containing LUKS decryption keys
	Secret core.LocalObjectReference `json:"secret"`
	// Use NBDE/Clevis for decryption instead of key files
	Clevis bool `json:"clevis,omitempty"`
}

// ──────────────────────────────────────────────
// Pod scheduling
// ──────────────────────────────────────────────

type SchedulingSpec struct {
	// Node selector for the conversion pod
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Additional labels on the conversion pod
	Labels map[string]string `json:"labels,omitempty"`
	// Additional annotations on the conversion pod
	Annotations map[string]string `json:"annotations,omitempty"`
	// Resource requirements override
	Resources *core.ResourceRequirements `json:"resources,omitempty"`
}

// ──────────────────────────────────────────────
// Status
// ──────────────────────────────────────────────

type GuestConversionStatus struct {
	Phase ConversionPhase `json:"phase"`
	// Reference to the created pod
	Pod *core.ObjectReference `json:"pod,omitempty"`
	// Per-disk transfer/conversion progress
	Progress []DiskProgress `json:"progress,omitempty"`
	// Results (populated on completion)
	Result *ConversionResult `json:"result,omitempty"`
	// Timing
	Started   *metav1.Time `json:"started,omitempty"`
	Completed *metav1.Time `json:"completed,omitempty"`
	// Standard conditions
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type ConversionPhase string

const (
	PhasePending   ConversionPhase = "Pending"
	PhaseCreating  ConversionPhase = "CreatingPod"
	PhaseRunning   ConversionPhase = "Running"
	PhaseSucceeded ConversionPhase = "Succeeded"
	PhaseFailed    ConversionPhase = "Failed"
)

type DiskProgress struct {
	// Disk index (0-based, matches Spec.Disks order)
	Index int `json:"index"`
	// PVC name
	Name string `json:"name"`
	// 0-100
	Progress int `json:"progress"`
}

// ──────────────────────────────────────────────
// Conversion result (from virt-v2v output)
// ──────────────────────────────────────────────

type ConversionResult struct {
	// Detected firmware: "bios" or "uefi"
	Firmware string `json:"firmware,omitempty"`
	// Detected OS (VMware guest ID or osinfo ID)
	OperatingSystem string `json:"operatingSystem,omitempty"`
	// Warnings emitted by virt-v2v
	Warnings []string `json:"warnings,omitempty"`
}
