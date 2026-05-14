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
	"errors"
	"fmt"
	"net/http"
	"testing"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/zero_trust"
	"github.com/stretchr/testify/require"
)

func TestTunnelClient_ConstructorSmoke(t *testing.T) {
	defer func() { _ = recover() }()
	_ = NewTunnelClientFromCF(nil)
}

func TestTunnel_FieldShape(t *testing.T) {
	tn := Tunnel{ID: "abc", Name: "n", AccountTag: "acct"}
	require.Equal(t, "abc", tn.ID)
	require.Equal(t, "n", tn.Name)
	require.Equal(t, "acct", tn.AccountTag)
}

func TestTunnelConfiguration_RoundTrip(t *testing.T) {
	noVerify := true
	cfg := TunnelConfiguration{
		Version: 7,
		Config: TunnelConfig{
			Ingress: []IngressEntry{
				{Hostname: "foo.example.com", Service: "http://svc.ns:80",
					OriginRequest: &IngressOriginRequest{NoTLSVerify: &noVerify}},
				{Service: "http_status:404"}, // catch-all
			},
		},
	}
	require.Equal(t, 7, cfg.Version)
	require.Len(t, cfg.Config.Ingress, 2)
	require.True(t, *cfg.Config.Ingress[0].OriginRequest.NoTLSVerify)
}

func TestTunnelConnection_FieldShape(t *testing.T) {
	c := TunnelConnection{ID: "cid", ColoName: "DEN", IsPendingReconnect: false}
	require.Equal(t, "cid", c.ID)
	require.Equal(t, "DEN", c.ColoName)
	require.False(t, c.IsPendingReconnect)
}

func TestTunnelToken_String(t *testing.T) {
	tk := TunnelToken("opaque-base64-blob")
	require.Equal(t, "opaque-base64-blob", string(tk))
}

// TestClassifyTunnelAPIErr covers the tunnel error classifier. Contract
// mirrors classifyDNSAPIErr / classifyZoneAPIErr: nil pass-through, 404 →
// wrapped with ErrTunnelNotFound, any other shape preserved as-is. Also
// verifies traversal through wrapped error chains via errors.As.
func TestClassifyTunnelAPIErr(t *testing.T) {
	tests := []struct {
		name        string
		in          error
		wantNil     bool
		wantWrapped bool
	}{
		{name: "nil input returns nil", in: nil, wantNil: true},
		{name: "404 wraps ErrTunnelNotFound", in: &cfgo.Error{StatusCode: http.StatusNotFound}, wantWrapped: true},
		{name: "403 preserved (no sentinel)", in: &cfgo.Error{StatusCode: http.StatusForbidden}, wantWrapped: false},
		{name: "500 preserved (no sentinel)", in: &cfgo.Error{StatusCode: http.StatusInternalServerError}, wantWrapped: false},
		{name: "non-cfgo error preserved", in: errors.New("boom"), wantWrapped: false},
		{name: "nested 404 unwraps via errors.As", in: fmt.Errorf("outer: %w", &cfgo.Error{StatusCode: http.StatusNotFound}), wantWrapped: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyTunnelAPIErr(tc.in)
			if tc.wantNil {
				require.NoError(t, got)
				return
			}
			require.Error(t, got)
			if tc.wantWrapped {
				require.ErrorIs(t, got, ErrTunnelNotFound)
			} else {
				require.NotErrorIs(t, got, ErrTunnelNotFound)
			}
		})
	}
}

