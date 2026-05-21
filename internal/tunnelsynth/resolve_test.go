/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package tunnelsynth

import (
	"testing"

	"github.com/stretchr/testify/require"

	cf "github.com/jacaudi/cloudflare-operator/internal/cloudflare"
)

func TestResolve_EmptyContributions_StillAppendsCatchAll(t *testing.T) {
	cfg, conflicts := Resolve(nil, ResolveOpts{CatchAllService: "http_status:404"})
	require.Empty(t, conflicts)
	require.Len(t, cfg.Ingress, 1)
	require.Equal(t, "http_status:404", cfg.Ingress[0].Service)
	require.Empty(t, cfg.Ingress[0].Hostname, "catch-all has no hostname")
}

func TestResolve_DeterministicOrdering(t *testing.T) {
	contribs := []ContributionWithSource{
		{IngressContribution: IngressContribution{Hostname: "b.example.com", Service: "http://b:80"}, Source: SourceKey{Kind: "Service", Namespace: "ns", Name: "b"}},
		{IngressContribution: IngressContribution{Hostname: "a.example.com", Service: "http://a:80"}, Source: SourceKey{Kind: "Service", Namespace: "ns", Name: "a"}},
	}
	cfg, _ := Resolve(contribs, ResolveOpts{CatchAllService: "http_status:404"})
	require.Len(t, cfg.Ingress, 3)
	require.Equal(t, "a.example.com", cfg.Ingress[0].Hostname)
	require.Equal(t, "b.example.com", cfg.Ingress[1].Hostname)
	require.Equal(t, "http_status:404", cfg.Ingress[2].Service)
}

func TestResolve_HostnameConflict_LexicographicWinner(t *testing.T) {
	contribs := []ContributionWithSource{
		{IngressContribution: IngressContribution{Hostname: "x.example.com", Service: "http://A:80"}, Source: SourceKey{Kind: "Service", Namespace: "ns", Name: "z"}},
		{IngressContribution: IngressContribution{Hostname: "x.example.com", Service: "http://B:80"}, Source: SourceKey{Kind: "HTTPRoute", Namespace: "ns", Name: "a"}},
	}
	cfg, conflicts := Resolve(contribs, ResolveOpts{CatchAllService: "http_status:404"})
	// (HTTPRoute, ns, a) < (Service, ns, z) — HTTPRoute is the winner.
	require.Len(t, cfg.Ingress, 2) // 1 winner + catch-all
	require.Equal(t, "http://B:80", cfg.Ingress[0].Service)
	require.Len(t, conflicts, 1)
	require.Equal(t, "x.example.com", conflicts[0].Hostname)
	require.Equal(t, "z", conflicts[0].Loser.Name)
	require.Equal(t, "a", conflicts[0].Winner.Name)
}

func TestResolve_FallbackHTTPStatusOverride(t *testing.T) {
	cfg, _ := Resolve(nil, ResolveOpts{CatchAllService: "http_status:503"})
	require.Equal(t, "http_status:503", cfg.Ingress[len(cfg.Ingress)-1].Service)
}

func TestResolve_PathPreserved(t *testing.T) {
	contribs := []ContributionWithSource{
		{IngressContribution: IngressContribution{Hostname: "a.example.com", Path: "^/api", Service: "http://x:80"}, Source: SourceKey{Kind: "HTTPRoute", Namespace: "ns", Name: "r"}},
	}
	cfg, _ := Resolve(contribs, ResolveOpts{CatchAllService: "http_status:404"})
	require.Len(t, cfg.Ingress, 2)
	require.Equal(t, "^/api", cfg.Ingress[0].Path)
}

func TestResolve_NoTLSVerifyAndOSNThreaded(t *testing.T) {
	b := true
	osn := "origin.example.com"
	contribs := []ContributionWithSource{
		{IngressContribution: IngressContribution{Hostname: "a.example.com", Service: "https://x:443", NoTLSVerify: &b, OriginServerName: &osn}, Source: SourceKey{Kind: "Service", Namespace: "ns", Name: "s"}},
	}
	cfg, _ := Resolve(contribs, ResolveOpts{CatchAllService: "http_status:404"})
	require.Len(t, cfg.Ingress, 2)
	require.NotNil(t, cfg.Ingress[0].OriginRequest)
	require.NotNil(t, cfg.Ingress[0].OriginRequest.NoTLSVerify)
	require.True(t, *cfg.Ingress[0].OriginRequest.NoTLSVerify)
	require.NotNil(t, cfg.Ingress[0].OriginRequest.OriginServerName)
	require.Equal(t, "origin.example.com", *cfg.Ingress[0].OriginRequest.OriginServerName)
}

// TestResolve_ReturnsTunnelConfigType verifies Resolve returns a
// cf.TunnelConfig (lock-in for the wire-format contract).
func TestResolve_ReturnsTunnelConfigType(t *testing.T) {
	cfg, _ := Resolve(nil, ResolveOpts{CatchAllService: "http_status:404"})
	var _ cf.TunnelConfig = cfg //nolint:staticcheck // QF1011: the explicit type is the compile-time wire-format contract assertion this test exists to enforce
}
