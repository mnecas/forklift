package copyappliance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"time"

	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"golang.org/x/crypto/ssh"
)

// applianceSSHPort is the port sshd listens on in the appliance image.
const applianceSSHPort = "22"

// sshPrivateKeyData is the key the appliance's SSH secret holds its private key
// under. It matches the name the provider's SSH key secrets use.
const sshPrivateKeyData = "private-key"

// sshTimeout bounds one login and the commands run over it. A reconcile must
// not sit on an appliance that is not answering; the step is re-entered on the
// next pass.
const sshTimeout = 30 * time.Second

// sshTransferTimeout bounds one image transfer. Unlike a login this is a
// connection the controller means to hold open: a few hundred megabytes to a
// freshly cloned VM is minutes, not seconds. Nothing else in the path imposes a
// limit, so this is what eventually cuts a stuck transfer loose.
const sshTransferTimeout = 30 * time.Minute

// SSHLogin logs in to the appliance for as long as a login and a handful of
// short commands take. See SSHLoginFor.
func (r *ApplianceContext) SSHLogin(ctx context.Context, address string) (client *ssh.Client, answered bool, err error) {
	return r.SSHLoginFor(ctx, address, sshTimeout)
}

// SSHLoginFor logs in to the appliance at the given address, and reports
// whether the appliance answered at all. An appliance that refuses the
// connection, or drops it before the handshake finishes, is still starting
// sshd: that is something to wait for, not a failure. One that answers and then
// rejects the key is a failure. The caller owns the returned client and must
// Close it.
//
// The timeout is set on the connection, not on the handshake, so it bounds the
// login and everything run over it together. A caller that means to move real
// data over the client has to ask for enough for all of it. The dial is bounded
// separately, at sshTimeout: an address that answers nothing at all -- a dropped
// packet rather than a refusal -- is a wait like any other, and must not hold a
// reconcile worker for however long the caller was prepared to spend on the
// transfer that would have followed.
//
// The appliance's host key is not checked. The VM is cloned fresh for each
// CopyAppliance, so there is no key recorded in advance to check it against;
// anything that can answer at this address can therefore impersonate the
// appliance.
func (r *ApplianceContext) SSHLoginFor(ctx context.Context, address string, timeout time.Duration) (client *ssh.Client, answered bool, err error) {
	signer, err := r.signer()
	if err != nil {
		return
	}
	config := &ssh.ClientConfig{
		User:            Settings.CopyAppliance.SSHUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	dialTimeout := timeout
	if dialTimeout > sshTimeout {
		dialTimeout = sshTimeout
	}
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	addr := r.sshAddr(address)
	dialer := &net.Dialer{}
	netConn, err := dialer.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		// Nothing is listening yet, or nothing answered. The guest reports its
		// address as soon as it has one, which is before sshd is accepting
		// connections.
		err = nil
		return
	}
	// Set on the connection so that it outlives dialCtx, which is cancelled
	// when this returns.
	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = netConn.SetDeadline(deadline)

	cc, chans, reqs, err := ssh.NewClientConn(netConn, addr, config)
	if err != nil {
		_ = netConn.Close()
		if isStarting(err) {
			err = nil
			return
		}
		err = liberr.Wrap(err, "address", addr, "user", config.User)
		return
	}
	client = ssh.NewClient(cc, chans, reqs)
	answered = true
	return
}

// RunCommands runs each command in turn over one login and stops at the first
// that fails. The command's output is carried into the error: a remote failure
// says nothing useful without it.
func (r *ApplianceContext) RunCommands(client *ssh.Client, commands ...string) (err error) {
	for _, command := range commands {
		// A session runs one command, so each needs its own.
		session, sErr := client.NewSession()
		if sErr != nil {
			err = liberr.Wrap(sErr, "command", command)
			return
		}
		output, cErr := session.CombinedOutput(command)
		_ = session.Close()
		if cErr != nil {
			err = liberr.New(
				"appliance command failed",
				"command", command,
				"error", cErr.Error(),
				"output", string(output))
			return
		}
	}
	return
}

