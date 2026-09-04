package diskimporter

import (
	"testing"
)

func TestLoadConfigFromEnv(t *testing.T) {
	t.Setenv("TRANSFER_TYPE", "VDDK")
	t.Setenv("CHECKPOINT_CURRENT", "snap-1")
	t.Setenv("CHECKPOINT_PREVIOUS", "snap-0")
	t.Setenv("FINAL_CHECKPOINT", "false")
	t.Setenv("VDDK_URL", "https://vcenter.example")
	t.Setenv("VDDK_UUID", "vm-uuid")
	t.Setenv("VDDK_BACKING_FILE", "[datastore] vm/disk.vmdk")
	t.Setenv("VDDK_THUMBPRINT", "thumb")
	t.Setenv("VDDK_SECRET_REF", "secret")

	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv failed: %v", err)
	}
	if cfg.TransferType != "VDDK" {
		t.Fatalf("unexpected transfer type %q", cfg.TransferType)
	}
	if cfg.TargetImage != "/data/disk.img" {
		t.Fatalf("unexpected target image %q", cfg.TargetImage)
	}
	if cfg.VDDK.URL != "https://vcenter.example" {
		t.Fatalf("unexpected vddk url %q", cfg.VDDK.URL)
	}
}

func TestLoadConfigFromEnvRequiresTransferType(t *testing.T) {
	t.Setenv("TRANSFER_TYPE", "")
	t.Setenv("CHECKPOINT_CURRENT", "snap-1")
	if _, err := LoadConfigFromEnv(); err == nil {
		t.Fatal("expected error when TRANSFER_TYPE is empty")
	}
}
