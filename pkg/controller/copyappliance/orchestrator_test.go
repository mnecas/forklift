package copyappliance

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kubev2v/forklift/pkg/nbd-container/announce"
)

// testLoadedImage is what LoadImage would have recorded by the time Configure
// runs, and so what the unit is rendered around.
const testLoadedImage = "localhost/forklift-copy-appliance:abc123def456"

// testOrchestratorBinary stands in for the binary the controller image ships.
// Its content is fixed rather than random because the install probe hashes it:
// a test that wrote something different each time would never see an appliance
// as already installed.
const testOrchestratorBinary = "#!/bin/sh\n# not really a binary\n"

func TestRenderUnit(t *testing.T) {
	t.Run("the unit describes this appliance", func(t *testing.T) {
		ac := sshContext(t, nil, "127.0.0.1:22")
		ac.Appliance.Status.ExporterImage = testLoadedImage

		unit, err := ac.renderUnit()
		if err != nil {
			t.Fatalf("renderUnit: %v", err)
		}

		// The image is the only part that differs per appliance, and getting it
		// wrong means a supervisor that starts and then cannot run anything.
		for _, want := range []string{
			"-image=" + testLoadedImage,
			"-certs-dir=" + applianceCertsDir,
			"-listen=:" + ac.announcePort(),
			orchestratorBinary,
			// Without this the supervisor does not come back after a reboot,
			// which is the reason for using systemd at all.
			"WantedBy=multi-user.target",
			"Restart=always",
		} {
			if !strings.Contains(unit, want) {
				t.Errorf("the unit does not carry %q:\n%s", want, unit)
			}
		}
	})

	// The image reference is the one thing the unit cannot be written without,
	// so rendering has to refuse rather than produce "-image=".
	t.Run("an appliance with no loaded image has no unit", func(t *testing.T) {
		ac := sshContext(t, nil, "127.0.0.1:22")

		_, err := ac.renderUnit()

		if err == nil {
			t.Error("rendered a unit for an appliance with no image")
		}
	})
}

func TestInstallOrchestrator(t *testing.T) {
	ac, server, client := applianceLogin(t)
	ac.Appliance.Status.ExporterImage = testLoadedImage
	unit, certs := testInstallInputs(t, ac)

	err := ac.InstallOrchestrator(client, unit, certs)
	if err != nil {
		t.Fatalf("InstallOrchestrator: %v", err)
	}

	t.Run("the certificates are the ones from the secret", func(t *testing.T) {
		for _, name := range []string{tlsCACert, tlsServerCert, tlsServerKey} {
			got, ran := server.Stdin(writeCommand(applianceCertsDir+"/"+name, "077"))
			if !ran {
				t.Errorf("%s was never written", name)
				continue
			}
			if !bytes.Equal(got, certs[name]) {
				t.Errorf("%s = %q, want the secret's", name, got)
			}
		}
	})

	// nbdkit refuses a server key any wider than its owner and crash-loops on
	// one, so the mode is not housekeeping.
	t.Run("the private key is never world readable", func(t *testing.T) {
		if _, ran := server.Stdin(writeCommand(applianceCertsDir+"/"+tlsServerKey, "077")); !ran {
			t.Error("the server key was not written under a 077 umask")
		}
	})

	t.Run("the binary is the one the controller ships", func(t *testing.T) {
		got, ran := server.Stdin(installBinaryCommand())
		if !ran {
			t.Fatal("the orchestrator binary was never sent")
		}
		if string(got) != testOrchestratorBinary {
			t.Errorf("binary = %q, want the controller's copy", got)
		}
	})

	t.Run("the unit is the rendered one", func(t *testing.T) {
		got, ran := server.Stdin(writeCommand(orchestratorService, "022"))
		if !ran {
			t.Fatal("the unit was never written")
		}
		if string(got) != unit {
			t.Errorf("unit = %q, want the rendered one", got)
		}
	})

	// Enabling is what survives a reboot; reset-failed is what keeps a unit
	// that tripped systemd's start limit from refusing to start ever again.
	t.Run("the service is enabled and started", func(t *testing.T) {
		ran := server.Ran()
		for _, want := range []string{
			"systemctl daemon-reload",
			"systemctl enable " + orchestratorUnit,
			"systemctl reset-failed " + orchestratorUnit,
			"systemctl restart " + orchestratorUnit,
		} {
			if !slices.Contains(ran, want) {
				t.Errorf("%q was not run; ran %v", want, ran)
			}
		}
	})
}

func TestRestartOrchestrator(t *testing.T) {
	ac, server, client := applianceLogin(t)
	err := ac.RestartOrchestrator(client)
	if err != nil {
		t.Fatalf("RestartOrchestrator: %v", err)
	}
	ran := server.Ran()
	for _, want := range []string{
		"systemctl reset-failed " + orchestratorUnit,
		"systemctl restart " + orchestratorUnit,
	} {
		if !slices.Contains(ran, want) {
			t.Errorf("%q was not run; ran %v", want, ran)
		}
	}
}

