package base

import (
	"context"

	libvsphere "github.com/kubev2v/forklift/pkg/lib/vsphere"
	"github.com/vmware/govmomi"
	core "k8s.io/api/core/v1"
)

// ConnectGovmomi builds a govmomi client for a vSphere provider secret.
//
// When insecureSkipVerify is false, the peer certificate is verified and its
// fingerprint is used as the thumbprint. When insecureSkipVerify is true,
// thumbprint is used as provided.
func ConnectGovmomi(ctx context.Context, url, user, password, thumbprint string, secret *core.Secret) (*govmomi.Client, error) {
	return libvsphere.ConnectProvider(ctx, url, user, password, thumbprint, secret)
}
