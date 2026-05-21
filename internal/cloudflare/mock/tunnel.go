/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package mock

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
)

// ErrConnectionsActive is the mock-only sentinel returned by DeleteTunnel
// when the tunnel still has registered connections. Mirrors the real
// Cloudflare API which rejects tunnel deletion with a 4xx response while
// any connector is connected. Match via errors.Is.
var ErrConnectionsActive = errors.New("mock cloudflare: tunnel has active connections")

// tunnelMock is the in-memory tunnel sub-mock. It satisfies
// cloudflare.TunnelClient. Construct via New() and access as Mock.Tunnel,
// mirroring the Mock.Zone / Mock.DNS / Mock.Ruleset / Mock.ZoneConfig
// pattern.
type tunnelMock struct {
	parent *Mock
	mu     sync.Mutex
	seq    atomic.Uint64

	tunnels     map[string]*cloudflare.Tunnel              // tunnelID -> tunnel
	configs     map[string]*cloudflare.TunnelConfiguration // tunnelID -> latest configuration (with version)
	connections map[string][]cloudflare.TunnelConnection   // tunnelID -> active connections
	tokens      map[string]cloudflare.TunnelToken          // tunnelID -> stable connector token
}

// CreateTunnel adds a tunnel and assigns a deterministic ID. The connector
// token is generated once and remains stable for the tunnel's lifetime.
func (t *tunnelMock) CreateTunnel(ctx context.Context, accountID string, params cloudflare.CreateTunnelParams) (*cloudflare.Tunnel, error) {
	if err := t.parent.take("Tunnel.CreateTunnel"); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	n := t.seq.Add(1)
	id := fmt.Sprintf("mock-tunnel-%s-%s", params.Name, strconv.FormatUint(n, 10))
	tn := &cloudflare.Tunnel{ID: id, Name: params.Name, AccountTag: accountID}
	t.tunnels[id] = tn
	t.tokens[id] = cloudflare.TunnelToken("token-" + id)
	return tn, nil
}

// GetTunnel returns the tunnel by ID or a dual-sentinel not-found error.
func (t *tunnelMock) GetTunnel(ctx context.Context, accountID, tunnelID string) (*cloudflare.Tunnel, error) {
	if err := t.parent.take("Tunnel.GetTunnel"); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	tn, ok := t.tunnels[tunnelID]
	if !ok {
		return nil, fmt.Errorf("%w: %w: tunnel %s", ErrNotFound, cloudflare.ErrTunnelNotFound, tunnelID)
	}
	return tn, nil
}

// ListTunnelsByName returns all tunnels whose Name matches.
func (t *tunnelMock) ListTunnelsByName(ctx context.Context, accountID, name string) ([]cloudflare.Tunnel, error) {
	if err := t.parent.take("Tunnel.ListTunnelsByName"); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := []cloudflare.Tunnel{}
	for _, tn := range t.tunnels {
		if tn.Name == name {
			out = append(out, *tn)
		}
	}
	return out, nil
}

// PatchTunnel updates the mutable fields of a tunnel (currently only Name).
func (t *tunnelMock) PatchTunnel(ctx context.Context, accountID, tunnelID string, params cloudflare.PatchTunnelParams) (*cloudflare.Tunnel, error) {
	if err := t.parent.take("Tunnel.PatchTunnel"); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	tn, ok := t.tunnels[tunnelID]
	if !ok {
		return nil, fmt.Errorf("%w: %w: tunnel %s", ErrNotFound, cloudflare.ErrTunnelNotFound, tunnelID)
	}
	if params.Name != nil {
		tn.Name = *params.Name
	}
	return tn, nil
}

// DeleteTunnel removes a tunnel. Fails with ErrConnectionsActive if any
// connector is still registered against the tunnel, mirroring the
// production API contract.
func (t *tunnelMock) DeleteTunnel(ctx context.Context, accountID, tunnelID string) error {
	if err := t.parent.take("Tunnel.DeleteTunnel"); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.tunnels[tunnelID]; !ok {
		return fmt.Errorf("%w: %w: tunnel %s", ErrNotFound, cloudflare.ErrTunnelNotFound, tunnelID)
	}
	if len(t.connections[tunnelID]) > 0 {
		return fmt.Errorf("%w: tunnel %s", ErrConnectionsActive, tunnelID)
	}
	delete(t.tunnels, tunnelID)
	delete(t.configs, tunnelID)
	delete(t.tokens, tunnelID)
	delete(t.connections, tunnelID)
	return nil
}

