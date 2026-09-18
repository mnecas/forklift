package copyappliance

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"text/template"

	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"golang.org/x/crypto/ssh"
)

// Where the orchestrator's pieces live on the appliance.
const (
	orchestratorBinary  = "/usr/local/bin/nbd-orchestrator"
	orchestratorUnit    = "nbd-orchestrator.service"
	orchestratorService = "/etc/systemd/system/" + orchestratorUnit
	applianceCertsDir   = "/etc/pki/nbd"
)

// orchestratorStaging is where the binary is written before it is moved into
// place. It is beside the destination and not in /tmp for two reasons: a
// running binary cannot be written in place, and a file moved between
// directories keeps the SELinux label of where it was created, which for /tmp
// is not one systemd will execute.
const orchestratorStaging = "/usr/local/bin/.nbd-orchestrator.tmp"

// controllerOrchestrator is where the controller image keeps the copy of the
// orchestrator it ships to appliances. Built into the image by
// build/forklift-controller/Containerfile.
const controllerOrchestrator = "/usr/local/bin/nbd-orchestrator"

// applianceAnnouncePort is the port the orchestrator serves its export list on,
// and applianceBasePort the first host port it publishes an export on. Both
// match the orchestrator's own defaults; they are named here because the
// appliance template has to have them open and the controller has to dial them.
const (
	applianceAnnouncePort = "8443"
	applianceBasePort     = 10809
)

// Keys of the TLS material within the appliance secret. They are the file names
// the appliance expects, so that what is in the secret is what lands on it.
const (
	tlsCACert     = "ca-cert.pem"
	tlsServerCert = "server-cert.pem"
	tlsServerKey  = "server-key.pem"
	tlsClientCert = "client-cert.pem"
	tlsClientKey  = "client-key.pem"
)

//go:embed nbd-orchestrator.service.tmpl
var orchestratorUnitTemplate string

var orchestratorUnitText = template.Must(
	template.New(orchestratorUnit).Parse(orchestratorUnitTemplate))

// renderUnit returns the systemd unit for this appliance. The image is the one
// LoadImage put in the appliance's podman store, which is why the unit is
// rendered per appliance rather than shipped as a fixed file.
func (r *ApplianceContext) renderUnit() (unit string, err error) {
	image := r.Appliance.Status.ExporterImage
	if image == "" {
		err = liberr.New(
			"the appliance has no loaded image to supervise",
			"appliance", r.Appliance.Name)
		return
	}
	buffer := &bytes.Buffer{}
	err = orchestratorUnitText.Execute(buffer, struct {
		Binary       string
		CertsDir     string
		Image        string
		AnnouncePort string
		BasePort     int
	}{
		Binary:       orchestratorBinary,
		CertsDir:     applianceCertsDir,
		Image:        image,
		AnnouncePort: r.announcePort(),
		BasePort:     applianceBasePort,
	})
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	unit = buffer.String()
	return
}

// ServerTLS is the half of the TLS material the appliance needs: the CA to
// verify clients against, and the certificate and key it serves with. The
// client half stays in the cluster. Keyed by the file name each lands under in
// applianceCertsDir.
func (r *ApplianceContext) ServerTLS() (files map[string][]byte, err error) {
	return r.tlsData(tlsCACert, tlsServerCert, tlsServerKey)
}

// ClientTLS is the half the controller keeps, to query the appliance's export
// list with.
func (r *ApplianceContext) ClientTLS() (ca, certificate, key []byte, err error) {
	files, err := r.tlsData(tlsCACert, tlsClientCert, tlsClientKey)
	if err != nil {
		return
	}
	return files[tlsCACert], files[tlsClientCert], files[tlsClientKey], nil
}

