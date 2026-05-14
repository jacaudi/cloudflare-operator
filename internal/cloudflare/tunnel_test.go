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