// GetConfiguration returns the latest stored configuration. Before any PUT
// it returns an empty configuration at version 0 — the same shape the real
// API returns for tunnels with no remote config yet.
func (t *tunnelMock) GetConfiguration(ctx context.Context, accountID, tunnelID string) (*cloudflare.TunnelConfiguration, error) {
	if err := t.parent.take("Tunnel.GetConfiguration"); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.tunnels[tunnelID]; !ok {
		return nil, fmt.Errorf("%w: %w: tunnel %s", ErrNotFound, cloudflare.ErrTunnelNotFound, tunnelID)
	}
	cfg, ok := t.configs[tunnelID]
	if !ok {
		return &cloudflare.TunnelConfiguration{Version: 0}, nil
	}
	return cfg, nil
}

// PutConfiguration replaces the configuration and increments the version
// counter. Version starts at 1 on the first PUT and increments on every
// subsequent PUT, matching the production API's monotonic version semantics.
func (t *tunnelMock) PutConfiguration(ctx context.Context, accountID, tunnelID string, cfg cloudflare.TunnelConfig) (*cloudflare.TunnelConfiguration, error) {
	if err := t.parent.take("Tunnel.PutConfiguration"); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.tunnels[tunnelID]; !ok {
		return nil, fmt.Errorf("%w: %w: tunnel %s", ErrNotFound, cloudflare.ErrTunnelNotFound, tunnelID)
	}
	prev := t.configs[tunnelID]
	version := 1
	if prev != nil {
		version = prev.Version + 1
	}
	tc := &cloudflare.TunnelConfiguration{Version: version, Config: cfg}
	t.configs[tunnelID] = tc
	return tc, nil
}

// GetToken returns the connector-join token. Stable per tunnel.
func (t *tunnelMock) GetToken(ctx context.Context, accountID, tunnelID string) (cloudflare.TunnelToken, error) {
	if err := t.parent.take("Tunnel.GetToken"); err != nil {
		return "", err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.tunnels[tunnelID]; !ok {
		return "", fmt.Errorf("%w: %w: tunnel %s", ErrNotFound, cloudflare.ErrTunnelNotFound, tunnelID)
	}
	return t.tokens[tunnelID], nil
}

// ListConnections returns a copy of the active connections for the tunnel.
func (t *tunnelMock) ListConnections(ctx context.Context, accountID, tunnelID string) ([]cloudflare.TunnelConnection, error) {
	if err := t.parent.take("Tunnel.ListConnections"); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.tunnels[tunnelID]; !ok {
		return nil, fmt.Errorf("%w: %w: tunnel %s", ErrNotFound, cloudflare.ErrTunnelNotFound, tunnelID)
	}
	return append([]cloudflare.TunnelConnection{}, t.connections[tunnelID]...), nil
}

// DeleteConnections clears any registered connections, unblocking DeleteTunnel.
func (t *tunnelMock) DeleteConnections(ctx context.Context, accountID, tunnelID string) error {
	if err := t.parent.take("Tunnel.DeleteConnections"); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.tunnels[tunnelID]; !ok {
		return fmt.Errorf("%w: %w: tunnel %s", ErrNotFound, cloudflare.ErrTunnelNotFound, tunnelID)
	}
	delete(t.connections, tunnelID)
	return nil
}

// SeedConnections lets tests inject simulated active connectors.
func (t *tunnelMock) SeedConnections(tunnelID string, conns []cloudflare.TunnelConnection) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.connections[tunnelID] = append([]cloudflare.TunnelConnection{}, conns...)
}

// SeedConfig lets tests pre-seed an existing TunnelConfiguration for a tunnel
// without going through PutConfiguration (which increments the version counter
// and requires the tunnel to already exist). Used by wipe-path envtests that
// need an OriginRequest present on the live Cloudflare side before the operator
// first reconciles.
//
// The tunnel must already exist (call SeedConnections or CreateTunnel first to
// register it). SeedConfig is test-only and bypasses the take() audit; it does
// NOT increment the call counter for Tunnel.PutConfiguration.
func (t *tunnelMock) SeedConfig(tunnelID string, cfg cloudflare.TunnelConfig) {
	t.mu.Lock()
	defer t.mu.Unlock()
	version := 1
	if prev := t.configs[tunnelID]; prev != nil {
		version = prev.Version + 1
	}
	t.configs[tunnelID] = &cloudflare.TunnelConfiguration{Version: version, Config: cfg}
}

// Compile-time interface assertion.
var _ cloudflare.TunnelClient = (*tunnelMock)(nil)
