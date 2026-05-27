/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package cloudflare

import (
	"fmt"

	cfgo "github.com/cloudflare/cloudflare-go/v7"
	"github.com/cloudflare/cloudflare-go/v7/option"
)

// Client is the Foundation-owned façade over cloudflare-go. It is intentionally
// thin: the only operations Foundation exposes are construction and accessor
// AccountID. Per-API impls (DNSClient, ZoneClient, TunnelClient, etc.) live in
// spec 2 and spec 3 and consume this same client via interfaces.go.
type Client struct {
	cf        *cfgo.Client
	accountID string
}

// NewClient returns a *Client for creds, reusing a cached instance when
// available.  Two reconciles using identical credentials share the same
// *Client and its underlying HTTP/2 connection pool.
//
// Cache-miss builds a fresh client via buildClient and stores it.  The cache
// is a 32-entry LRU with a 30-minute absolute TTL (measured from insertion,
// not last use); see clientcache.go.
func NewClient(creds Credentials) (*Client, error) {
	if creds.Token == "" {
		return nil, fmt.Errorf("token required")
	}
	if creds.AccountID == "" {
		return nil, fmt.Errorf("accountID required")
	}
	key := cacheKey(creds)
	if c, ok := clientCache.Get(key); ok {
		return c, nil
	}
	c, err := buildClient(creds)
	if err != nil {
		return nil, err
	}
	clientCache.Add(key, c)
	return c, nil
}

// buildClient constructs a fresh *Client from resolved credentials without
// consulting the cache.  Called on cache-miss by NewClient.
func buildClient(creds Credentials) (*Client, error) {
	// MaxRetries caps the SDK's internal retry budget (default 10) to 3 so a
	// stuck Cloudflare endpoint fails fast and lets controller-runtime requeue
	// rather than blocking a reconciler worker for minutes. Retry-After
	// honoring + 429/5xx classification remain handled by the SDK.
	cf := cfgo.NewClient(
		option.WithAPIToken(creds.Token.Expose()),
		option.WithMaxRetries(3),
	)
	return &Client{cf: cf, accountID: creds.AccountID}, nil
}

// CF returns the underlying cloudflare-go client. Per-API impls use this
// to dispatch SDK calls; tests can substitute via an interface.
func (c *Client) CF() *cfgo.Client { return c.cf }

// AccountID returns the account this client is scoped to.
func (c *Client) AccountID() string { return c.accountID }
