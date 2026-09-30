package vsphere

import (
	"context"

	liberr "github.com/kubev2v/forklift/pkg/lib/error"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
)

// Session is a govmomi login plus inventory Finder.
type Session struct {
	Client *govmomi.Client
	Finder *find.Finder
}

// NewSession wraps a govmomi client with a Finder.
func NewSession(ctx context.Context, client *govmomi.Client, all bool) (*Session, error) {
	if client == nil {
		return nil, liberr.New("govmomi client is required")
	}
	finder := find.NewFinder(client.Client, all)
	if dc, err := finder.DefaultDatacenter(ctx); err == nil {
		finder.SetDatacenter(dc)
	}
	return &Session{Client: client, Finder: finder}, nil
}

// Close logs out and closes idle connections.
func (s *Session) Close(ctx context.Context) error {
	if s == nil || s.Client == nil {
		return nil
	}
	err := s.Client.Logout(ctx)
	s.Client.CloseIdleConnections()
	s.Client = nil
	s.Finder = nil
	return err
}

// SetDatacenter scopes the Finder to name (or the default when empty).
func (s *Session) SetDatacenter(ctx context.Context, name string) (*object.Datacenter, error) {
	dc, err := s.Finder.DatacenterOrDefault(ctx, name)
	if err != nil {
		return nil, liberr.Wrap(err)
	}
	s.Finder.SetDatacenter(dc)
	return dc, nil
}
