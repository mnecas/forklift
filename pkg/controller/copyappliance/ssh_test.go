package copyappliance

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	api "github.com/kubev2v/forklift/pkg/apis/forklift/v1beta1"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"golang.org/x/crypto/ssh"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The appliance is talked to over a real protocol, so the tests answer it with
// a real one. An SSH server standing in for the appliance is a handful of lines
// and exercises the handshake, the auth exchange and the exec channel the way
// the appliance will; a stub behind an interface would only replay what the
// production code already assumes.
type sshServer struct {
	addr string
	// failing names the commands the appliance refuses to run, and dropping
	// the ones it drops the whole connection on rather than answering.
	failing  map[string]bool
	dropping map[string]bool
	mutex    sync.Mutex
	ran      []sshCommand
}

// sshCommand is one command the appliance was asked to run and everything it
// was given on its standard input.
type sshCommand struct {
	command string
	stdin   []byte
}

// commandFailureOutput is what a failing command writes, so that a test can see
// whether the output made it into the error.
const commandFailureOutput = "no such file or directory"

// startSSHServer answers on the loopback address until the test ends, accepting
// the one key it is given and refusing every other. A nil key refuses all of
// them, which is the appliance that was built with somebody else's.
func startSSHServer(t *testing.T, authorized ssh.PublicKey, failing ...string) *sshServer {
	t.Helper()
	server := &sshServer{
		failing:  map[string]bool{},
		dropping: map[string]bool{},
	}
	for _, command := range failing {
		server.failing[command] = true
	}

	config := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, offered ssh.PublicKey) (*ssh.Permissions, error) {
			if authorized == nil || !bytes.Equal(offered.Marshal(), authorized.Marshal()) {
				return nil, fmt.Errorf("key not in authorized_keys")
			}
			return &ssh.Permissions{}, nil
		},
	}
	config.AddHostKey(testSigner(t))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	server.addr = listener.Addr().String()

	go func() {
		for {
			conn, aErr := listener.Accept()
			if aErr != nil {
				// The listener was closed at the end of the test.
				return
			}
			go server.serve(conn, config)
		}
	}()
	return server
}

// dropOn makes the appliance drop the connection when it is asked to run this
// command, which is the network going away part way through a transfer.
func (r *sshServer) dropOn(command string) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.dropping[command] = true
}

// Ran is the commands the appliance was asked to run, in the order it was asked.
func (r *sshServer) Ran() []string {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	names := make([]string, 0, len(r.ran))
	for _, ran := range r.ran {
		names = append(names, ran.command)
	}
	return names
}

// Stdin is what the named command was given on its standard input, and whether
// it was run at all. The last run wins, which for a command run once is the
// only one there is.
func (r *sshServer) Stdin(command string) (stdin []byte, ran bool) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	for _, was := range slices.Backward(r.ran) {
		if was.command == command {
			return was.stdin, true
		}
	}
	return
}

func (r *sshServer) serve(conn net.Conn, config *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		// A rejected key, or a client that hung up. Either way there is no
		// connection left to serve.
		_ = conn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, newChannel.ChannelType())
			continue
		}
		channel, requests, aErr := newChannel.Accept()
		if aErr != nil {
			return
		}
		go r.session(channel, requests, func() { _ = conn.Close() })
	}
}

