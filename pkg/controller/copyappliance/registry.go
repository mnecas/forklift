package copyappliance

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/kubev2v/forklift/pkg/settings"
	imagev1client "github.com/openshift/client-go/image/clientset/versioned/typed/image/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

// serviceAccountTokenFile is the controller's bearer token, which the cluster's
// internal registry accepts as a password.
const serviceAccountTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token" // #nosec G101

// registryUser is the username sent with the token. The internal registry
// validates only the token.
const registryUser = "serviceaccount"

// ClusterRegistry reads container images from the cluster's internal image
// registry. Images are named by ImageStreamTag in the controller's own
// namespace and pulled with the controller's service account as the credential.
type ClusterRegistry struct {
	// Namespace is where the appliance image's ImageStream is built.
	Namespace string
	// CAFile is the PEM file holding the CA that signed the registry's serving
	// certificate. The service-serving signer is in no system trust store.
	CAFile string
	// TokenFile is the bearer token the registry accepts as a password. It is
	// read on every pull because the kubelet rotates the projected token.
	TokenFile string

	tags imagev1client.ImageStreamTagsGetter
}

// NewClusterRegistry returns a reader for the registry of the cluster the
// controller is running in.
func NewClusterRegistry() (registry *ClusterRegistry, err error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	registry, err = newClusterRegistry(
		cfg,
		Settings.Namespace,
		settings.ServiceCAFile,
		serviceAccountTokenFile)
	return
}

// newClusterRegistry builds a ClusterRegistry against the given API server
// configuration, which is how a test stands one up outside a cluster.
func newClusterRegistry(cfg *rest.Config, namespace, caFile, tokenFile string) (registry *ClusterRegistry, err error) {
	client, err := imagev1client.NewForConfig(cfg)
	if err != nil {
		err = liberr.Wrap(err)
		return
	}
	registry = &ClusterRegistry{
		Namespace: namespace,
		CAFile:    caFile,
		TokenFile: tokenFile,
		tags:      client,
	}
	return
}

// Image returns the metadata for the image the ImageStreamTag points at. Only
// the manifest and the config are fetched; the layers are read on demand.
func (r *ClusterRegistry) Image(ctx context.Context, tag string) (img v1.Image, err error) {
	spec, err := r.PullSpec(ctx, tag)
	if err != nil {
		return
	}
	options, err := r.options()
	if err != nil {
		return
	}
	img, err = r.registryImage(ctx, spec, options...)
	return
}

// PullSpec returns the pull spec the ImageStreamTag resolves to.
func (r *ClusterRegistry) PullSpec(ctx context.Context, tag string) (pullSpec string, err error) {
	imageTag, err := r.tags.ImageStreamTags(r.Namespace).Get(ctx, tag, meta.GetOptions{})
	if err != nil {
		if k8serr.IsNotFound(err) {
			// The appliance image is built before any migration runs, so a
			// missing tag means an unfinished deployment.
			err = liberr.New(
				"the copy appliance container image is not in the cluster registry",
				"namespace", r.Namespace,
				"imageStreamTag", tag)
			return
		}
		err = liberr.Wrap(err, "namespace", r.Namespace, "imageStreamTag", tag)
		return
	}
	pullSpec = imageTag.Image.DockerImageReference
	return
}

// options returns the remote options for reaching the registry: a transport
// that trusts its CA, and basic auth with the controller's token.
func (r *ClusterRegistry) options() (options []remote.Option, err error) {
	transport, err := r.registryTransport(r.CAFile)
	if err != nil {
		return
	}
	token, err := os.ReadFile(r.TokenFile)
	if err != nil {
		err = liberr.Wrap(err, "file", r.TokenFile)
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

// registryImage reads the metadata for an image from the registry.
func (r *ClusterRegistry) registryImage(ctx context.Context, spec string, options ...remote.Option) (img v1.Image, err error) {
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

// registryTransport returns a RoundTripper that trusts the certificates
// in the given PEM file on top of the system roots.
func (r *ClusterRegistry) registryTransport(caFile string) (transport http.RoundTripper, err error) {
	ca, err := os.ReadFile(caFile)
	if err != nil {
		err = liberr.Wrap(err, "file", caFile)
		return
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		// Not fatal: the only certificate this transport verifies is the
		// registry's, which is added below.
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
