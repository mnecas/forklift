package guest

import (
	"strings"
	"testing"
)

func TestStartCommand(t *testing.T) {
	cmd := StartCommand(10809, "/dev/sdb", "/var/run/nbdkit-disk0.pid", 8)
	if cmd == "" {
		t.Fatal("expected command")
	}
	if !strings.Contains(cmd, `DUMMY_DISK_DEV="/dev/sdb"`) {
		t.Fatalf("unexpected command: %s", cmd)
	}
}

func TestExportURI(t *testing.T) {
	if got := ExportURI("10.0.0.5", 10809); got != "nbd://10.0.0.5:10809/" {
		t.Fatalf("unexpected uri: %s", got)
	}
}
