package guest

import (
	"fmt"
	"strconv"
)

const startScript = "/usr/local/sbin/start-nbdkit.sh"

// StartCommand returns the shell command that starts nbdkit-file for a disk export.
func StartCommand(port int32, device, pidfile string, threads int32) string {
	if threads <= 0 {
		threads = 8
	}
	return fmt.Sprintf(
		"NBD_PORT=%d DUMMY_DISK_DEV=%s NBDKIT_PIDFILE=%s NBDKIT_THREADS=%d %s",
		port,
		shellQuote(device),
		shellQuote(pidfile),
		threads,
		startScript,
	)
}

// VerifyCommand returns a shell command that checks nbdkit is listening on a port.
func VerifyCommand(port int32, pidfile string) string {
	return fmt.Sprintf(
		`test -f %s && kill -0 "$(cat %s)" 2>/dev/null && ss -ltn | grep -q ":%d "`,
		shellQuote(pidfile),
		shellQuote(pidfile),
		port,
	)
}

// StopCommand returns a shell command that stops nbdkit for a pidfile.
func StopCommand(pidfile string) string {
	return fmt.Sprintf(
		`if test -f %s; then kill "$(cat %s)" 2>/dev/null || true; rm -f %s; fi`,
		shellQuote(pidfile),
		shellQuote(pidfile),
		shellQuote(pidfile),
	)
}

// PIDFile returns the pidfile path for a named export.
func PIDFile(name string) string {
	return fmt.Sprintf("/var/run/nbdkit-%s.pid", sanitizeName(name))
}

// ExportURI builds the client NBD URI for an appliance IP and port.
func ExportURI(host string, port int32) string {
	return fmt.Sprintf("nbd://%s:%d/", host, port)
}

func shellQuote(value string) string {
	return strconv.Quote(value)
}

func sanitizeName(name string) string {
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "export"
	}
	return string(out)
}