// tlsData reads the named keys out of the appliance's secret, and reports which
// one is missing rather than that something is.
func (r *ApplianceContext) tlsData(keys ...string) (files map[string][]byte, err error) {
	ref := r.Appliance.Spec.Secret
	if r.ApplianceSecret == nil {
		err = liberr.New(
			"the appliance secret is missing",
			"namespace", ref.Namespace,
			"name", ref.Name)
		return
	}
	files = make(map[string][]byte, len(keys))
	for _, key := range keys {
		value, found := r.ApplianceSecret.Data[key]
		if !found || len(value) == 0 {
			files = nil
			err = liberr.New(
				"the appliance secret has no "+key,
				"namespace", r.ApplianceSecret.Namespace,
				"name", r.ApplianceSecret.Name)
			return
		}
		files[key] = value
	}
	return
}

// OrchestratorInstalled reports whether the appliance already holds exactly
// what InstallOrchestrator would put there: the same binary, the same unit and
// the same certificates. It asks in one command, over checksums, so the answer
// costs nothing next to sending ~10MB again.
//
// This is deliberately separate from whether the service is running. Collapsing
// the two would mean an appliance whose orchestrator cannot start gets the whole
// install pushed again on every pass, and every push restarts the service and
// tears down the exports it was supervising.
func (r *ApplianceContext) OrchestratorInstalled(client *ssh.Client, unit string, certs map[string][]byte) (ok bool, err error) {
	manifest, err := r.manifest(unit, certs)
	if err != nil {
		return
	}
	// sha256sum reads the manifest on its standard input and exits non-zero if
	// any line does not match, which is an answer and so wants Probe.
	return r.Probe(client, "printf '%s' "+shellQuote(manifest)+" | sha256sum --status -c -")
}

// manifest is the sha256sum check file describing everything the install puts
// on the appliance, in a fixed order so the same install always renders the
// same manifest.
func (r *ApplianceContext) manifest(unit string, certs map[string][]byte) (manifest string, err error) {
	binarySum, err := fileSum(r.orchestratorSource())
	if err != nil {
		return
	}
	lines := []string{
		sumLine(binarySum, orchestratorBinary),
		sumLine(dataSum([]byte(unit)), orchestratorService),
	}
	names := make([]string, 0, len(certs))
	for name := range certs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		lines = append(lines, sumLine(dataSum(certs[name]), applianceCertsDir+"/"+name))
	}
	manifest = strings.Join(lines, "")
	return
}

// InstallOrchestrator puts the certificates, the binary and the unit on the
// appliance and starts the service. Enabling it is what makes the supervisor
// come back when the appliance reboots, which is the whole reason for running
// it under systemd rather than just launching it.
func (r *ApplianceContext) InstallOrchestrator(client *ssh.Client, unit string, certs map[string][]byte) (err error) {
	err = r.RunCommands(client, "install -d -m 0700 "+applianceCertsDir)
	if err != nil {
		return
	}
	// umask rather than a chmod afterwards: nbdkit refuses a server key any
	// wider than the owner, and this way there is no moment where it is.
	for _, name := range []string{tlsCACert, tlsServerCert, tlsServerKey} {
		err = r.RunWithStdin(client,
			"(umask 077 && cat > "+applianceCertsDir+"/"+name+")",
			bytes.NewReader(certs[name]))
		if err != nil {
			return
		}
	}

	binary, err := os.Open(r.orchestratorSource())
	if err != nil {
		err = liberr.Wrap(err, "path", r.orchestratorSource())
		return
	}
	defer func() {
		_ = binary.Close()
	}()
	err = r.RunWithStdin(client,
		"(umask 022 && cat > "+orchestratorStaging+") && "+
			"chmod 0755 "+orchestratorStaging+" && "+
			"mv -f "+orchestratorStaging+" "+orchestratorBinary,
		binary)
	if err != nil {
		return
	}

	err = r.RunWithStdin(client,
		"(umask 022 && cat > "+orchestratorService+")",
		strings.NewReader(unit))
	if err != nil {
		return
	}

	return r.RunCommands(client,
		"systemctl daemon-reload",
		"systemctl enable "+orchestratorUnit,
		// Clears a start limit tripped by an earlier install, which would
		// otherwise make every restart below exit non-zero for good.
		"systemctl reset-failed "+orchestratorUnit,
		"systemctl restart "+orchestratorUnit)
}

