package conversion

import (
	"context"

	libvsphere "github.com/kubev2v/forklift/pkg/lib/vsphere"
	"github.com/vmware/govmomi"
	core "k8s.io/api/core/v1"
)

// GovmomiClientFromSecret builds a govmomi client from a connection Secret
// with keys: url, user, password. Optional: insecureSkipVerify, fingerprint, cacert.
func GovmomiClientFromSecret(ctx context.Context, secret *core.Secret) (*govmomi.Client, error) {
	return libvsphere.ConnectFromSecret(ctx, secret)
}