// RunWithStdin runs one command with the reader as its standard input, and
// returns once the appliance has consumed all of it and the command has exited.
// The reader is streamed rather than read up front, so the payload never has to
// fit in the controller's memory.
//
// Standard error is collected into a buffer rather than read from a pipe: the
// pipe has to be drained before Wait, and a command that writes more than the
// channel window before exiting would deadlock against a Wait that never gets
// to run.
func (r *ApplianceContext) RunWithStdin(client *ssh.Client, command string, in io.Reader) (err error) {
	session, err := client.NewSession()
	if err != nil {
		err = liberr.Wrap(err, "command", command)
		return
	}
	defer func() {
		_ = session.Close()
	}()
	stderr := &bytes.Buffer{}
	session.Stderr = stderr

	stdin, err := session.StdinPipe()
	if err != nil {
		err = liberr.Wrap(err, "command", command)
		return
	}
	err = session.Start(command)
	if err != nil {
		err = liberr.Wrap(err, "command", command)
		return
	}

	_, cErr := io.Copy(stdin, in)
	// The command is told there is no more input either way. Closing is what
	// lets it finish, and a command that has already given up on us is better
	// reported by the exit status and the output it left behind than by the
	// broken pipe we saw writing to it.
	closeErr := stdin.Close()
	wErr := session.Wait()

	switch {
	case wErr != nil:
		err = liberr.New(
			"appliance command failed",
			"command", command,
			"error", wErr.Error(),
			"output", stderr.String())
	case cErr != nil:
		err = liberr.Wrap(cErr, "command", command)
	case closeErr != nil:
		err = liberr.Wrap(closeErr, "command", command)
	}
	return
}

// Probe runs one command and reports whether it succeeded. A command that exits
// non-zero is an answer and not a failure, which is what asks the appliance a
// question rather than telling it to do something; RunCommands cannot say this,
// because there any non-zero exit fails the deploy.
func (r *ApplianceContext) Probe(client *ssh.Client, command string) (ok bool, err error) {
	session, err := client.NewSession()
	if err != nil {
		err = liberr.Wrap(err, "command", command)
		return
	}
	defer func() {
		_ = session.Close()
	}()
	output, rErr := session.CombinedOutput(command)
	if rErr == nil {
		ok = true
		return
	}
	exited := &ssh.ExitError{}
	if errors.As(rErr, &exited) {
		return
	}
	// The command never ran, or the connection went away while it did. That is
	// not an answer to the question.
	err = liberr.Wrap(rErr, "command", command, "output", string(output))
	return
}

// Alive reports whether the login is still usable. It answers the question a
// failed command leaves open: the appliance said no, or the connection went
// away while it was being asked. Those read the same in the error -- the exit
// status and a dropped session both come back as a command that failed -- and
// they mean opposite things, because only one of them is the appliance's
// answer. A connection that has gone is gone for good; ssh does not reconnect.
func (r *ApplianceContext) Alive(client *ssh.Client) (alive bool) {
	ok, err := r.Probe(client, "true")
	alive = ok && err == nil
	return
}

// sshAddr is the address to dial to reach the appliance's sshd.
func (r *ApplianceContext) sshAddr(address string) (addr string) {
	port := r.sshPort
	if port == "" {
		port = applianceSSHPort
	}
	addr = net.JoinHostPort(address, port)
	return
}

// signer is the key the appliance was built with the public half of.
func (r *ApplianceContext) signer() (signer ssh.Signer, err error) {
	ref := r.Appliance.Spec.Secret
	if r.ApplianceSecret == nil {
		err = liberr.New(
			"the appliance secret is missing",
			"namespace", ref.Namespace,
			"name", ref.Name)
		return
	}
	key, found := r.ApplianceSecret.Data[sshPrivateKeyData]
	if !found {
		err = liberr.New(
			"the appliance secret has no "+sshPrivateKeyData,
			"namespace", r.ApplianceSecret.Namespace,
			"name", r.ApplianceSecret.Name)
		return
	}
	signer, err = ssh.ParsePrivateKey(key)
	if err != nil {
		err = liberr.Wrap(err,
			"namespace", r.ApplianceSecret.Namespace,
			"name", r.ApplianceSecret.Name)
		return
	}
	return
}

// isStarting reports whether a failed handshake means sshd is still coming up
// rather than refusing us. sshd accepts the socket before it is ready to talk,
// and drops the connection when it is not, so the handshake ends without a
// reply instead of with one.
func isStarting(err error) (ok bool) {
	ok = errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET)
	return
}