// TestToSDKConfig_RoundTrip exercises the plain-Go → SDK update params
// translation across three representative ingress shapes: a fully populated
// OriginRequest, a partially populated one, and the bare catch-all entry
// (no OriginRequest at all). The catch-all is the conventional last entry
// in any cloudflared ingress list.
func TestToSDKConfig_RoundTrip(t *testing.T) {
	noVerify := true
	serverName := "origin.example.com"
	caPool := "/etc/ssl/ca.pem"
	timeout := int32(30)

	caPoolOnly := "/only/ca.pem"

	cfg := TunnelConfig{
		Ingress: []IngressEntry{
			{
				Hostname: "full.example.com",
				Path:     "/api",
				Service:  "http://svc.ns:80",
				OriginRequest: &IngressOriginRequest{
					NoTLSVerify:           &noVerify,
					OriginServerName:      &serverName,
					CAPool:                &caPool,
					ConnectTimeoutSeconds: &timeout,
				},
			},
			{
				Hostname: "partial.example.com",
				Service:  "https://svc.ns:443",
				OriginRequest: &IngressOriginRequest{
					CAPool: &caPoolOnly,
				},
			},
			{Service: "http_status:404"}, // catch-all, no OriginRequest
		},
	}

	out := toSDKConfig(cfg)

	ingress := out.Ingress.Value
	require.Len(t, ingress, 3)

	// Entry 0 — full OriginRequest
	require.Equal(t, "full.example.com", ingress[0].Hostname.Value)
	require.Equal(t, "/api", ingress[0].Path.Value)
	require.Equal(t, "http://svc.ns:80", ingress[0].Service.Value)
	or0 := ingress[0].OriginRequest.Value
	require.True(t, or0.NoTLSVerify.Value)
	require.Equal(t, "origin.example.com", or0.OriginServerName.Value)
	require.Equal(t, "/etc/ssl/ca.pem", or0.CAPool.Value)
	require.Equal(t, int64(30), or0.ConnectTimeout.Value)

	// Entry 1 — partial OriginRequest (only CAPool)
	require.Equal(t, "partial.example.com", ingress[1].Hostname.Value)
	require.Equal(t, "https://svc.ns:443", ingress[1].Service.Value)
	or1 := ingress[1].OriginRequest.Value
	require.Equal(t, "/only/ca.pem", or1.CAPool.Value)
	// Unset fields stay zero — param.Field with the zero present-flag is
	// what the SDK marshals as "absent".
	require.False(t, or1.NoTLSVerify.Value)
	require.Equal(t, "", or1.OriginServerName.Value)
	require.Equal(t, int64(0), or1.ConnectTimeout.Value)

	// Entry 2 — catch-all, no OriginRequest projected at all.
	require.Equal(t, "http_status:404", ingress[2].Service.Value)
	require.Equal(t, "", ingress[2].Hostname.Value)
	// The Value on an unset param.Field is the zero-struct; the meaningful
	// assertion is that no fields inside it were set.
	or2 := ingress[2].OriginRequest.Value
	require.False(t, or2.NoTLSVerify.Value)
	require.Equal(t, "", or2.OriginServerName.Value)
	require.Equal(t, "", or2.CAPool.Value)
	require.Equal(t, int64(0), or2.ConnectTimeout.Value)
}

// TestMapConfigurationGetResponse_AllFields verifies the GET-side mapper
// projects all four operator-modeled OriginRequest fields when the SDK
// reports non-zero values, and projects no OriginRequest at all when every
// field is zero.
func TestMapConfigurationGetResponse_AllFields(t *testing.T) {
	resp := &zero_trust.TunnelCloudflaredConfigurationGetResponse{
		Version: 11,
		Config: zero_trust.TunnelCloudflaredConfigurationGetResponseConfig{
			Ingress: []zero_trust.TunnelCloudflaredConfigurationGetResponseConfigIngress{
				{
					Hostname: "full.example.com",
					Path:     "/api",
					Service:  "http://svc.ns:80",
					OriginRequest: zero_trust.TunnelCloudflaredConfigurationGetResponseConfigIngressOriginRequest{
						NoTLSVerify:      true,
						OriginServerName: "origin.example.com",
						CAPool:           "/etc/ssl/ca.pem",
						ConnectTimeout:   30,
					},
				},
				{
					Hostname: "bare.example.com",
					Service:  "http://svc.ns:80",
					// OriginRequest fields all zero — no projection expected.
				},
			},
		},
	}

	out := mapConfigurationGetResponse(resp)
	require.Equal(t, 11, out.Version)
	require.Len(t, out.Config.Ingress, 2)

	// Entry 0 — all four fields projected.
	e0 := out.Config.Ingress[0]
	require.Equal(t, "full.example.com", e0.Hostname)
	require.Equal(t, "/api", e0.Path)
	require.Equal(t, "http://svc.ns:80", e0.Service)
	require.NotNil(t, e0.OriginRequest)
	require.NotNil(t, e0.OriginRequest.NoTLSVerify)
	require.True(t, *e0.OriginRequest.NoTLSVerify)
	require.NotNil(t, e0.OriginRequest.OriginServerName)
	require.Equal(t, "origin.example.com", *e0.OriginRequest.OriginServerName)
	require.NotNil(t, e0.OriginRequest.CAPool)
	require.Equal(t, "/etc/ssl/ca.pem", *e0.OriginRequest.CAPool)
	require.NotNil(t, e0.OriginRequest.ConnectTimeoutSeconds)
	require.Equal(t, int32(30), *e0.OriginRequest.ConnectTimeoutSeconds)

	// Entry 1 — all SDK OriginRequest fields zero ⇒ no projection.
	e1 := out.Config.Ingress[1]
	require.Equal(t, "bare.example.com", e1.Hostname)
	require.Nil(t, e1.OriginRequest)
}

