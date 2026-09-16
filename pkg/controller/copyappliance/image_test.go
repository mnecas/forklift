package copyappliance

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	stdlog "log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// testRegistry serves a registry out of memory and returns the host to address
// it by. It is on the loopback address, which go-containerregistry addresses
// over plain HTTP; what the cluster's own registry needs on top of that is
// clusterRegistry's job, and tested there.
func testRegistry(t *testing.T, handler http.Handler) string {
	t.Helper()
	if handler == nil {
		handler = registry.New(registry.Logger(stdlog.New(io.Discard, "", 0)))
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://")
}

// pushImage puts a synthetic image in the registry and returns the spec that
// pulls it back.
func pushImage(t *testing.T, host string) (spec string, img v1.Image) {
	t.Helper()
	img, err := random.Image(256, 2)
	if err != nil {
		t.Fatalf("random image: %v", err)
	}
	ref, err := name.NewTag(host + "/forklift/copy-appliance:latest")
	if err != nil {
		t.Fatalf("tag: %v", err)
	}
	err = remote.Write(ref, img)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	return ref.Name(), img
}

// digestOf is an image's manifest digest, which is how two images are told
// apart here.
func digestOf(t *testing.T, img v1.Image) string {
	t.Helper()
	digest, err := img.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	return digest.String()
}

func TestRegistryImage(t *testing.T) {
	t.Run("an image in the registry is the image that comes back", func(t *testing.T) {
		host := testRegistry(t, nil)
		spec, want := pushImage(t, host)

		got, err := registryImage(context.TODO(), spec)
		if err != nil {
			t.Fatalf("registryImage: %v", err)
		}
		if digestOf(t, got) != digestOf(t, want) {
			t.Errorf("digest = %s, want %s", digestOf(t, got), digestOf(t, want))
		}
	})

	// The tag is stamped into the CR from a setting, so a tag that was never
	// built has to say which one it was looking for.
	t.Run("an image that is not there fails naming it", func(t *testing.T) {
		host := testRegistry(t, nil)
		spec := host + "/forklift/copy-appliance:never-built"

		_, err := registryImage(context.TODO(), spec)
		if err == nil {
			t.Fatal("registryImage succeeded for an image that was never pushed")
		}
		if !errorMentions(t, err, spec) {
			t.Errorf("error = %q, want it to name %q", err, spec)
		}
	})

	t.Run("something that is not a reference fails naming it", func(t *testing.T) {
		_, err := registryImage(context.TODO(), "NOT A REFERENCE")
		if err == nil {
			t.Fatal("registryImage succeeded for something that is not a reference")
		}
		if !errorMentions(t, err, "NOT A REFERENCE") {
			t.Errorf("error = %q, want it to name what it was given", err)
		}
	})
}

// testCAFile writes a server's certificate out in the form the service CA
// arrives in.
func testCAFile(t *testing.T, certificate *x509.Certificate) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "service-ca.crt")
	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("write CA: %v", err)
	}
	return path
}

// unrelatedCertificate is a CA that signed nothing in this test. Every
// httptest TLS server shares one certificate, so telling "trusted because the
// CA is in the file" from "trusted regardless" needs a certificate from
// somewhere else.
func unrelatedCertificate(t *testing.T) *x509.Certificate {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "unrelated"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return certificate
}

// testTokenFile writes a bearer token out in the form the service account's
// projected volume holds it.
func testTokenFile(t *testing.T, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	return path
}

// The internal registry's serving certificate is signed by the service-serving
// signer, which is in no system trust store. If the CA file were not reaching
// the transport, every pull would fail on the cluster and nowhere else.
func TestRegistryTransport(t *testing.T) {
	server := httptest.NewTLSServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(server.Close)

	t.Run("a server signed by the CA in the file is trusted", func(t *testing.T) {
		transport, err := registryTransport(testCAFile(t, server.Certificate()))
		if err != nil {
			t.Fatalf("registryTransport: %v", err)
		}
		response, err := (&http.Client{Transport: transport}).Get(server.URL)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		_ = response.Body.Close()
	})

	// The other half of the same claim: the CA in the file is what makes the
	// connection work, and not a verification that was turned off.
	t.Run("a server the CA did not sign is not trusted", func(t *testing.T) {
		transport, err := registryTransport(testCAFile(t, unrelatedCertificate(t)))
		if err != nil {
			t.Fatalf("registryTransport: %v", err)
		}
		response, err := (&http.Client{Transport: transport}).Get(server.URL)
		if err == nil {
			_ = response.Body.Close()
			t.Fatal("a server signed by an unrelated CA was trusted")
		}
	})

	t.Run("a CA file that is not there fails naming it", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "absent.crt")

		_, err := registryTransport(path)
		if err == nil {
			t.Fatal("registryTransport succeeded with no CA file")
		}
		if !errorMentions(t, err, path) {
			t.Errorf("error = %q, want it to name %q", err, path)
		}
	})

	// A file that holds something other than a certificate would otherwise
	// leave a pool that silently trusts only the system roots, and the failure
	// would surface later as an unrelated TLS error.
	t.Run("a CA file holding no certificate fails naming it", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "service-ca.crt")
		if err := os.WriteFile(path, []byte("not a certificate"), 0o600); err != nil {
			t.Fatalf("write CA: %v", err)
		}

		_, err := registryTransport(path)
		if err == nil {
			t.Fatal("registryTransport succeeded with a CA file holding no certificate")
		}
		if !errorMentions(t, err, path) {
			t.Errorf("error = %q, want it to name %q", err, path)
		}
	})
}