// session answers one exec request and closes, which is what one command over
// its own session looks like from the appliance's side.
func (r *sshServer) session(channel ssh.Channel, requests <-chan *ssh.Request, drop func()) {
	defer func() { _ = channel.Close() }()
	for request := range requests {
		if request.Type != "exec" {
			if request.WantReply {
				_ = request.Reply(false, nil)
			}
			continue
		}
		var payload struct{ Command string }
		_ = ssh.Unmarshal(request.Payload, &payload)
		if request.WantReply {
			_ = request.Reply(true, nil)
		}
		status, dropped := r.run(payload.Command, channel)
		if dropped {
			drop()
			return
		}
		_, _ = channel.SendRequest(
			"exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
		return
	}
}

// run reads the command's standard input to the end and then answers. Draining
// first is what a real command does, and a server that replied without draining
// would hang any client sending more than fits in the channel's window.
func (r *sshServer) run(command string, channel ssh.Channel) (status uint32, dropped bool) {
	stdin, _ := io.ReadAll(channel)
	r.mutex.Lock()
	r.ran = append(r.ran, sshCommand{command: command, stdin: stdin})
	refused := r.failing[command]
	dropped = r.dropping[command]
	r.mutex.Unlock()
	if dropped {
		return
	}
	if refused {
		// On stderr, where a failing command writes. CombinedOutput merges the
		// two, so a caller that reads either still sees it.
		_, _ = io.WriteString(channel.Stderr(), commandFailureOutput)
		status = 1
	}
	return
}

// testSigner is a host key. ed25519 because it generates instantly; an RSA key
// would take longer than everything else in the package put together.
func testSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatalf("host key signer: %v", err)
	}
	return signer
}

// testKeyPair is a private key in the PEM form the secret holds, and the public
// half the appliance image would have installed.
func testKeyPair(t *testing.T) (private []byte, public ssh.PublicKey) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("key signer: %v", err)
	}
	return pem.EncodeToMemory(block), signer.PublicKey()
}

// sshContext is an appliance reachable at the given address. Nothing here talks
// to vCenter, so it has no connection.
func sshContext(t *testing.T, private []byte, addr string) *ApplianceContext {
	t.Helper()
	withSettings(t, testSettings())
	appliance := testAppliance()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	appliance.Status.Addresses = []api.ApplianceAddress{
		{Network: "VM Network", MAC: "00:50:56:01:02:03", IP: host},
	}
	// One secret carries both halves, so an appliance without a key still has
	// the certificates the steps ahead of the login read.
	data := make(map[string][]byte, len(applianceTLS().data)+1)
	for key, value := range applianceTLS().data {
		data[key] = value
	}
	if private != nil {
		data[sshPrivateKeyData] = private
	}
	ac := &ApplianceContext{
		Appliance: appliance,
		Log:       testLog(),
		sshPort:   port,
		ApplianceSecret: &core.Secret{
			ObjectMeta: meta.ObjectMeta{
				Namespace: appliance.Spec.Secret.Namespace,
				Name:      appliance.Spec.Secret.Name,
			},
			Data: data,
		},
		orchestratorPath: writeOrchestrator(t),
	}
	return ac
}

// errorMentions reports whether the error says the given thing anywhere an
// operator would read it. liberr keeps the message fixed and carries what names
// the secret, the command or the network beside it, so both have to be looked
// at.
func errorMentions(t *testing.T, err error, want string) bool {
	t.Helper()
	if strings.Contains(err.Error(), want) {
		return true
	}
	wrapped := &liberr.Error{}
	if !errors.As(err, &wrapped) {
		t.Fatalf("error = %v (%T), want a liberr.Error", err, err)
	}
	for _, value := range wrapped.Context() {
		if strings.Contains(fmt.Sprintf("%v", value), want) {
			return true
		}
	}
	return false
}

// closedAddr is an address that accepted a connection a moment ago and does not
// now, which is the appliance between the guest reporting its address and sshd
// coming up.
func closedAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return addr
}