// TestMapConfigurationUpdateResponse_AllFields mirrors the GET test against
// the Update response type. Same shape, distinct SDK type — we keep a
// dedicated mapper so the two cannot drift.
func TestMapConfigurationUpdateResponse_AllFields(t *testing.T) {
	resp := &zero_trust.TunnelCloudflaredConfigurationUpdateResponse{
		Version: 12,
		Config: zero_trust.TunnelCloudflaredConfigurationUpdateResponseConfig{
			Ingress: []zero_trust.TunnelCloudflaredConfigurationUpdateResponseConfigIngress{
				{
					Hostname: "full.example.com",
					Path:     "/api",
					Service:  "http://svc.ns:80",
					OriginRequest: zero_trust.TunnelCloudflaredConfigurationUpdateResponseConfigIngressOriginRequest{
						NoTLSVerify:      true,
						OriginServerName: "origin.example.com",
						CAPool:           "/etc/ssl/ca.pem",
						ConnectTimeout:   45,
					},
				},
				{
					Hostname: "bare.example.com",
					Service:  "http://svc.ns:80",
				},
			},
		},
	}

	out := mapConfigurationUpdateResponse(resp)
	require.Equal(t, 12, out.Version)
	require.Len(t, out.Config.Ingress, 2)

	e0 := out.Config.Ingress[0]
	require.NotNil(t, e0.OriginRequest)
	require.NotNil(t, e0.OriginRequest.NoTLSVerify)
	require.True(t, *e0.OriginRequest.NoTLSVerify)
	require.NotNil(t, e0.OriginRequest.OriginServerName)
	require.Equal(t, "origin.example.com", *e0.OriginRequest.OriginServerName)
	require.NotNil(t, e0.OriginRequest.CAPool)
	require.Equal(t, "/etc/ssl/ca.pem", *e0.OriginRequest.CAPool)
	require.NotNil(t, e0.OriginRequest.ConnectTimeoutSeconds)
	require.Equal(t, int32(45), *e0.OriginRequest.ConnectTimeoutSeconds)

	e1 := out.Config.Ingress[1]
	require.Nil(t, e1.OriginRequest)
}