// The controller's own token is the only credential it has for the internal
// registry. A pull that sent none would fail with a 401 on the cluster and
// nowhere else.
func TestClusterRegistry(t *testing.T) {
	const token = "a-service-account-token"

	t.Run("the service account token is sent as the registry password", func(t *testing.T) {
		var authorization string
		inner := registry.New(registry.Logger(stdlog.New(io.Discard, "", 0)))
		host := testRegistry(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if header := req.Header.Get("Authorization"); header != "" {
				authorization = header
			}
			inner.ServeHTTP(w, req)
		}))
		spec, _ := pushImage(t, host)
		authorization = ""

		tls := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		t.Cleanup(tls.Close)
		options, err := clusterRegistry(
			testCAFile(t, tls.Certificate()), testTokenFile(t, token))
		if err != nil {
			t.Fatalf("clusterRegistry: %v", err)
		}

		_, err = registryImage(context.TODO(), spec, options...)
		if err != nil {
			t.Fatalf("registryImage: %v", err)
		}
		user, password, ok := parseBasic(authorization)
		if !ok {
			t.Fatalf("Authorization = %q, want basic credentials", authorization)
		}
		if user != registryUser || password != token {
			t.Errorf("credentials = (%q, %q), want (%q, the token)", user, password, registryUser)
		}
	})

	t.Run("a token file that is not there fails naming it", func(t *testing.T) {
		tls := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		t.Cleanup(tls.Close)
		path := filepath.Join(t.TempDir(), "absent")

		_, err := clusterRegistry(testCAFile(t, tls.Certificate()), path)
		if err == nil {
			t.Fatal("clusterRegistry succeeded with no token file")
		}
		if !errorMentions(t, err, path) {
			t.Errorf("error = %q, want it to name %q", err, path)
		}
	})
}

// parseBasic pulls the credentials back out of an Authorization header.
func parseBasic(header string) (user, password string, ok bool) {
	request := &http.Request{Header: http.Header{"Authorization": []string{header}}}
	return request.BasicAuth()
}

// The appliance is asked about the reference by name, so the reference has to
// change when the image behind the tag does. Otherwise a rebuilt appliance
// image is never picked up: the store still answers to the old name.
func TestLoadedReference(t *testing.T) {
	first, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random image: %v", err)
	}
	second, err := random.Image(512, 1)
	if err != nil {
		t.Fatalf("random image: %v", err)
	}

	ref, err := loadedReference(first)
	if err != nil {
		t.Fatalf("loadedReference: %v", err)
	}
	if ref.Repository.Name() != loadedImageRepository {
		t.Errorf("repository = %q, want %q", ref.Repository.Name(), loadedImageRepository)
	}
	digest, err := first.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if !strings.HasPrefix(digest.Hex, ref.TagStr()) || ref.TagStr() == "" {
		t.Errorf("tag = %q, want a prefix of the digest %q", ref.TagStr(), digest.Hex)
	}

	other, err := loadedReference(second)
	if err != nil {
		t.Fatalf("loadedReference: %v", err)
	}
	if other.Name() == ref.Name() {
		t.Errorf("both images are %q, want two different references", ref.Name())
	}
}

func TestStreamImage(t *testing.T) {
	t.Run("what the appliance is fed reads back as the image", func(t *testing.T) {
		ac, server, client := applianceLogin(t)
		img, err := random.Image(4096, 3)
		if err != nil {
			t.Fatalf("random image: %v", err)
		}
		ref, err := loadedReference(img)
		if err != nil {
			t.Fatalf("loadedReference: %v", err)
		}

		err = ac.streamImage(client, img, ref)
		if err != nil {
			t.Fatalf("streamImage: %v", err)
		}

		archive, ran := server.Stdin(applianceLoadCommand)
		if !ran {
			t.Fatalf("ran %v, want %q", server.Ran(), applianceLoadCommand)
		}
		opener := func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(archive)), nil
		}
		// The name the archive files the image under is the name the appliance
		// is asked about on the next pass, and it is recorded from ref.Name().
		// If those two ever disagree the probe never matches and every pass
		// sends the image again.
		manifest, err := tarball.LoadManifest(opener)
		if err != nil {
			t.Fatalf("read back the manifest: %v", err)
		}
		if len(manifest) != 1 || !slices.Contains(manifest[0].RepoTags, ref.Name()) {
			t.Errorf("archive holds %+v, want one image tagged %q", manifest, ref.Name())
		}
		// By position rather than by tag. tarball's own lookup re-parses the
		// RepoTag with a default registry and would not find a reference that
		// deliberately has none; podman matches the name as written.
		loaded, err := tarball.Image(opener, nil)
		if err != nil {
			t.Fatalf("read back the archive: %v", err)
		}
		config, err := loaded.ConfigName()
		if err != nil {
			t.Fatalf("config: %v", err)
		}
		want, err := img.ConfigName()
		if err != nil {
			t.Fatalf("config: %v", err)
		}
		if config != want {
			t.Errorf("config = %s, want %s", config, want)
		}
		layers, err := loaded.Layers()
		if err != nil {
			t.Fatalf("layers: %v", err)
		}
		if len(layers) != 3 {
			t.Errorf("layers = %d, want 3", len(layers))
		}
	})

	// A load that fails is the end of the deploy, so it has to say what the
	// appliance said rather than that a pipe broke.
	t.Run("a load the appliance refuses carries its output into the error", func(t *testing.T) {
		ac, _, client := applianceLogin(t, applianceLoadCommand)
		img, err := random.Image(4096, 2)
		if err != nil {
			t.Fatalf("random image: %v", err)
		}
		ref, err := loadedReference(img)
		if err != nil {
			t.Fatalf("loadedReference: %v", err)
		}

		err = ac.streamImage(client, img, ref)
		if err == nil {
			t.Fatal("streamImage succeeded against an appliance that refused the load")
		}
		if !errorMentions(t, err, commandFailureOutput) {
			t.Errorf("error = %q, want it to carry %q", err, commandFailureOutput)
		}
	})
}