func TestSSHClient(t *testing.T) {
	t.Run("a login with the installed key succeeds", func(t *testing.T) {
		private, public := testKeyPair(t)
		server := startSSHServer(t, public)
		ac := sshContext(t, private, server.addr)

		client, ready, err := ac.SSHClient(context.TODO(), sshTimeout)
		if err != nil {
			t.Fatalf("SSHClient: %v", err)
		}
		if !ready || client == nil {
			t.Fatalf("ready = %v, client = %v, want a logged-in client", ready, client)
		}
		_ = client.Close()
	})

	// An appliance that answers and then turns us away is not one to wait for.
	t.Run("a login with a key the appliance does not know fails", func(t *testing.T) {
		private, _ := testKeyPair(t)
		_, installed := testKeyPair(t)
		server := startSSHServer(t, installed)
		ac := sshContext(t, private, server.addr)

		client, ready, err := ac.SSHClient(context.TODO(), sshTimeout)
		if err == nil {
			t.Fatal("SSHClient succeeded with a key the appliance does not know")
		}
		if ready || client != nil {
			t.Errorf("ready = %v, client = %v, want neither", ready, client)
		}
	})

	t.Run("an address nothing is listening on has not answered", func(t *testing.T) {
		private, _ := testKeyPair(t)
		ac := sshContext(t, private, closedAddr(t))

		client, ready, err := ac.SSHClient(context.TODO(), sshTimeout)
		if err != nil {
			t.Fatalf("SSHClient: %v, want a closed port to be something to wait for", err)
		}
		if ready || client != nil {
			t.Errorf("ready = %v, client = %v, want neither", ready, client)
		}
	})

	// The provider's key secrets carry both halves under fixed names, so a
	// secret without the private one is the wrong secret.
	t.Run("a secret with no private key fails", func(t *testing.T) {
		_, public := testKeyPair(t)
		server := startSSHServer(t, public)
		ac := sshContext(t, nil, server.addr)
		ac.ApplianceSecret = &core.Secret{
			ObjectMeta: meta.ObjectMeta{Namespace: "forklift", Name: "appliance-secret"},
			Data:       map[string][]byte{"public-key": []byte("ssh-ed25519 AAAA")},
		}

		_, _, err := ac.SSHClient(context.TODO(), sshTimeout)
		if err == nil {
			t.Fatal("SSHClient succeeded with no private key in the secret")
		}
		if !errorMentions(t, err, sshPrivateKeyData) {
			t.Errorf("error = %q, want it to name %q", err, sshPrivateKeyData)
		}
	})

	// The secret is tolerated as missing so that a teardown is not blocked, so
	// this is where an operator finds out which one to create.
	t.Run("a missing secret fails by name", func(t *testing.T) {
		ac := sshContext(t, nil, closedAddr(t))
		ac.ApplianceSecret = nil

		_, _, err := ac.SSHClient(context.TODO(), sshTimeout)
		if err == nil {
			t.Fatal("SSHClient succeeded with no secret at all")
		}
		if !errorMentions(t, err, ac.Appliance.Spec.Secret.Name) {
			t.Errorf("error = %q, want it to name the secret", err)
		}
	})
}

// The timeout SSHClient is given is what the caller is prepared to spend on the
// transfer the login is for, and LoadImage asks for thirty minutes of it. The
// reconcile's own context has to be able to end it sooner: otherwise one
// appliance that accepts connections and then says nothing holds a reconcile
// worker for the whole half hour.
func TestSSHClientHonoursTheContextDeadline(t *testing.T) {
	private, _ := testKeyPair(t)
	ac := sshContext(t, private, silentAddr(t))
	ctx, cancel := context.WithTimeout(context.TODO(), 250*time.Millisecond)
	defer cancel()

	type result struct {
		client *SSHClient
		ready  bool
		err    error
	}
	finished := make(chan result, 1)
	go func() {
		client, ready, err := ac.SSHClient(ctx, SSHFileTransferTimeout)
		finished <- result{client, ready, err}
	}()

	select {
	case got := <-finished:
		if got.err == nil {
			t.Fatal("SSHClient succeeded against an appliance that never said anything")
		}
		if got.ready || got.client != nil {
			t.Errorf("ready = %v, client = %v, want neither", got.ready, got.client)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SSHClient is still waiting: the login is bounded by its own timeout only")
	}
}

// silentAddr accepts connections and then says nothing, which is the appliance
// that costs the most: the dial succeeds, so there is no refusal to read as
// "still starting", and the handshake waits for a banner that never comes.
func silentAddr(t *testing.T) (addr string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		close(done)
		_ = listener.Close()
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				<-done
				_ = conn.Close()
			}()
		}
	}()
	return listener.Addr().String()
}