// TestMapConfigurationGetResponse_SymmetryWithUpdate is the regression-defense
// test: if Get and Update mappers diverge in which OriginRequest fields they
// project, the drift-detection consumer (T9) sees permanent false drift. We
// build matching field combinations on both SDK response types and assert
// the two mappers produce identical plain-Go IngressOriginRequest values.
func TestMapConfigurationGetResponse_SymmetryWithUpdate(t *testing.T) {
	// Case A: every field non-zero (everything should project).
	// Case B: only OriginServerName set (CAPool, ConnectTimeout zero; NoTLSVerify false).
	//          This is the one Fix 1 specifically guards.
	// Case C: only NoTLSVerify true.
	// Case D: only ConnectTimeout set.
	// Case E: all zero (no projection at either side).

	getResp := &zero_trust.TunnelCloudflaredConfigurationGetResponse{
		Config: zero_trust.TunnelCloudflaredConfigurationGetResponseConfig{
			Ingress: []zero_trust.TunnelCloudflaredConfigurationGetResponseConfigIngress{
				{Hostname: "a.example.com", Service: "http://a:80", OriginRequest: zero_trust.TunnelCloudflaredConfigurationGetResponseConfigIngressOriginRequest{
					NoTLSVerify: true, OriginServerName: "a.origin", CAPool: "/a", ConnectTimeout: 5,
				}},
				{Hostname: "b.example.com", Service: "http://b:80", OriginRequest: zero_trust.TunnelCloudflaredConfigurationGetResponseConfigIngressOriginRequest{
					OriginServerName: "b.origin",
				}},
				{Hostname: "c.example.com", Service: "http://c:80", OriginRequest: zero_trust.TunnelCloudflaredConfigurationGetResponseConfigIngressOriginRequest{
					NoTLSVerify: true,
				}},
				{Hostname: "d.example.com", Service: "http://d:80", OriginRequest: zero_trust.TunnelCloudflaredConfigurationGetResponseConfigIngressOriginRequest{
					ConnectTimeout: 99,
				}},
				{Hostname: "e.example.com", Service: "http://e:80"},
			},
		},
	}
	updResp := &zero_trust.TunnelCloudflaredConfigurationUpdateResponse{
		Config: zero_trust.TunnelCloudflaredConfigurationUpdateResponseConfig{
			Ingress: []zero_trust.TunnelCloudflaredConfigurationUpdateResponseConfigIngress{
				{Hostname: "a.example.com", Service: "http://a:80", OriginRequest: zero_trust.TunnelCloudflaredConfigurationUpdateResponseConfigIngressOriginRequest{
					NoTLSVerify: true, OriginServerName: "a.origin", CAPool: "/a", ConnectTimeout: 5,
				}},
				{Hostname: "b.example.com", Service: "http://b:80", OriginRequest: zero_trust.TunnelCloudflaredConfigurationUpdateResponseConfigIngressOriginRequest{
					OriginServerName: "b.origin",
				}},
				{Hostname: "c.example.com", Service: "http://c:80", OriginRequest: zero_trust.TunnelCloudflaredConfigurationUpdateResponseConfigIngressOriginRequest{
					NoTLSVerify: true,
				}},
				{Hostname: "d.example.com", Service: "http://d:80", OriginRequest: zero_trust.TunnelCloudflaredConfigurationUpdateResponseConfigIngressOriginRequest{
					ConnectTimeout: 99,
				}},
				{Hostname: "e.example.com", Service: "http://e:80"},
			},
		},
	}

	gotGet := mapConfigurationGetResponse(getResp)
	gotUpd := mapConfigurationUpdateResponse(updResp)

	require.Len(t, gotGet.Config.Ingress, 5)
	require.Len(t, gotUpd.Config.Ingress, 5)

	// Hostname/Path/Service equality is incidental; the contract under
	// test is OriginRequest projection symmetry.
	for i := range gotGet.Config.Ingress {
		require.Equal(t, gotGet.Config.Ingress[i].OriginRequest, gotUpd.Config.Ingress[i].OriginRequest,
			"OriginRequest projection diverges between Get and Update mappers at index %d", i)
	}

	// Spot-check Case B explicitly — this is the exact asymmetry Fix 1
	// addresses (OriginServerName set, NoTLSVerify zero).
	caseB := gotGet.Config.Ingress[1]
	require.NotNil(t, caseB.OriginRequest, "Case B must project OriginRequest even though NoTLSVerify is false")
	require.NotNil(t, caseB.OriginRequest.OriginServerName)
	require.Equal(t, "b.origin", *caseB.OriginRequest.OriginServerName)
	require.Nil(t, caseB.OriginRequest.NoTLSVerify, "NoTLSVerify must not be projected when SDK reports false (unset vs. explicit-false ambiguity)")

	// Case E — every field zero ⇒ no projection.
	require.Nil(t, gotGet.Config.Ingress[4].OriginRequest)
	require.Nil(t, gotUpd.Config.Ingress[4].OriginRequest)
}