// OrchestratorActive reports whether the supervisor is running. This is not
// proof that it works -- systemd reports a Type=simple unit active as soon as it
// has forked, and a crash loop is active for part of every cycle. The proof is
// the appliance answering with its exports, which is what WaitForExports is for;
// this only keeps Configure from reporting success over a service that is
// plainly down.
func (r *ApplianceContext) OrchestratorActive(client *ssh.Client) (ok bool, err error) {
	return r.Probe(client, "systemctl is-active --quiet "+orchestratorUnit)
}

// StartOrchestrator starts an already-installed supervisor that is not running.
// It starts rather than restarts: a restart would tear down the exports of a
// service that is in fact up, and reset-failed first because a unit that has
// tripped systemd's start limit stays failed and refuses to start at all.
func (r *ApplianceContext) StartOrchestrator(client *ssh.Client) (err error) {
	return r.RunCommands(client,
		"systemctl reset-failed "+orchestratorUnit,
		"systemctl start "+orchestratorUnit)
}

// RestartOrchestrator restarts the supervisor so it rediscovers block devices
// after disks are hot-attached to the appliance VM.
func (r *ApplianceContext) RestartOrchestrator(client *ssh.Client) (err error) {
	return r.RunCommands(client,
		"systemctl reset-failed "+orchestratorUnit,
		"systemctl restart "+orchestratorUnit)
}

// OrchestratorLog is the tail of the supervisor's journal, for logging when it
// will not come up. Best effort: this runs on the path where something is
// already wrong, and failing to collect the evidence must not replace the
// problem being reported.
func (r *ApplianceContext) OrchestratorLog(client *ssh.Client) (tail string) {
	session, err := client.NewSession()
	if err != nil {
		return "(no journal)"
	}
	defer func() {
		_ = session.Close()
	}()
	output, _ := session.CombinedOutput(
		"journalctl -u " + orchestratorUnit + " --no-pager -n 20")
	tail = strings.TrimSpace(string(output))
	if tail == "" {
		// Either journalctl failed, or the unit has never said anything. Both
		// read better as this than as an empty field beside the problem.
		return "(no journal)"
	}
	return
}

// announceAddr is the address to query the appliance's export list at.
func (r *ApplianceContext) announceAddr(address string) (addr string) {
	return net.JoinHostPort(address, r.announcePort())
}

// announcePort is the port the appliance announces its exports on. Empty means
// the port the orchestrator defaults to, which is the only one an appliance is
// installed with; a test appliance is on whatever it was given.
func (r *ApplianceContext) announcePort() (port string) {
	port = r.announcePortOverride
	if port == "" {
		port = applianceAnnouncePort
	}
	return
}

// orchestratorSource is the binary to ship to the appliance.
func (r *ApplianceContext) orchestratorSource() (path string) {
	path = r.orchestratorPath
	if path == "" {
		path = controllerOrchestrator
	}
	return
}

// fileSum is the hex sha256 of a file, read in a stream so that the binary
// never has to be held in memory to be summed.
func fileSum(path string) (sum string, err error) {
	file, err := os.Open(path)
	if err != nil {
		err = liberr.Wrap(err, "path", path)
		return
	}
	defer func() {
		_ = file.Close()
	}()
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	if err != nil {
		err = liberr.Wrap(err, "path", path)
		return
	}
	sum = hex.EncodeToString(hash.Sum(nil))
	return
}

// dataSum is the hex sha256 of a value already in hand.
func dataSum(data []byte) (sum string) {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// sumLine is one line of a sha256sum check file.
func sumLine(sum, path string) (line string) {
	return fmt.Sprintf("%s  %s\n", sum, path)
}

// shellQuote wraps a value so a POSIX shell passes it through unchanged.
func shellQuote(value string) (quoted string) {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
