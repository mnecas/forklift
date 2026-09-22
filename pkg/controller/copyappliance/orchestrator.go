package copyappliance

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/template"
	"time"

	liberr "github.com/kubev2v/forklift/pkg/lib/error"
)

// Orchestrator is the NBD supervisor on the appliance, reached over an SSH
// login. The login is bound once rather than handed to each verb.
type Orchestrator struct {
	context *ApplianceContext
	ssh     *SSHClient
}

// NewOrchestrator logs in to the appliance and returns its supervisor, reporting
// whether the appliance answered. The caller owns the result and must Close it.
func NewOrchestrator(ctx context.Context, ac *ApplianceContext, timeout time.Duration) (orch *Orchestrator, ready bool, err error) {
	client, ready, err := ac.SSHClient(ctx, timeout)
	if err != nil || !ready {
		return
	}
	orch = &Orchestrator{
		context: ac,
		ssh:     client,
	}
	return
}

// Close the login the supervisor is reached over.
func (r *Orchestrator) Close() (err error) {
	return r.ssh.Close()
}

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
func (r *Orchestrator) renderUnit() (unit string, err error) {
	appliance := r.context.Appliance
	image := appliance.Status.ExporterImage
	if image == "" {
		err = liberr.New(
			"the appliance has no loaded image to supervise",
			"appliance", appliance.Name)
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
		AnnouncePort: r.context.announcePort(),
		BasePort:     applianceBasePort,
	})
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	unit = buffer.String()
	return
}

// Installed reports whether the appliance already holds exactly what Install
// would put there: the same binary, the same unit and the same certificates. It
// asks in one command, over checksums, so the answer costs nothing next to
// sending ~10MB again.
//
// This is deliberately separate from whether the service is running. Collapsing
// the two would mean an appliance whose orchestrator cannot start gets the whole
// install pushed again on every pass, and every push restarts the service and
// tears down the exports it was supervising.
func (r *Orchestrator) Installed() (ok bool, err error) {
	unit, err := r.renderUnit()
	if err != nil {
		return
	}
	certs, err := r.context.ServerTLS()
	if err != nil {
		return
	}
	manifest, err := r.manifest(unit, certs)
	if err != nil {
		return
	}
	err = r.ssh.RunCommand(
		"printf '%s' " + shellQuote(manifest) + " | sha256sum --status -c -")
	switch {
	case err == nil:
		ok = true
	case IsExitError(err):
		// sha256sum reads the manifest on its standard input and exits non-zero
		// if any line does not match, which is this question's other answer.
		err = nil
	}
	return
}

// manifest is the sha256sum check file describing everything the install puts
// on the appliance, in a fixed order so the same install always renders the
// same manifest.
func (r *Orchestrator) manifest(unit string, certs map[string][]byte) (manifest string, err error) {
	binarySum, err := fileSum(r.source())
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

// Install puts the certificates, the binary and the unit on the appliance and
// starts the service. Enabling it is what makes the supervisor come back when
// the appliance reboots, which is the whole reason for running it under systemd
// rather than just launching it.
func (r *Orchestrator) Install() (err error) {
	unit, err := r.renderUnit()
	if err != nil {
		return
	}
	certs, err := r.context.ServerTLS()
	if err != nil {
		return
	}
	err = r.ssh.RunCommand("install -d -m 0700 " + applianceCertsDir)
	if err != nil {
		return
	}
	// umask rather than a chmod afterwards: nbdkit refuses a server key any
	// wider than the owner, and this way there is no moment where it is.
	for _, name := range []string{tlsCACert, tlsServerCert, tlsServerKey} {
		err = r.ssh.RunWithStdin("(umask 077 && cat > "+applianceCertsDir+"/"+name+")",
			bytes.NewReader(certs[name]))
		if err != nil {
			return
		}
	}

	source := r.source()
	binary, err := os.Open(source)
	if err != nil {
		err = liberr.Wrap(err, "path", source)
		return
	}
	defer func() {
		_ = binary.Close()
	}()
	err = r.ssh.RunWithStdin("(umask 022 && cat > "+orchestratorStaging+") && "+
		"chmod 0755 "+orchestratorStaging+" && "+
		"mv -f "+orchestratorStaging+" "+orchestratorBinary,
		binary)
	if err != nil {
		return
	}

	err = r.ssh.RunWithStdin("(umask 022 && cat > "+orchestratorService+")",
		strings.NewReader(unit))
	if err != nil {
		return
	}

	// The commands configure one appliance in sequence, so running the rest
	// after one has failed would configure it half way and call it done.
	for _, command := range []string{
		"systemctl daemon-reload",
		"systemctl enable " + orchestratorUnit,
		// Clears a start limit tripped by an earlier install, which would
		// otherwise make every restart below exit non-zero for good.
		"systemctl reset-failed " + orchestratorUnit,
		"systemctl restart " + orchestratorUnit,
	} {
		err = r.ssh.RunCommand(command)
		if err != nil {
			return
		}
	}
	return
}

// Active reports whether the supervisor is running. This is not proof that it
// works -- systemd reports a Type=simple unit active as soon as it has forked,
// and a crash loop is active for part of every cycle. The proof is the appliance
// answering with its exports, which is what WaitForExports is for; this only
// keeps Configure from reporting success over a service that is plainly down.
func (r *Orchestrator) Active() (ok bool, err error) {
	err = r.ssh.RunCommand("systemctl is-active --quiet " + orchestratorUnit)
	switch {
	case err == nil:
		ok = true
	case IsExitError(err):
		// is-active exits non-zero for a unit that is not running, which is
		// this question's other answer.
		err = nil
	}
	return
}

// Start starts an already-installed supervisor that is not running. It starts
// rather than restarts: a restart would tear down the exports of a service that
// is in fact up, and reset-failed first because a unit that has tripped
// systemd's start limit stays failed and refuses to start at all.
func (r *Orchestrator) Start() (err error) {
	return r.systemctl("start")
}

// Restart restarts the supervisor so it rediscovers block devices after disks
// are hot-attached to the appliance VM.
func (r *Orchestrator) Restart() (err error) {
	return r.systemctl("restart")
}

// systemctl clears a tripped start limit and then runs the verb. A unit that
// has tripped the limit stays failed and refuses both verbs until it is reset.
func (r *Orchestrator) systemctl(verb string) (err error) {
	err = r.ssh.RunCommand("systemctl reset-failed " + orchestratorUnit)
	if err != nil {
		return
	}
	err = r.ssh.RunCommand("systemctl " + verb + " " + orchestratorUnit)
	return
}

// Log is the tail of the supervisor's journal, for logging when it will not
// come up. Best effort: this runs on the path where something is already wrong,
// and failing to collect the evidence must not replace the problem being
// reported.
func (r *Orchestrator) Log() (tail string) {
	session, err := r.ssh.Client.NewSession()
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

// source is the binary to ship to the appliance.
func (r *Orchestrator) source() (path string) {
	path = r.context.orchestratorPath
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
