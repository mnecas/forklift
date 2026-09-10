package base

import (
	"context"
	liburl "net/url"

	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/session"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/soap"
	core "k8s.io/api/core/v1"
)

// ConnectGovmomi builds a logged-in govmomi client for a vSphere endpoint.
// The insecureSkipVerify behavior is derived from secret, matching the rest
// of the codebase (see GetInsecureSkipVerifyFlag).
func ConnectGovmomi(ctx context.Context, rawURL, user, password, thumbprint string, secret *core.Secret) (*govmomi.Client, error) {
	url, err := liburl.Parse(rawURL)
	if err != nil {
		return nil, liberr.Wrap(err)
	}
	url.User = liburl.UserPassword(user, password)
	soapClient := soap.NewClient(url, GetInsecureSkipVerifyFlag(secret))
	soapClient.SetThumbprint(url.Host, thumbprint)
	vimClient, err := vim25.NewClient(ctx, soapClient)
	if err != nil {
		return nil, liberr.Wrap(err)
	}
	client := &govmomi.Client{
		SessionManager: session.NewManager(vimClient),
		Client:         vimClient,
	}
	if err = client.Login(ctx, url.User); err != nil {
		return nil, liberr.Wrap(err)
	}
	return client, nil
}