func TestRunCommand(t *testing.T) {
	t.Run("the command runs on the appliance", func(t *testing.T) {
		_, server, client := applianceLogin(t)

		err := client.RunCommand("configure")
		if err != nil {
			t.Fatalf("RunCommand: %v", err)
		}
		want := []string{"configure"}
		if !slices.Equal(server.Ran(), want) {
			t.Errorf("ran %v, want %v", server.Ran(), want)
		}
	})

	// "Process exited with status 1" on its own tells an operator nothing.
	t.Run("a failed command carries its output into the error", func(t *testing.T) {
		_, _, client := applianceLogin(t, "configure")

		err := client.RunCommand("configure")
		if err == nil {
			t.Fatal("RunCommand succeeded with a failing command")
		}
		if !errorMentions(t, err, commandFailureOutput) {
			t.Errorf("error = %q, want it to carry %q", err, commandFailureOutput)
		}
		if !errorMentions(t, err, "configure") {
			t.Errorf("error = %q, want it to name the command", err)
		}
	})
}

// applianceLogin is a logged-in client on an appliance that refuses the named
// commands.
func applianceLogin(t *testing.T, failing ...string) (*ApplianceContext, *sshServer, *SSHClient) {
	t.Helper()
	private, public := testKeyPair(t)
	server := startSSHServer(t, public, failing...)
	ac := sshContext(t, private, server.addr)
	client, ready, err := ac.SSHClient(context.TODO(), sshTimeout)
	if err != nil || !ready {
		t.Fatalf("SSHClient: (%v, %v)", ready, err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return ac, server, client
}

func TestRunWithStdin(t *testing.T) {
	t.Run("the payload arrives whole and unaltered", func(t *testing.T) {
		_, server, client := applianceLogin(t)
		payload := []byte("\x00\x01 a payload with an embedded NUL and a \n in it\xff")

		err := client.RunWithStdin("load", bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("RunWithStdin: %v", err)
		}
		got, ran := server.Stdin("load")
		if !ran {
			t.Fatal("the command was never run")
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("stdin = %q, want %q", got, payload)
		}
	})

	// The image is hundreds of megabytes, which is many times the SSH channel
	// window. Writing it has to keep pace with the far side reading it rather
	// than filling the window and stopping.
	t.Run("a payload larger than one channel window does not stall", func(t *testing.T) {
		_, server, client := applianceLogin(t)
		payload := make([]byte, 4<<20)
		if _, err := rand.Read(payload); err != nil {
			t.Fatalf("generate payload: %v", err)
		}

		err := client.RunWithStdin("load", bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("RunWithStdin: %v", err)
		}
		got, _ := server.Stdin("load")
		if !bytes.Equal(got, payload) {
			t.Errorf("stdin is %d bytes, want the %d that were sent", len(got), len(payload))
		}
	})

	// "Process exited with status 1" on its own tells an operator nothing, and
	// this is the one command whose failure they will have to act on.
	t.Run("a failed command carries its output into the error", func(t *testing.T) {
		_, _, client := applianceLogin(t, "load")

		err := client.RunWithStdin("load", strings.NewReader("payload"))
		if err == nil {
			t.Fatal("RunWithStdin succeeded against a command that failed")
		}
		if !errorMentions(t, err, commandFailureOutput) {
			t.Errorf("error = %q, want it to carry %q", err, commandFailureOutput)
		}
		if !errorMentions(t, err, "load") {
			t.Errorf("error = %q, want it to name the command", err)
		}
	})
}

// A command the caller asked as a question has two answers, and both of them
// come back from RunCommand the same way a lost connection does. IsExitError is
// what tells them apart: reading "no" as a failure would fail every deploy that
// has not loaded its image yet, which is all of them, and reading a lost
// connection as "no" would have the caller act on an answer nobody gave.
func TestIsExitError(t *testing.T) {
	const command = "podman image exists something"

	t.Run("a command that exits zero has nothing to classify", func(t *testing.T) {
		_, _, client := applianceLogin(t)

		err := client.RunCommand(command)

		if err != nil {
			t.Fatalf("RunCommand: %v", err)
		}
	})

	t.Run("a command that exits non-zero has answered", func(t *testing.T) {
		_, _, client := applianceLogin(t, command)

		err := client.RunCommand(command)

		if err == nil {
			t.Fatal("RunCommand succeeded against a command that failed")
		}
		if !IsExitError(err) {
			t.Errorf("IsExitError(%v) = false, want a non-zero exit read as an answer", err)
		}
	})

	// Dropped rather than closed from this side: the appliance going away part
	// way through is what the caller has to tell from an answer, and a link this
	// side has already given up on is not that.
	t.Run("a connection that has gone away is not an answer", func(t *testing.T) {
		_, server, client := applianceLogin(t)
		server.dropOn(command)

		err := client.RunCommand(command)

		if err == nil {
			t.Fatal("RunCommand succeeded over a connection that went away")
		}
		if IsExitError(err) {
			t.Errorf("IsExitError(%v) = true, want no answer at all", err)
		}
	})
}

func TestConfigure(t *testing.T) {
	// configureContext is an appliance that has been through LoadImage, which
	// is what Configure renders its unit around.
	configureContext := func(t *testing.T, private []byte, addr string) *ApplianceContext {
		t.Helper()
		ac := sshContext(t, private, addr)
		ac.Appliance.Status.ExporterImage = testLoadedImage
		return ac
	}

	// The install pushes the whole orchestrator binary and restarts the
	// service, which tears down every export it was supervising. Doing that on
	// an appliance already running the right thing would mean doing it on every
	// pass, for as long as the appliance lives.
	t.Run("an appliance already running the supervisor is configured untouched", func(t *testing.T) {
		private, public := testKeyPair(t)
		server := startSSHServer(t, public)
		runner := DeployRunner{context: configureContext(t, private, server.addr)}

		done, err := runner.Configure(context.TODO())
		if err != nil {
			t.Fatalf("Configure: %v", err)
		}
		if !done {
			t.Error("done = false, want the appliance configured")
		}
		for _, unwanted := range []string{
			installBinaryCommand(),
			"systemctl restart " + orchestratorUnit,
		} {
			if slices.Contains(server.Ran(), unwanted) {
				t.Errorf("%q was run against an appliance that was already installed", unwanted)
			}
		}
	})

	t.Run("an appliance without the supervisor has it installed and enabled", func(t *testing.T) {
		private, public := testKeyPair(t)
		server := startSSHServer(t, public, installedProbe(t))
		runner := DeployRunner{context: configureContext(t, private, server.addr)}

		done, err := runner.Configure(context.TODO())

		if err != nil {
			t.Fatalf("Configure: %v", err)
		}
		// Whether it stays up is a question for the next pass. Asked now it
		// would only catch a process that had not yet got round to failing.
		if done {
			t.Error("done = true, want the supervisor checked on a later pass")
		}
		for _, want := range []string{
			installBinaryCommand(),
			"systemctl enable " + orchestratorUnit,
			"systemctl restart " + orchestratorUnit,
		} {
			if !slices.Contains(server.Ran(), want) {
				t.Errorf("%q was not run; ran %v", want, server.Ran())
			}
		}
	})

	// The install pushes the whole binary, so there is a window of megabytes in
	// which the link can go away. Reconciler.Deploy turns any error the runner
	// returns into PhaseDeployFailed, which is absorbing, so treating one i/o
	// timeout as a failure would wedge the CopyAppliance for good over something
	// that would have worked on the next pass.
	t.Run("a connection lost part way through the install is not a failure", func(t *testing.T) {
		private, public := testKeyPair(t)
		server := startSSHServer(t, public, installedProbe(t))
		server.dropOn(writeCommand(applianceCertsDir + "/" + tlsCACert))
		runner := DeployRunner{context: configureContext(t, private, server.addr)}

		done, err := runner.Configure(context.TODO())

		if err != nil {
			t.Fatalf("Configure: %v, want a lost connection to be something to retry", err)
		}
		if done {
			t.Error("done = true, want an install that did not finish reported as unfinished")
		}
	})

	// A unit that is installed but down is worth one more start, and is not
	// worth failing the deploy over: PhaseDeployFailed has no way back, and
	// this is the kind of thing that comes right on its own.
	t.Run("an appliance whose supervisor is down is started and not configured", func(t *testing.T) {
		private, public := testKeyPair(t)
		isActive := "systemctl is-active --quiet " + orchestratorUnit
		server := startSSHServer(t, public, isActive)
		runner := DeployRunner{context: configureContext(t, private, server.addr)}

		done, err := runner.Configure(context.TODO())

		if err != nil {
			t.Fatalf("Configure: %v", err)
		}
		if done {
			t.Error("done = true, want an appliance whose supervisor is down left alone")
		}
		for _, want := range []string{
			// reset-failed first, or a unit that tripped systemd's start limit
			// refuses to start at all.
			"systemctl reset-failed " + orchestratorUnit,
			"systemctl start " + orchestratorUnit,
		} {
			if !slices.Contains(server.Ran(), want) {
				t.Errorf("%q was not run; ran %v", want, server.Ran())
			}
		}
		// Starting is not reinstalling. The binary is already there.
		if slices.Contains(server.Ran(), installBinaryCommand()) {
			t.Error("the binary was sent again to an appliance that already had it")
		}
	})

	// The guest reports its address as soon as it has one, which is before sshd
	// is answering on it. Failing here would fail a deploy that is on track.
	t.Run("an appliance that is not answering yet is not configured and is not a failure", func(t *testing.T) {
		private, _ := testKeyPair(t)
		runner := DeployRunner{context: configureContext(t, private, closedAddr(t))}

		done, err := runner.Configure(context.TODO())
		if err != nil {
			t.Fatalf("Configure: %v", err)
		}
		if done {
			t.Error("done = true, want the appliance left to come up")
		}
	})

	// Waiting on this would park the deploy forever: the key is not going to
	// start working.
	t.Run("an appliance that rejects the key fails the deploy", func(t *testing.T) {
		private, _ := testKeyPair(t)
		_, installed := testKeyPair(t)
		server := startSSHServer(t, installed)
		runner := DeployRunner{context: configureContext(t, private, server.addr)}

		done, err := runner.Configure(context.TODO())
		if err == nil {
			t.Fatal("Configure succeeded against an appliance that rejected the key")
		}
		if done {
			t.Error("done = true, want it not configured")
		}
	})

	// WaitForNetwork does not hand over until there is one, so this is a phase
	// reached out of order rather than an appliance still booting.
	t.Run("an appliance reporting no address fails", func(t *testing.T) {
		private, public := testKeyPair(t)
		server := startSSHServer(t, public)
		ac := configureContext(t, private, server.addr)
		ac.Appliance.Status.Addresses = nil
		runner := DeployRunner{context: ac}

		done, err := runner.Configure(context.TODO())
		if err == nil {
			t.Fatal("Configure succeeded with no address to reach the appliance at")
		}
		if done {
			t.Error("done = true, want it not configured")
		}
		if !errorMentions(t, err, ac.Appliance.Name) {
			t.Errorf("error = %q, want it to name the appliance", err)
		}
	})

	// The secret holds both the login key and the certificates, so there is
	// nothing to do on an appliance without one and nothing it could be left
	// half-installed with.
	t.Run("an appliance whose secret is missing fails before logging in", func(t *testing.T) {
		private, public := testKeyPair(t)
		server := startSSHServer(t, public)
		ac := configureContext(t, private, server.addr)
		ac.ApplianceSecret = nil
		runner := DeployRunner{context: ac}

		done, err := runner.Configure(context.TODO())

		if err == nil {
			t.Fatal("Configure succeeded with no TLS material to install")
		}
		if done {
			t.Error("done = true, want it not configured")
		}
		if ran := server.Ran(); len(ran) != 0 {
			t.Errorf("ran %v, want nothing sent to the appliance", ran)
		}
	})
}
