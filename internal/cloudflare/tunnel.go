package cloudflare

import (
	"context"
	"fmt"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/shared"
	"github.com/cloudflare/cloudflare-go/v6/zero_trust"
)

// tunnelClient wraps the cloudflare-go v6 SDK to implement TunnelClient.
type tunnelClient struct {
	cf *cfgo.Client
}

// NewTunnelClientFromCF creates a TunnelClient from a cloudflare-go Client.
func NewTunnelClientFromCF(cf *cfgo.Client) TunnelClient {
	return &tunnelClient{cf: cf}
}

func (c *tunnelClient) GetTunnel(ctx context.Context, accountID, tunnelID string) (*Tunnel, error) {
	resp, err := c.cf.ZeroTrust.Tunnels.Cloudflared.Get(ctx, tunnelID, zero_trust.TunnelCloudflaredGetParams{
		AccountID: cfgo.F(accountID),
	})
	if err != nil {
		return nil, fmt.Errorf("get tunnel %s: %w", tunnelID, err)
	}
	return mapTunnelResponse(resp), nil
}

func (c *tunnelClient) ListTunnelsByName(ctx context.Context, accountID, name string) ([]Tunnel, error) {
	page, err := c.cf.ZeroTrust.Tunnels.Cloudflared.List(ctx, zero_trust.TunnelCloudflaredListParams{
		AccountID: cfgo.F(accountID),
		Name:      cfgo.F(name),
		IsDeleted: cfgo.F(false),
	})
	if err != nil {
		return nil, fmt.Errorf("list tunnels: %w", err)
	}

	var tunnels []Tunnel
	for _, t := range page.Result {
		tunnels = append(tunnels, *mapTunnelResponse(&t))
	}
	return tunnels, nil
}

func (c *tunnelClient) CreateTunnel(ctx context.Context, accountID string, params TunnelParams) (*Tunnel, error) {
	resp, err := c.cf.ZeroTrust.Tunnels.Cloudflared.New(ctx, zero_trust.TunnelCloudflaredNewParams{
		AccountID:    cfgo.F(accountID),
		Name:         cfgo.F(params.Name),
		TunnelSecret: cfgo.F(params.TunnelSecret),
	})
	if err != nil {
		return nil, fmt.Errorf("create tunnel: %w", err)
	}
	return mapTunnelResponse(resp), nil
}

func (c *tunnelClient) DeleteTunnel(ctx context.Context, accountID, tunnelID string) error {
	_, err := c.cf.ZeroTrust.Tunnels.Cloudflared.Delete(ctx, tunnelID, zero_trust.TunnelCloudflaredDeleteParams{
		AccountID: cfgo.F(accountID),
	})
	if err != nil {
		return fmt.Errorf("delete tunnel %s: %w", tunnelID, err)
	}
	return nil
}

// mapTunnelResponse converts a Cloudflare SDK CloudflareTunnel to our internal Tunnel.
func mapTunnelResponse(t *shared.CloudflareTunnel) *Tunnel {
	return &Tunnel{
		ID:   t.ID,
		Name: t.Name,
	}
}
