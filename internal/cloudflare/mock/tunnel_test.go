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

package mock

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
)

func TestMock_TunnelLifecycle(t *testing.T) {
	m := New()
	ctx := context.Background()

	created, err := m.Tunnel.CreateTunnel(ctx, "acct-1", cloudflare.CreateTunnelParams{Name: "cf-app-foo"})
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	require.Equal(t, "cf-app-foo", created.Name)
	require.Equal(t, "acct-1", created.AccountTag)

	got, err := m.Tunnel.GetTunnel(ctx, "acct-1", created.ID)
	require.NoError(t, err)
	require.Equal(t, created.ID, got.ID)

	list, err := m.Tunnel.ListTunnelsByName(ctx, "acct-1", "cf-app-foo")
	require.NoError(t, err)
	require.Len(t, list, 1)

	require.NoError(t, m.Tunnel.DeleteTunnel(ctx, "acct-1", created.ID))

	_, err = m.Tunnel.GetTunnel(ctx, "acct-1", created.ID)
	require.Error(t, err)
}

func TestMock_PatchTunnel(t *testing.T) {
	m := New()
	ctx := context.Background()
	tn, err := m.Tunnel.CreateTunnel(ctx, "a", cloudflare.CreateTunnelParams{Name: "t"})
	require.NoError(t, err)

	newName := "renamed"
	patched, err := m.Tunnel.PatchTunnel(ctx, "a", tn.ID, cloudflare.PatchTunnelParams{Name: &newName})
	require.NoError(t, err)
	require.Equal(t, "renamed", patched.Name)
}

func TestMock_DeleteTunnel_FailsIfConnectionsActive(t *testing.T) {
	m := New()
	ctx := context.Background()
	tn, err := m.Tunnel.CreateTunnel(ctx, "a", cloudflare.CreateTunnelParams{Name: "t"})
	require.NoError(t, err)

	m.Tunnel.SeedConnections(tn.ID, []cloudflare.TunnelConnection{{ID: "c1", ColoName: "DEN"}})

	err = m.Tunnel.DeleteTunnel(ctx, "a", tn.ID)
	require.Error(t, err, "delete should fail while connections exist")
	require.True(t, errors.Is(err, ErrConnectionsActive), "should satisfy ErrConnectionsActive sentinel")

	require.NoError(t, m.Tunnel.DeleteConnections(ctx, "a", tn.ID))
	require.NoError(t, m.Tunnel.DeleteTunnel(ctx, "a", tn.ID))
}

func TestMock_Configuration_PutGet(t *testing.T) {
	m := New()
	ctx := context.Background()
	tn, err := m.Tunnel.CreateTunnel(ctx, "a", cloudflare.CreateTunnelParams{Name: "t"})
	require.NoError(t, err)

	cfg := cloudflare.TunnelConfig{
		Ingress: []cloudflare.IngressEntry{
			{Hostname: "foo.example.com", Service: "http://svc:80"},
			{Service: "http_status:404"},
		},
	}
	put, err := m.Tunnel.PutConfiguration(ctx, "a", tn.ID, cfg)
	require.NoError(t, err)
	require.Equal(t, 1, put.Version, "version starts at 1 on first PUT")

	got, err := m.Tunnel.GetConfiguration(ctx, "a", tn.ID)
	require.NoError(t, err)
	require.Equal(t, 1, got.Version)
	require.Len(t, got.Config.Ingress, 2)

	put2, err := m.Tunnel.PutConfiguration(ctx, "a", tn.ID, cfg)
	require.NoError(t, err)
	require.Equal(t, 2, put2.Version, "version increments on each PUT")
}

func TestMock_GetToken(t *testing.T) {
	m := New()
	ctx := context.Background()
	tn, err := m.Tunnel.CreateTunnel(ctx, "a", cloudflare.CreateTunnelParams{Name: "t"})
	require.NoError(t, err)

	tok, err := m.Tunnel.GetToken(ctx, "a", tn.ID)
	require.NoError(t, err)
	require.NotEmpty(t, tok)

	// Stable per tunnel.
	tok2, err := m.Tunnel.GetToken(ctx, "a", tn.ID)
	require.NoError(t, err)
	require.Equal(t, tok, tok2)
}

func TestMock_ListConnections_Empty(t *testing.T) {
	m := New()
	ctx := context.Background()
	tn, err := m.Tunnel.CreateTunnel(ctx, "a", cloudflare.CreateTunnelParams{Name: "t"})
	require.NoError(t, err)
	conns, err := m.Tunnel.ListConnections(ctx, "a", tn.ID)
	require.NoError(t, err)
	require.Empty(t, conns)
}

func TestMock_GetTunnel_NotFound_DualSentinel(t *testing.T) {
	m := New()
	ctx := context.Background()
	_, err := m.Tunnel.GetTunnel(ctx, "a", "nonexistent")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNotFound), "should satisfy mock-level sentinel")
	require.True(t, errors.Is(err, cloudflare.ErrTunnelNotFound), "should satisfy cloudflare-level sentinel")
}

func TestMock_InjectError_Tunnel(t *testing.T) {
	m := New()
	ctx := context.Background()
	boom := errors.New("boom")
	m.InjectError("Tunnel.CreateTunnel", boom)

	_, err := m.Tunnel.CreateTunnel(ctx, "a", cloudflare.CreateTunnelParams{Name: "t"})
	require.ErrorIs(t, err, boom, "injected error should surface")

	// Injection is one-shot — subsequent calls succeed.
	_, err = m.Tunnel.CreateTunnel(ctx, "a", cloudflare.CreateTunnelParams{Name: "t"})
	require.NoError(t, err)
}

func TestMock_ImplementsTunnelClient(t *testing.T) {
	var _ cloudflare.TunnelClient = (*tunnelMock)(nil) // compile-time assertion
}