func TestLoadImage(t *testing.T) {
	// The image is hundreds of megabytes, so a pass that re-entered the step
	// and sent it again would cost as much as the first one did. Asking the
	// appliance is the whole of what makes the step idempotent.
	t.Run("an image the appliance already has is not sent again", func(t *testing.T) {
		private, public := testKeyPair(t)
		server := startSSHServer(t, public)
		ac := sshContext(t, private, server.addr)
		ac.Appliance.Status.LoadedImage = loadedImageRepository + ":0123456789ab"
		runner := DeployRunner{context: ac}

		done, err := runner.LoadImage(context.TODO())
		if err != nil {
			t.Fatalf("LoadImage: %v", err)
		}
		if !done {
			t.Error("done = false, want the image the appliance already has to count")
		}
		if slices.Contains(server.Ran(), applianceLoadCommand) {
			t.Errorf("ran %v, want no load at all", server.Ran())
		}
	})

	// sshd can go away between passes, and an appliance that is not answering
	// is one to come back to rather than one to fail the deploy over.
	t.Run("an appliance that is not answering is not a failure", func(t *testing.T) {
		private, _ := testKeyPair(t)
		runner := DeployRunner{context: sshContext(t, private, closedAddr(t))}

		done, err := runner.LoadImage(context.TODO())
		if err != nil {
			t.Fatalf("LoadImage: %v", err)
		}
		if done {
			t.Error("done = true, want the appliance left to come back")
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

		done, err := runner.LoadImage(context.TODO())
		if err == nil {
			t.Fatal("LoadImage succeeded with no address to reach the appliance at")
		}
		if done {
			t.Error("done = true, want nothing loaded")
		}
		if !errorMentions(t, err, ac.Appliance.Name) {
			t.Errorf("error = %q, want it to name the appliance", err)
		}
	})

	t.Run("the image is sent and the reference recorded", func(t *testing.T) {
		ac, server, client := applianceLogin(t)
		spec, img := pushImage(t, testRegistry(t, nil))
		runner := DeployRunner{context: ac}

		done, err := runner.loadImage(context.TODO(), client, spec)
		if err != nil {
			t.Fatalf("loadImage: %v", err)
		}
		if !done {
			t.Error("done = false, want the image loaded")
		}
		want, err := loadedReference(img)
		if err != nil {
			t.Fatalf("loadedReference: %v", err)
		}
		if ac.Appliance.Status.LoadedImage != want.Name() {
			t.Errorf("recorded %q, want %q", ac.Appliance.Status.LoadedImage, want.Name())
		}
		if _, ran := server.Stdin(applianceLoadCommand); !ran {
			t.Errorf("ran %v, want %q", server.Ran(), applianceLoadCommand)
		}
	})

	// A load that was cut off part way can still leave the image in the store,
	// so the next pass has to know which reference to ask about even though
	// this one failed.
	t.Run("a load that fails still records what to ask about", func(t *testing.T) {
		ac, _, client := applianceLogin(t, applianceLoadCommand)
		spec, img := pushImage(t, testRegistry(t, nil))
		runner := DeployRunner{context: ac}

		done, err := runner.loadImage(context.TODO(), client, spec)
		if err == nil {
			t.Fatal("loadImage succeeded against an appliance that refused the load")
		}
		if done {
			t.Error("done = true, want the load to have failed")
		}
		if !errorMentions(t, err, commandFailureOutput) {
			t.Errorf("error = %q, want it to carry %q", err, commandFailureOutput)
		}
		want, err := loadedReference(img)
		if err != nil {
			t.Fatalf("loadedReference: %v", err)
		}
		if ac.Appliance.Status.LoadedImage != want.Name() {
			t.Errorf("recorded %q, want %q", ac.Appliance.Status.LoadedImage, want.Name())
		}
	})
}
