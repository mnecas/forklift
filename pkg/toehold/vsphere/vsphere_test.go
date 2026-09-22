package vsphere

import "testing"

func TestRequiredDatastoreFreeBytes(t *testing.T) {
	base := InventoryPreflight{RequireTemplateSpace: true}
	if requiredDatastoreFreeBytes(base) != defaultTemplateDatastoreFreeBytes {
		t.Fatalf("expected default template headroom")
	}
	reuse := InventoryPreflight{RequireTemplateSpace: false}
	if requiredDatastoreFreeBytes(reuse) != -1 {
		t.Fatalf("expected no capacity requirement when template reuse")
	}
}

func TestFormatBytes(t *testing.T) {
	if formatBytes(1057482752) != "1008.5MiB" {
		t.Fatalf("unexpected format: %s", formatBytes(1057482752))
	}
}

func TestNormalizeInventoryPathKeepsAbsolute(t *testing.T) {
	if got := normalizeInventoryPath(" /Datacenter/vm/toehold-test "); got != "/Datacenter/vm/toehold-test" {
		t.Fatalf("got %q, want leading slash preserved", got)
	}
	if got := normalizeInventoryPath("vm/toehold-test"); got != "vm/toehold-test" {
		t.Fatalf("got %q, want relative path unchanged", got)
	}
}
