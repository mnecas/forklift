package registry

import "testing"

func TestDigestFromReference(t *testing.T) {
	got := digestFromReference("quay.io/foo/bar@sha256:abc123")
	if got != "sha256:abc123" {
		t.Fatalf("unexpected digest: %s", got)
	}
}

func TestResolveImageIDUsesEmbeddedDigest(t *testing.T) {
	got := ResolveImageID(t.Context(), "quay.io/foo/bar@sha256:deadbeef", nil)
	if got != "sha256:deadbeef" {
		t.Fatalf("unexpected id: %s", got)
	}
}

func TestParseImageReference(t *testing.T) {
	registry, repo, tag, err := parseImageReference("quay.io/mnecas0/nbdkit-appliance-bootc:poc")
	if err != nil {
		t.Fatal(err)
	}
	if registry != "quay.io" || repo != "mnecas0/nbdkit-appliance-bootc" || tag != "poc" {
		t.Fatalf("unexpected parse: %s %s %s", registry, repo, tag)
	}
}