func TestOrchestratorInstalled(t *testing.T) {
	t.Run("an appliance that matches is already installed", func(t *testing.T) {
		ac, _, client := applianceLogin(t)
		ac.Appliance.Status.ExporterImage = testLoadedImage
		unit, certs := testInstallInputs(t, ac)

		installed, err := ac.OrchestratorInstalled(client, unit, certs)

		if err != nil {
			t.Fatalf("OrchestratorInstalled: %v", err)
		}
		if !installed {
			t.Error("an appliance that answered yes was read as not installed")
		}
	})

	// A checksum that does not match is an answer, not a failure: it is how a
	// first deploy and an appliance holding an older build both look.
	t.Run("an appliance that does not match is not installed", func(t *testing.T) {
		probe := installedProbe(t)
		ac, _, client := applianceLogin(t, probe)
		ac.Appliance.Status.ExporterImage = testLoadedImage
		unit, certs := testInstallInputs(t, ac)

		installed, err := ac.OrchestratorInstalled(client, unit, certs)

		if err != nil {
			t.Fatalf("OrchestratorInstalled: %v", err)
		}
		if installed {
			t.Error("an appliance that answered no was read as installed")
		}
	})
}

// --- fixtures ---

// testInstallInputs is what Configure would have worked out before logging in.
func testInstallInputs(t *testing.T, ac *ApplianceContext) (unit string, certs map[string][]byte) {
	t.Helper()
	unit, err := ac.renderUnit()
	if err != nil {
		t.Fatalf("renderUnit: %v", err)
	}
	certs, err = ac.ServerTLS()
	if err != nil {
		t.Fatalf("ServerTLS: %v", err)
	}
	return
}

// installedProbe is the command Configure asks "is this already installed?"
// with, so that a test can tell the appliance to answer no to it. It does not
// depend on the appliance's address, which is what lets it be worked out before
// there is a server to point at.
func installedProbe(t *testing.T) (command string) {
	t.Helper()
	ac := sshContext(t, nil, "127.0.0.1:22")
	ac.Appliance.Status.ExporterImage = testLoadedImage
	unit, certs := testInstallInputs(t, ac)
	manifest, err := ac.manifest(unit, certs)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	return "printf '%s' " + shellQuote(manifest) + " | sha256sum --status -c -"
}

// writeCommand and installBinaryCommand mirror what InstallOrchestrator sends,
// so a test can ask the appliance what it was given for a particular file.
func writeCommand(path, umask string) (command string) {
	return "(umask " + umask + " && cat > " + path + ")"
}

func installBinaryCommand() (command string) {
	return "(umask 022 && cat > " + orchestratorStaging + ") && " +
		"chmod 0755 " + orchestratorStaging + " && " +
		"mv -f " + orchestratorStaging + " " + orchestratorBinary
}

// writeOrchestrator drops a stand-in for the shipped binary where a test
// appliance context can find it. The path differs per test and the content does
// not, which is what the install probe cares about.
func writeOrchestrator(t *testing.T) (path string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "nbd-orchestrator")
	if err := os.WriteFile(path, []byte(testOrchestratorBinary), 0o755); err != nil {
		t.Fatalf("write orchestrator: %v", err)
	}
	return
}

// tlsMaterial is one CA and the two halves signed by it.
type tlsMaterial struct {
	// data is the TLS secret as the operator creates it.
	data map[string][]byte
	// server is the keypair the appliance's announce endpoint serves with, and
	// pool the CA to verify it against.
	server tls.Certificate
	pool   *x509.CertPool
}

// applianceTLS is the material every test appliance is deployed with. Generated
// once for the whole binary, because the install probe hashes the certificates:
// two appliances holding different ones would disagree about what "already
// installed" means, and the point of the fixture is that they do not.
var applianceTLS = sync.OnceValue(newTLSMaterial)

func newTLSMaterial() *tlsMaterial {
	caKey, caCert, caPEM := issue(nil, nil, "Test CA", true, "")
	_, _, serverCertPEM, serverKeyPEM := leaf(caCert, caKey, announce.ServerName, x509.ExtKeyUsageServerAuth)
	_, _, clientCertPEM, clientKeyPEM := leaf(caCert, caKey, "client", x509.ExtKeyUsageClientAuth)

	server, err := tls.X509KeyPair(serverCertPEM, serverKeyPEM)
	if err != nil {
		panic(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	return &tlsMaterial{
		data: map[string][]byte{
			tlsCACert:     caPEM,
			tlsServerCert: serverCertPEM,
			tlsServerKey:  serverKeyPEM,
			tlsClientCert: clientCertPEM,
			tlsClientKey:  clientKeyPEM,
		},
		server: server,
		pool:   pool,
	}
}

func leaf(ca *x509.Certificate, caKey *ecdsa.PrivateKey, name string, eku x509.ExtKeyUsage) (*ecdsa.PrivateKey, *x509.Certificate, []byte, []byte) {
	key, cert, certPEM := issue(ca, caKey, name, false, "", eku)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		panic(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	return key, cert, certPEM, keyPEM
}

// issue signs a certificate, self-signing it when there is no parent. The name
// goes in both the subject and a DNS SAN, which is what a client verifies.
func issue(parent *x509.Certificate, parentKey *ecdsa.PrivateKey, name string, ca bool, _ string, eku ...x509.ExtKeyUsage) (*ecdsa.PrivateKey, *x509.Certificate, []byte) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           eku,
		BasicConstraintsValid: true,
	}
	if ca {
		template.IsCA = true
		template.KeyUsage |= x509.KeyUsageCertSign
	} else {
		template.DNSNames = []string{name}
	}

	signer, signerKey := template, key
	if parent != nil {
		signer, signerKey = parent, parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, template, signer, &key.PublicKey, signerKey)
	if err != nil {
		panic(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}
	return key, cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
