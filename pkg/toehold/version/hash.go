package version

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
)

type templateHashInput struct {
	BootcImage    string `json:"bootcImage"`
	BootcImageID  string `json:"bootcImageID"`
	TemplateName  string `json:"templateName"`
	Network       string `json:"network"`
	CPU           int32  `json:"cpu"`
	MemoryMiB     int32  `json:"memoryMiB"`
}

type vmHashInput struct {
	TemplateHash string `json:"templateHash"`
	VMName       string `json:"vmName"`
	SSHKeyFP     string `json:"sshKeyFingerprint"`
}

// TemplateContentHash fingerprints inputs that affect the OVF/template artifact.
func TemplateContentHash(spec api.ToeholdSpec, bootcImageID string) string {
	cpu := spec.Resources.CPU
	if cpu == 0 {
		cpu = 2
	}
	mem := spec.Resources.MemoryMiB
	if mem == 0 {
		mem = 4096
	}
	return hash(templateHashInput{
		BootcImage:   spec.BootcImage,
		BootcImageID: bootcImageID,
		TemplateName: spec.TemplateName,
		Network:      spec.Network,
		CPU:          cpu,
		MemoryMiB:    mem,
	})
}

// VMContentHash fingerprints inputs that affect the cloned VM.
func VMContentHash(templateHash, vmName, sshKeyFingerprint string) string {
	return hash(vmHashInput{
		TemplateHash: templateHash,
		VMName:       vmName,
		SSHKeyFP:     sshKeyFingerprint,
	})
}

// SSHKeyFingerprint returns a stable fingerprint for an authorized key line.
func SSHKeyFingerprint(publicKey string) string {
	key := strings.TrimSpace(publicKey)
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func hash(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		sum := sha256.Sum256([]byte(fmt.Sprint(v)))
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
