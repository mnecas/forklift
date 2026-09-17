/*
Copyright 2019 Red Hat Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package provider

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	"github.com/kubev2v/forklift/pkg/nbd-container/announce"
	v1 "k8s.io/api/core/v1"
)

// Keys of the toehold TLS material within the appliance secret. They are the
// file names the appliance expects, so that what is in the secret is what lands
// on the appliance.
const (
	tlsCACert     = "ca-cert.pem"
	tlsServerCert = "server-cert.pem"
	tlsServerKey  = "server-key.pem"
	tlsClientCert = "client-cert.pem"
	tlsClientKey  = "client-key.pem"
)

// Subject names of the issued certificates. The server is named for the logical
// service rather than for an address: the appliance is cloned on demand and its
// address is not known when the certificate is issued, so the client verifies
// the name instead of where it reached it.
const (
	tlsCAName     = "forklift-toehold-ca"
	tlsClientName = "forklift-controller"
)

// tlsLifetime is how long the issued certificates are good for. There is no
// renewal: the material is regenerated with the provider's SSH keys, and an
// appliance is a transient clone that outlives neither.
const tlsLifetime = 10 * 365 * 24 * time.Hour

// tlsClockSkew backdates NotBefore so that a verifier whose clock runs behind
// the controller's does not reject a freshly issued certificate.
const tlsClockSkew = time.Hour

// toeholdTLS issues the mutual-TLS material a toehold appliance serves its
// exports with: a private CA, the server half the appliance presents, and the
// client half the controller queries it with. The CA key is discarded once the
// leaves are signed, so nothing else can be issued against it. Keyed by the
// file name each lands under on the appliance.
func toeholdTLS() (data map[string][]byte, err error) {
	caKey, ca, caPEM, err := issueCertificate(nil, nil, tlsCAName)
	if err != nil {
		return
	}
	serverCertPEM, serverKeyPEM, err := issueLeaf(ca, caKey, announce.ServerName, x509.ExtKeyUsageServerAuth)
	if err != nil {
		return
	}
	clientCertPEM, clientKeyPEM, err := issueLeaf(ca, caKey, tlsClientName, x509.ExtKeyUsageClientAuth)
	if err != nil {
		return
	}
	data = map[string][]byte{
		tlsCACert:     caPEM,
		tlsServerCert: serverCertPEM,
		tlsServerKey:  serverKeyPEM,
		tlsClientCert: clientCertPEM,
		tlsClientKey:  clientKeyPEM,
	}
	return
}

// ensureToeholdTLS fills in the TLS material an appliance secret is missing.
// All five are regenerated together: a leaf and a CA from different runs do not
// chain, so replacing only what is absent would leave the secret unusable.
func (r *Reconciler) ensureToeholdTLS(secret *v1.Secret) error {
	complete := true
	for _, key := range []string{tlsCACert, tlsServerCert, tlsServerKey, tlsClientCert, tlsClientKey} {
		if len(secret.Data[key]) == 0 {
			complete = false
			break
		}
	}
	if complete {
		return nil
	}

	r.Log.Info("Generating toehold TLS material for existing secret", "secret", secret.Name)
	data, err := toeholdTLS()
	if err != nil {
		return err
	}
	if secret.Data == nil {
		secret.Data = make(map[string][]byte, len(data))
	}
	for key, value := range data {
		secret.Data[key] = value
	}
	err = r.Update(context.TODO(), secret)
	if err != nil {
		return fmt.Errorf("failed to update secret %s with toehold TLS material: %w", secret.Name, err)
	}
	return nil
}

// issueLeaf signs an end-entity certificate against the CA and returns it with
// its key, both PEM encoded.
func issueLeaf(ca *x509.Certificate, caKey *ecdsa.PrivateKey, name string, eku x509.ExtKeyUsage) (certPEM, keyPEM []byte, err error) {
	key, _, certPEM, err := issueCertificate(ca, caKey, name, eku)
	if err != nil {
		return
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		err = fmt.Errorf("failed to marshal %s key: %w", name, err)
		return
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	return
}

// issueCertificate signs a certificate, self-signing it as a CA when there is
// no parent. A leaf carries the name as a DNS SAN as well as in the subject,
// because that is the half a Go client verifies.
func issueCertificate(parent *x509.Certificate, parentKey *ecdsa.PrivateKey, name string, eku ...x509.ExtKeyUsage) (key *ecdsa.PrivateKey, cert *x509.Certificate, certPEM []byte, err error) {
	key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		err = fmt.Errorf("failed to generate %s key: %w", name, err)
		return
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		err = fmt.Errorf("failed to generate %s serial: %w", name, err)
		return
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             now.Add(-tlsClockSkew),
		NotAfter:              now.Add(tlsLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           eku,
		BasicConstraintsValid: true,
	}
	if parent == nil {
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
		err = fmt.Errorf("failed to sign %s certificate: %w", name, err)
		return
	}
	cert, err = x509.ParseCertificate(der)
	if err != nil {
		err = fmt.Errorf("failed to parse %s certificate: %w", name, err)
		return
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return
}
