package version

import (
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
)

func TestDiskHashStable(t *testing.T) {
	spec := api.ToeholdTemplateSpec{
		BaseDisk: api.ToeholdBaseDisk{
			ContainerImage: "registry.example/rhel-guest-image:9.8",
		},
		Network:   "VM Network",
		Resources: api.ToeholdResources{CPU: 2, MemoryMiB: 4096},
	}
	h1 := DiskHash(spec)
	h2 := DiskHash(spec)
	if h1 != h2 {
		t.Fatalf("expected stable hash, got %q vs %q", h1, h2)
	}
	spec.BaseDisk.ContainerImage = "registry.example/rhel-guest-image:9.9"
	h3 := DiskHash(spec)
	if h3 == h1 {
		t.Fatal("expected disk hash to change when base container image changes")
	}
}

func TestConfigHashStable(t *testing.T) {
	spec := api.ToeholdTemplateSpec{
		Network:   "VM Network",
		Resources: api.ToeholdResources{CPU: 2, MemoryMiB: 4096},
	}
	h1 := ConfigHash(spec)
	spec.Resources.CPU = 4
	h2 := ConfigHash(spec)
	if h1 == h2 {
		t.Fatal("expected config hash to change when CPU changes")
	}
}
