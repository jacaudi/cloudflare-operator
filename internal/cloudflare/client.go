package cloudflare

import (
	"fmt"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/option"
)

// Client is the Foundation-owned façade over cloudflare-go. It is intentionally
// thin: the only operations Foundation exposes are construction and accessor
// AccountID. Per-API impls (DNSClient, ZoneClient, TunnelClient, etc.) live in
// spec 2 and spec 3 and consume this same client via interfaces.go.
type Client struct {
	cf        *cfgo.Client
	accountID string
}

// NewClient builds a cloudflare-go client from resolved credentials.
func NewClient(creds Credentials) (*Client, error) {
	if creds.Token == "" {
		return nil, fmt.Errorf("token required")
	}
	if creds.AccountID == "" {
		return nil, fmt.Errorf("accountID required")
	}
	cf := cfgo.NewClient(option.WithAPIToken(creds.Token))
	return &Client{cf: cf, accountID: creds.AccountID}, nil
}

// CF returns the underlying cloudflare-go client. Per-API impls use this
// to dispatch SDK calls; tests can substitute via an interface.
func (c *Client) CF() *cfgo.Client { return c.cf }

// AccountID returns the account this client is scoped to.
func (c *Client) AccountID() string { return c.accountID }
