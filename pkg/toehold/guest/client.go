package guest

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Client runs commands on the appliance guest over SSH.
type Client struct {
	client *ssh.Client
}

// Dial opens an SSH session to the appliance guest.
func Dial(ctx context.Context, host, user string, privateKey []byte) (*Client, error) {
	if strings.TrimSpace(host) == "" {
		return nil, fmt.Errorf("guest host is required")
	}
	if user == "" {
		user = "root"
	}
	signer, err := ssh.ParsePrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	addr := net.JoinHostPort(host, "22")
	dialer := &net.Dialer{}
	netConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = netConn.SetDeadline(deadline)
	}
	cc, chans, reqs, err := ssh.NewClientConn(netConn, addr, config)
	if err != nil {
		_ = netConn.Close()
		return nil, err
	}
	return &Client{client: ssh.NewClient(cc, chans, reqs)}, nil
}

// Close closes the SSH client.
func (c *Client) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Close()
}

// Run executes a remote command and returns combined output.
func (c *Client) Run(ctx context.Context, command string) (string, error) {
	session, err := c.client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = session.Setenv("LC_ALL", "C")
		_ = deadline
	}
	output, err := session.CombinedOutput(command)
	if err != nil {
		return strings.TrimSpace(string(output)), fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

// BlockDevices returns guest block device names such as sdb, excluding the boot disk.
func (c *Client) BlockDevices(ctx context.Context) (map[string]bool, error) {
	output, err := c.Run(ctx, `ls -1 /sys/block | grep -E '^sd[b-z]$' || true`)
	if err != nil {
		return nil, err
	}
	devices := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		name := strings.TrimSpace(line)
		if name != "" {
			devices[name] = true
		}
	}
	return devices, nil
}

// WaitForNewBlockDevice waits until a block device appears that was not in the initial set.
func (c *Client) WaitForNewBlockDevice(ctx context.Context, existing map[string]bool, timeout time.Duration) (string, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		devices, err := c.BlockDevices(waitCtx)
		if err != nil {
			return "", err
		}
		for name := range devices {
			if !existing[name] {
				return "/dev/" + name, nil
			}
		}
		select {
		case <-waitCtx.Done():
			return "", fmt.Errorf("timed out waiting for new block device")
		case <-ticker.C:
		}
	}
}

// Settle runs udevadm settle on the guest.
func (c *Client) Settle(ctx context.Context) error {
	_, err := c.Run(ctx, "udevadm settle -t 60")
	return err
}
