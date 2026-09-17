package copyappliance

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"os"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	imagev1client "github.com/openshift/client-go/image/clientset/versioned/typed/image/v1"
	"golang.org/x/crypto/ssh"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

// serviceAccountTokenFile holds the controller's own bearer token, which the
// cluster's internal registry takes as a password.
const serviceAccountTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token" // #nosec G101

// registryUser is the username sent alongside the token. The internal registry
// checks only the token, but a password needs a username to go with it.
const registryUser = "serviceaccount"

// loadedImageRepository is where the image is filed in the appliance's podman
// store. Deliberately not the cluster pull spec: the appliance has no reason to
// carry the in-cluster registry's hostname, and a digest reference cannot be a
// tag in a docker archive.
const loadedImageRepository = "localhost/forklift-copy-appliance"

// loadedImageTagLength is how much of the digest goes into the tag. Enough to
// tell two builds apart at a glance, which is all the tag is for; the store is
// only ever asked about references this controller put there.
const loadedImageTagLength = 12

// applianceLoadCommand reads a docker archive on its standard input and adds
// what it finds to the appliance's podman store.
const applianceLoadCommand = "/usr/local/bin/toehold-podman load"

// resolveImage returns the pull spec of an ImageStreamTag in the controller's
// own namespace, which is where the appliance image is built.
func resolveImage(ctx context.Context, tag string) (spec string, err error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	client, err := imagev1client.NewForConfig(cfg)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	namespace := Settings.Namespace
	imageTag, err := client.ImageStreamTags(namespace).Get(ctx, tag, meta.GetOptions{})
	if err != nil {
		if k8serr.IsNotFound(err) {
			// Named rather than wrapped: the appliance image is built before a
			// migration runs, so this is a deployment that was never finished
			// rather than a race with anything.
			err = liberr.New(
				"the copy appliance container image is not in the cluster registry",
				"namespace", namespace,
				"imageStreamTag", tag)
			return
		}
		err = liberr.Wrap(err, "namespace", namespace, "imageStreamTag", tag)
		return
	}
	spec = imageTag.Image.DockerImageReference
	return
}

// registryImage reads the image from a registry. Nothing is pulled here beyond
// the manifest and the config: the layers are fetched as the archive is
// written.
func registryImage(ctx context.Context, spec string, options ...remote.Option) (img v1.Image, err error) {
	ref, err := name.ParseReference(spec)
	if err != nil {
		err = liberr.Wrap(err, "image", spec)
		return
	}
	img, err = remote.Image(ref, append(options, remote.WithContext(ctx))...)
	if err != nil {
		err = liberr.Wrap(err, "image", spec)
		return
	}
	return
}

// clusterRegistry is how the controller reaches the cluster's internal
// registry: its own service account token as the password, and the CA that
// signed the registry's certificate as a root to trust.
//
// Both come out of the service account's projected volume. The registry's
// certificate is signed by the service-serving signer, which is not in the
// system trust store, so the CA has to be pointed at explicitly -- the same
// file the inventory and policy clients reach for.
func clusterRegistry(caFile, tokenFile string) (options []remote.Option, err error) {
	transport, err := registryTransport(caFile)
	if err != nil {
		return
	}
	token, err := os.ReadFile(tokenFile)
	if err != nil {
		err = liberr.Wrap(err, "file", tokenFile)
		return
	}
	options = []remote.Option{
		remote.WithTransport(transport),
		remote.WithAuth(&authn.Basic{
			Username: registryUser,
			Password: string(token),
		}),
	}
	return
}

// registryTransport trusts the certificates in the given PEM file on top of the
// system roots.
func registryTransport(caFile string) (transport http.RoundTripper, err error) {
	ca, err := os.ReadFile(caFile)
	if err != nil {
		err = liberr.Wrap(err, "file", caFile)
		return
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		// Not fatal: the only certificate this transport has to verify is the
		// registry's, and that one is about to be added.
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(ca) {
		err = liberr.New("the CA file holds no certificate", "file", caFile)
		return
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	transport = base
	return
}

// loadedReference is the reference the image is filed under in the appliance's
// podman store. It carries the digest, so an appliance still holding an earlier
// build of the same tag does not answer to it.
func loadedReference(img v1.Image) (ref name.Tag, err error) {
	digest, err := img.Digest()
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	short := digest.Hex
	if len(short) > loadedImageTagLength {
		short = short[:loadedImageTagLength]
	}
	// With no default registry the reference stays exactly what it says.
	// Otherwise "localhost" reads as a repository rather than a host -- it has
	// no dot and no port -- and the reference the appliance is later asked
	// about comes back qualified with Docker Hub, which is not what the archive
	// files the image under.
	ref, err = name.NewTag(loadedImageRepository+":"+short, name.WithDefaultRegistry(""))
	if err != nil {
		err = liberr.Wrap(err, "digest", digest.String())
		return
	}
	return
}

// streamImage writes the image into the appliance's podman store as a docker
// archive on the load command's standard input. The archive is built as it is
// sent, so an image larger than the controller's memory is not a problem.
func (r *ApplianceContext) streamImage(client *ssh.Client, img v1.Image, ref name.Tag) (err error) {
	reader, writer := io.Pipe()
	go func() {
		// A failure part way through arrives at the load side as a read error,
		// rather than as a truncated archive that podman would reject for ther
		// wrong reason.
		_ = writer.CloseWithError(tarball.Write(ref, img, writer))
	}()
	// Closing the read half is what unblocks the writer when the load command
	// gives up before the archive is finished.
	defer func() {
		_ = reader.Close()
	}()

	err = r.RunWithStdin(client, applianceLoadCommand, reader)
	return
}
