package copyappliance

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	stdlog "log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	imagev1 "github.com/openshift/api/image/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

// testRegistry serves a registry out of memory and returns the host to address
// it by. It is on the loopback address, which go-containerregistry reaches over
// plain HTTP; the TLS and auth the cluster's registry needs are tested against
// ClusterRegistry itself.
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
	// The spec and the options are arguments, so no field is read here.
	cluster := &ClusterRegistry{}

	t.Run("an image in the registry is the image that comes back", func(t *testing.T) {
		host := testRegistry(t, nil)
		spec, want := pushImage(t, host)

		got, err := cluster.registryImage(context.TODO(), spec)
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

		_, err := cluster.registryImage(context.TODO(), spec)
		if err == nil {
			t.Fatal("registryImage succeeded for an image that was never pushed")
		}
		if !errorMentions(t, err, spec) {
			t.Errorf("error = %q, want it to name %q", err, spec)
		}
	})

	t.Run("something that is not a reference fails naming it", func(t *testing.T) {
		_, err := cluster.registryImage(context.TODO(), "NOT A REFERENCE")
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
	// The CA file is an argument, so no field is read here.
	cluster := &ClusterRegistry{}
	server := httptest.NewTLSServer(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(server.Close)

	t.Run("a server signed by the CA in the file is trusted", func(t *testing.T) {
		transport, err := cluster.registryTransport(testCAFile(t, server.Certificate()))
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
		transport, err := cluster.registryTransport(testCAFile(t, unrelatedCertificate(t)))
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

		_, err := cluster.registryTransport(path)
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

		_, err := cluster.registryTransport(path)
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
func TestClusterRegistryOptions(t *testing.T) {
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
		cluster := &ClusterRegistry{
			CAFile:    testCAFile(t, tls.Certificate()),
			TokenFile: testTokenFile(t, token),
		}
		options, err := cluster.options()
		if err != nil {
			t.Fatalf("options: %v", err)
		}

		_, err = cluster.registryImage(context.TODO(), spec, options...)
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
		absent := filepath.Join(t.TempDir(), "absent")
		cluster := &ClusterRegistry{
			CAFile:    testCAFile(t, tls.Certificate()),
			TokenFile: absent,
		}

		_, err := cluster.options()
		if err == nil {
			t.Fatal("options succeeded with no token file")
		}
		if !errorMentions(t, err, absent) {
			t.Errorf("error = %q, want it to name %q", err, absent)
		}
	})
}

// parseBasic pulls the credentials back out of an Authorization header.
func parseBasic(header string) (user, password string, ok bool) {
	request := &http.Request{Header: http.Header{"Authorization": []string{header}}}
	return request.BasicAuth()
}

// testClusterRegistry is a ClusterRegistry against a stand-in API server that
// serves the given ImageStreamTags in the given namespace and answers
// everything else as not found. The registry the specs point at is a real one.
func testClusterRegistry(t *testing.T, namespace string, specByTag map[string]string) *ClusterRegistry {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		prefix := path.Join("/apis/image.openshift.io/v1/namespaces", namespace, "imagestreamtags") + "/"
		tag := strings.TrimPrefix(req.URL.Path, prefix)
		spec, found := specByTag[tag]
		if !strings.HasPrefix(req.URL.Path, prefix) || !found {
			writeStatus(w, http.StatusNotFound, meta.StatusReasonNotFound)
			return
		}
		writeJSON(w, http.StatusOK, &imagev1.ImageStreamTag{
			ObjectMeta: meta.ObjectMeta{Namespace: namespace, Name: tag},
			Image:      imagev1.Image{DockerImageReference: spec},
		})
	}))
	t.Cleanup(server.Close)

	cluster, err := newClusterRegistry(
		&rest.Config{Host: server.URL},
		namespace,
		testCAFile(t, unrelatedCertificate(t)),
		testTokenFile(t, "a-service-account-token"))
	if err != nil {
		t.Fatalf("newClusterRegistry: %v", err)
	}
	return cluster
}

// writeStatus writes a failure the way the API server reports one, so the
// client turns it back into an error carrying the reason.
func writeStatus(w http.ResponseWriter, code int, reason meta.StatusReason) {
	writeJSON(w, code, &meta.Status{
		TypeMeta: meta.TypeMeta{APIVersion: "v1", Kind: "Status"},
		Status:   meta.StatusFailure,
		Code:     int32(code),
		Reason:   reason,
	})
}

func writeJSON(w http.ResponseWriter, code int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func TestClusterRegistryPullSpec(t *testing.T) {
	const namespace = "forklift"

	t.Run("an ImageStreamTag resolves to the reference the image stream records", func(t *testing.T) {
		const want = "image-registry.openshift-image-registry.svc:5000/forklift/copy-appliance@sha256:abc"
		cluster := testClusterRegistry(t, namespace, map[string]string{"copy-appliance:latest": want})

		spec, err := cluster.PullSpec(context.TODO(), "copy-appliance:latest")
		if err != nil {
			t.Fatalf("PullSpec: %v", err)
		}
		if spec != want {
			t.Errorf("spec = %q, want %q", spec, want)
		}
	})

	// The tag comes from a setting stamped into the CR, so an image that was
	// never built has to say which namespace and which tag it looked in.
	t.Run("a tag that was never built fails naming the namespace and the tag", func(t *testing.T) {
		cluster := testClusterRegistry(t, namespace, nil)

		_, err := cluster.PullSpec(context.TODO(), "copy-appliance:never-built")
		if err == nil {
			t.Fatal("PullSpec succeeded for a tag that was never built")
		}
		if !errorMentions(t, err, "not in the cluster registry") {
			t.Errorf("error = %q, want the named error rather than a wrapped 404", err)
		}
		if !errorMentions(t, err, namespace) {
			t.Errorf("error = %q, want it to name %q", err, namespace)
		}
		if !errorMentions(t, err, "copy-appliance:never-built") {
			t.Errorf("error = %q, want it to name the tag", err)
		}
	})

	// A refusal is not a missing image, and reporting it as one would send an
	// operator looking for a build that is already there.
	t.Run("an API server that refuses the request fails naming the tag", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeStatus(w, http.StatusForbidden, meta.StatusReasonForbidden)
		}))
		t.Cleanup(server.Close)
		cluster, err := newClusterRegistry(&rest.Config{Host: server.URL}, namespace, "", "")
		if err != nil {
			t.Fatalf("newClusterRegistry: %v", err)
		}

		_, err = cluster.PullSpec(context.TODO(), "copy-appliance:latest")
		if err == nil {
			t.Fatal("PullSpec succeeded against an API server that refused it")
		}
		if errorMentions(t, err, "not in the cluster registry") {
			t.Errorf("error = %q, want a refusal rather than the not-built error", err)
		}
		if !errorMentions(t, err, "copy-appliance:latest") {
			t.Errorf("error = %q, want it to name the tag", err)
		}
	})
}

// Resolving and pulling are one step from the caller's side, and the pull spec
// the cluster hands back is the only thing that joins them.
func TestClusterRegistryImage(t *testing.T) {
	spec, want := pushImage(t, testRegistry(t, nil))
	cluster := testClusterRegistry(t, "forklift", map[string]string{"copy-appliance:latest": spec})

	got, err := cluster.Image(context.TODO(), "copy-appliance:latest")
	if err != nil {
		t.Fatalf("Image: %v", err)
	}
	if digestOf(t, got) != digestOf(t, want) {
		t.Errorf("digest = %s, want %s", digestOf(t, got), digestOf(t, want))
	}
}
