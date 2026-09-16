package version

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
)

type configHashInput struct {
	Network   string `json:"network"`
	CPU       int32  `json:"cpu"`
	MemoryMiB int32  `json:"memoryMiB"`
}

// DiskHash fingerprints the base containerdisk image that produces the disk artifact.
func DiskHash(spec api.ToeholdTemplateSpec) string {
	return hash(spec.BaseDisk.ContainerImage)
}

// ConfigHash fingerprints OVF hardware and network configuration.
func ConfigHash(spec api.ToeholdTemplateSpec) string {
	cpu := spec.Resources.CPU
	if cpu == 0 {
		cpu = 2
	}
	mem := spec.Resources.MemoryMiB
	if mem == 0 {
		mem = 4096
	}
	return hash(configHashInput{
		Network:   spec.Network,
		CPU:       cpu,
		MemoryMiB: mem,
	})
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
