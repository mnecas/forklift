package vsphere

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/session"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/soap"
)

// ConnectOptions configures a vCenter session.
type ConnectOptions struct {
	URL        string
	Username   string
	Password   string
	Thumbprint string
	Insecure   bool
}

// Client wraps a govmomi session and inventory helpers.
type Client struct {
	Govmomi *govmomi.Client
	Finder  *find.Finder
}

// Connect establishes a vCenter session.
func Connect(ctx context.Context, opts ConnectOptions) (*Client, error) {
	if opts.URL == "" {
		return nil, fmt.Errorf("vCenter URL is required")
	}
	u, err := url.Parse(opts.URL)
	if err != nil {
		return nil, err
	}
	u.User = url.UserPassword(opts.Username, opts.Password)
	soapClient := soap.NewClient(u, opts.Insecure)
	if opts.Thumbprint != "" {
		soapClient.SetThumbprint(u.Host, opts.Thumbprint)
	}
	vimClient, err := vim25.NewClient(ctx, soapClient)
	if err != nil {
		return nil, err
	}
	gc := &govmomi.Client{
		Client:         vimClient,
		SessionManager: session.NewManager(vimClient),
	}
	if err = gc.Login(ctx, u.User); err != nil {
		return nil, err
	}
	finder := find.NewFinder(gc.Client, true)
	dc, err := finder.DefaultDatacenter(ctx)
	if err == nil {
		finder.SetDatacenter(dc)
	}
	return &Client{Govmomi: gc, Finder: finder}, nil
}

// Close logs out of vCenter.
func (c *Client) Close(ctx context.Context) error {
	if c == nil || c.Govmomi == nil {
		return nil
	}
	return c.Govmomi.Logout(ctx)
}

func normalizeInventoryPath(folder string) string {
	p := strings.TrimSpace(folder)
	p = strings.TrimPrefix(p, "/")
	return p
}

func (c *Client) findFolder(ctx context.Context, folderPath string) (*object.Folder, error) {
	path := normalizeInventoryPath(folderPath)
	if path == "" {
		return c.Finder.DefaultFolder(ctx)
	}
	return c.Finder.Folder(ctx, path)
}

func (c *Client) findDatastore(ctx context.Context, name string) (*object.Datastore, error) {
	return c.Finder.Datastore(ctx, name)
}

func (c *Client) findNetwork(ctx context.Context, name string) (object.NetworkReference, error) {
	return c.Finder.Network(ctx, name)
}

func (c *Client) findResourcePool(ctx context.Context, folderPath string) (*object.ResourcePool, error) {
	pool, err := c.Finder.ResourcePool(ctx, "Resources")
	if err == nil {
		return pool, nil
	}
	folder, err := c.findFolder(ctx, folderPath)
	if err != nil {
		return nil, err
	}
	children, err := folder.Children(ctx)
	if err != nil {
		return nil, err
	}
	for _, child := range children {
		ref := child.Reference()
		if ref.Type == "ResourcePool" {
			return object.NewResourcePool(c.Govmomi.Client, ref), nil
		}
		if ref.Type == "ClusterComputeResource" || ref.Type == "ComputeResource" {
			cr := object.NewComputeResource(c.Govmomi.Client, ref)
			p, perr := cr.ResourcePool(ctx)
			if perr == nil {
				return p, nil
			}
		}
	}
	return nil, fmt.Errorf("resource pool not found under %q", folderPath)
}
