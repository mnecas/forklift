package version

import (
	"testing"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
)

func TestTemplateContentHashStable(t *testing.T) {
	spec := api.ToeholdSpec{
		BootcImage:   "quay.io/example/bootc:poc",
		TemplateName: "nbdkit-toehold",
		Network:      "VM Network",
		Resources:    api.ToeholdResources{CPU: 2, MemoryMiB: 4096},
	}
	h1 := TemplateContentHash(spec, "sha256:abc")
	h2 := TemplateContentHash(spec, "sha256:abc")
	if h1 != h2 {
		t.Fatalf("expected stable hash, got %q vs %q", h1, h2)
	}
	spec.BootcImage = "quay.io/example/bootc:v2"
	h3 := TemplateContentHash(spec, "sha256:abc")
	if h3 == h1 {
		t.Fatal("expected hash to change when bootcImage changes")
	}
}

func TestVMContentHashStable(t *testing.T) {
	h1 := VMContentHash("tpl", "vm-01", SSHKeyFingerprint("ssh-rsa AAA"))
	h2 := VMContentHash("tpl", "vm-01", SSHKeyFingerprint("ssh-rsa AAA"))
	if h1 != h2 {
		t.Fatalf("expected stable vm hash")
	}
}
