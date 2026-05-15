/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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
	// MaxRetries caps the SDK's internal retry budget (default 10) to 3 so a
	// stuck Cloudflare endpoint fails fast and lets controller-runtime requeue
	// rather than blocking a reconciler worker for minutes. Retry-After
	// honoring + 429/5xx classification remain handled by the SDK.
	cf := cfgo.NewClient(
		option.WithAPIToken(creds.Token),
		option.WithMaxRetries(3),
	)
	return &Client{cf: cf, accountID: creds.AccountID}, nil
}

// CF returns the underlying cloudflare-go client. Per-API impls use this
// to dispatch SDK calls; tests can substitute via an interface.
func (c *Client) CF() *cfgo.Client { return c.cf }

// AccountID returns the account this client is scoped to.
func (c *Client) AccountID() string { return c.accountID }
