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
	// once stops the appliance listening after one connection.
	once bool
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
			if server.stopping() {
				_ = listener.Close()
				return
			}
		}
	}()
	return server
}

// stopAfterOne makes the appliance stop listening once it has taken one
// connection, which is sshd going away between two steps of the same pass.
func (r *sshServer) stopAfterOne() {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.once = true
}

// dropOn makes the appliance drop the connection when it is asked to run this
// command, which is the network going away part way through a transfer.
func (r *sshServer) dropOn(command string) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.dropping[command] = true
}

func (r *sshServer) stopping() bool {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return r.once
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
	ac := &ApplianceContext{
		Appliance: appliance,
		Log:       testLog(),
		sshPort:   port,
		TLSSecret: &core.Secret{
			ObjectMeta: meta.ObjectMeta{
				Namespace: appliance.Spec.TLSSecret.Namespace,
				Name:      appliance.Spec.TLSSecret.Name,
			},
			Data: applianceTLS().data,
		},
		orchestratorPath: writeOrchestrator(t),
	}
	if private != nil {
		ac.SSHSecret = &core.Secret{
			ObjectMeta: meta.ObjectMeta{
				Namespace: appliance.Spec.SSHKey.Namespace,
				Name:      appliance.Spec.SSHKey.Name,
			},
			Data: map[string][]byte{sshPrivateKeyData: private},
		}
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

func TestSSHLogin(t *testing.T) {
	t.Run("a login with the installed key succeeds", func(t *testing.T) {
		private, public := testKeyPair(t)
		server := startSSHServer(t, public)
		ac := sshContext(t, private, server.addr)

		client, answered, err := ac.SSHLogin(context.TODO(), "127.0.0.1")
		if err != nil {
			t.Fatalf("SSHLogin: %v", err)
		}
		if !answered || client == nil {
			t.Fatalf("answered = %v, client = %v, want a logged-in client", answered, client)
		}
		_ = client.Close()
	})

	// An appliance that answers and then turns us away is not one to wait for.
	t.Run("a login with a key the appliance does not know fails", func(t *testing.T) {
		private, _ := testKeyPair(t)
		_, installed := testKeyPair(t)
		server := startSSHServer(t, installed)
		ac := sshContext(t, private, server.addr)

		client, answered, err := ac.SSHLogin(context.TODO(), "127.0.0.1")
		if err == nil {
			t.Fatal("SSHLogin succeeded with a key the appliance does not know")
		}
		if answered || client != nil {
			t.Errorf("answered = %v, client = %v, want neither", answered, client)
		}
	})

	t.Run("an address nothing is listening on has not answered", func(t *testing.T) {
		private, _ := testKeyPair(t)
		ac := sshContext(t, private, closedAddr(t))

		client, answered, err := ac.SSHLogin(context.TODO(), "127.0.0.1")
		if err != nil {
			t.Fatalf("SSHLogin: %v, want a closed port to be something to wait for", err)
		}
		if answered || client != nil {
			t.Errorf("answered = %v, client = %v, want neither", answered, client)
		}
	})

	// The provider's key secrets carry both halves under fixed names, so a
	// secret without the private one is the wrong secret.
	t.Run("a secret with no private key fails", func(t *testing.T) {
		_, public := testKeyPair(t)
		server := startSSHServer(t, public)
		ac := sshContext(t, nil, server.addr)
		ac.SSHSecret = &core.Secret{
			ObjectMeta: meta.ObjectMeta{Namespace: "forklift", Name: "appliance-ssh-key"},
			Data:       map[string][]byte{"public-key": []byte("ssh-ed25519 AAAA")},
		}

		_, _, err := ac.SSHLogin(context.TODO(), "127.0.0.1")
		if err == nil {
			t.Fatal("SSHLogin succeeded with no private key in the secret")
		}
		if !errorMentions(t, err, sshPrivateKeyData) {
			t.Errorf("error = %q, want it to name %q", err, sshPrivateKeyData)
		}
	})

	// The secret is tolerated as missing so that a teardown is not blocked, so
	// this is where an operator finds out which one to create.
	t.Run("a missing secret fails by name", func(t *testing.T) {
		ac := sshContext(t, nil, closedAddr(t))

		_, _, err := ac.SSHLogin(context.TODO(), "127.0.0.1")
		if err == nil {
			t.Fatal("SSHLogin succeeded with no secret at all")
		}
		if !errorMentions(t, err, ac.Appliance.Spec.SSHKey.Name) {
			t.Errorf("error = %q, want it to name the secret", err)
		}
	})
}

// The timeout SSHLoginFor is given is what the caller is prepared to spend on
// the transfer the login is for, and LoadImage asks for thirty minutes of it.
// The reconcile's own context has to be able to end it sooner: otherwise one
// appliance that accepts connections and then says nothing holds a reconcile
// worker for the whole half hour.
func TestSSHLoginForHonoursTheContextDeadline(t *testing.T) {
	private, _ := testKeyPair(t)
	ac := sshContext(t, private, silentAddr(t))
	ctx, cancel := context.WithTimeout(context.TODO(), 250*time.Millisecond)
	defer cancel()

	type result struct {
		client   *ssh.Client
		answered bool
		err      error
	}
	finished := make(chan result, 1)
	go func() {
		client, answered, err := ac.SSHLoginFor(ctx, "127.0.0.1", sshTransferTimeout)
		finished <- result{client, answered, err}
	}()

	select {
	case got := <-finished:
		if got.err == nil {
			t.Fatal("SSHLoginFor succeeded against an appliance that never said anything")
		}
		if got.answered || got.client != nil {
			t.Errorf("answered = %v, client = %v, want neither", got.answered, got.client)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SSHLoginFor is still waiting: the login is bounded by its own timeout only")
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

func TestRunCommands(t *testing.T) {
	// login is a client on an appliance that refuses the named commands.
	login := func(t *testing.T, failing ...string) (*ApplianceContext, *sshServer, *ssh.Client) {
		t.Helper()
		private, public := testKeyPair(t)
		server := startSSHServer(t, public, failing...)
		ac := sshContext(t, private, server.addr)
		client, answered, err := ac.SSHLogin(context.TODO(), "127.0.0.1")
		if err != nil || !answered {
			t.Fatalf("SSHLogin: (%v, %v)", answered, err)
		}
		t.Cleanup(func() { _ = client.Close() })
		return ac, server, client
	}

	t.Run("every command runs in order", func(t *testing.T) {
		ac, server, client := login(t)

		err := ac.RunCommands(client, "first", "second", "third")
		if err != nil {
			t.Fatalf("RunCommands: %v", err)
		}
		want := []string{"first", "second", "third"}
		if !slices.Equal(server.Ran(), want) {
			t.Errorf("ran %v, want %v", server.Ran(), want)
		}
	})

	// The commands configure one appliance in sequence, so running the rest
	// after one has failed would configure it half way and call it done.
	t.Run("the first command to fail stops the rest", func(t *testing.T) {
		ac, server, client := login(t, "second")

		err := ac.RunCommands(client, "first", "second", "third")
		if err == nil {
			t.Fatal("RunCommands succeeded with a failing command")
		}
		want := []string{"first", "second"}
		if !slices.Equal(server.Ran(), want) {
			t.Errorf("ran %v, want %v", server.Ran(), want)
		}
	})

	// "Process exited with status 1" on its own tells an operator nothing.
	t.Run("a failed command carries its output into the error", func(t *testing.T) {
		ac, _, client := login(t, "configure")

		err := ac.RunCommands(client, "configure")
		if err == nil {
			t.Fatal("RunCommands succeeded with a failing command")
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
func applianceLogin(t *testing.T, failing ...string) (*ApplianceContext, *sshServer, *ssh.Client) {
	t.Helper()
	private, public := testKeyPair(t)
	server := startSSHServer(t, public, failing...)
	ac := sshContext(t, private, server.addr)
	client, answered, err := ac.SSHLogin(context.TODO(), "127.0.0.1")
	if err != nil || !answered {
		t.Fatalf("SSHLogin: (%v, %v)", answered, err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return ac, server, client
}

func TestRunWithStdin(t *testing.T) {
	t.Run("the payload arrives whole and unaltered", func(t *testing.T) {
		ac, server, client := applianceLogin(t)
		payload := []byte("\x00\x01 a payload with an embedded NUL and a \n in it\xff")

		err := ac.RunWithStdin(client, "load", bytes.NewReader(payload))
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
		ac, server, client := applianceLogin(t)
		payload := make([]byte, 4<<20)
		if _, err := rand.Read(payload); err != nil {
			t.Fatalf("generate payload: %v", err)
		}

		err := ac.RunWithStdin(client, "load", bytes.NewReader(payload))
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
		ac, _, client := applianceLogin(t, "load")

		err := ac.RunWithStdin(client, "load", strings.NewReader("payload"))
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

// A probe asks a question, so the two answers have to be told apart from not
// getting one. Treating "no" as a failure would fail every deploy that has not
// loaded its image yet, which is all of them.
func TestProbe(t *testing.T) {
	t.Run("a command that exits zero answers yes", func(t *testing.T) {
		ac, _, client := applianceLogin(t)

		ok, err := ac.Probe(client, "podman image exists something")
		if err != nil {
			t.Fatalf("Probe: %v", err)
		}
		if !ok {
			t.Error("ok = false, want the appliance to have answered yes")
		}
	})

	t.Run("a command that exits non-zero answers no and is not a failure", func(t *testing.T) {
		ac, _, client := applianceLogin(t, "podman image exists something")

		ok, err := ac.Probe(client, "podman image exists something")
		if err != nil {
			t.Fatalf("Probe: %v, want a non-zero exit to be an answer", err)
		}
		if ok {
			t.Error("ok = true, want the appliance to have answered no")
		}
	})

	t.Run("a connection that has gone away is not an answer", func(t *testing.T) {
		ac, _, client := applianceLogin(t)
		if err := client.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}

		ok, err := ac.Probe(client, "podman image exists something")
		if err == nil {
			t.Fatal("Probe succeeded over a closed connection")
		}
		if ok {
			t.Error("ok = true, want no answer at all")
		}
	})
}

func TestConfigure(t *testing.T) {
	// configureContext is an appliance that has been through LoadImage, which
	// is what Configure renders its unit around.
	configureContext := func(t *testing.T, private []byte, addr string) *ApplianceContext {
		t.Helper()
		ac := sshContext(t, private, addr)
		ac.Appliance.Status.LoadedImage = testLoadedImage
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
		server.dropOn(writeCommand(applianceCertsDir+"/"+tlsCACert, "077"))
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

	// Without the certificates the appliance cannot serve anything, and the
	// controller cannot read what it serves. Sending everything else first and
	// discovering it afterwards would leave a half-installed appliance.
	t.Run("an appliance whose TLS secret is missing fails before logging in", func(t *testing.T) {
		private, public := testKeyPair(t)
		server := startSSHServer(t, public)
		ac := configureContext(t, private, server.addr)
		ac.TLSSecret = nil
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
