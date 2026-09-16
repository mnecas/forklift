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
	// failing names the commands the appliance refuses to run.
	failing map[string]bool
	mutex   sync.Mutex
	ran     []sshCommand
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
	server := &sshServer{failing: map[string]bool{}}
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
		go r.session(channel, requests)
	}
}

// session answers one exec request and closes, which is what one command over
// its own session looks like from the appliance's side.
func (r *sshServer) session(channel ssh.Channel, requests <-chan *ssh.Request) {
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
		status := r.run(payload.Command, channel)
		_, _ = channel.SendRequest(
			"exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
		return
	}
}

// run reads the command's standard input to the end and then answers. Draining
// first is what a real command does, and a server that replied without draining
// would hang any client sending more than fits in the channel's window.
func (r *sshServer) run(command string, channel ssh.Channel) (status uint32) {
	stdin, _ := io.ReadAll(channel)
	r.mutex.Lock()
	r.ran = append(r.ran, sshCommand{command: command, stdin: stdin})
	refused := r.failing[command]
	r.mutex.Unlock()
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
	t.Run("an appliance that answers is configured", func(t *testing.T) {
		private, public := testKeyPair(t)
		server := startSSHServer(t, public)
		runner := DeployRunner{context: sshContext(t, private, server.addr)}

		done, err := runner.Configure(context.TODO())
		if err != nil {
			t.Fatalf("Configure: %v", err)
		}
		if !done {
			t.Error("done = false, want the appliance configured")
		}
		if !slices.Equal(server.Ran(), applianceConfigCommands) {
			t.Errorf("ran %v, want %v", server.Ran(), applianceConfigCommands)
		}
	})

	// The guest reports its address as soon as it has one, which is before sshd
	// is answering on it. Failing here would fail a deploy that is on track.
	t.Run("an appliance that is not answering yet is not configured and is not a failure", func(t *testing.T) {
		private, _ := testKeyPair(t)
		runner := DeployRunner{context: sshContext(t, private, closedAddr(t))}

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
		runner := DeployRunner{context: sshContext(t, private, server.addr)}

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
		ac := sshContext(t, private, server.addr)
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
}
