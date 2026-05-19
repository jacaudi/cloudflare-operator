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
	"net/http"
	"strings"
	"testing"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/dns"
	"github.com/stretchr/testify/require"
)

func TestDNSClient_ConstructorSmoke(t *testing.T) {
	// NewDNSClientFromCF stores cf without dereferencing it, so nil is legal
	// at construction time and must produce a non-nil client.
	require.NotNil(t, NewDNSClientFromCF(nil))
}

// TestClassifyDNSAPIErr covers the DNS-record error classifier. The contract
// mirrors classifyZoneAPIErr: nil pass-through, 404 → wrapped with the
// ErrRecordNotFound sentinel, and any other shape preserved as-is.
func TestClassifyDNSAPIErr(t *testing.T) {
	tests := []struct {
		name        string
		in          error
		wantNil     bool
		wantWrapped bool
	}{
		{name: "nil input returns nil", in: nil, wantNil: true},
		{name: "404 wraps ErrRecordNotFound", in: &cfgo.Error{StatusCode: http.StatusNotFound}, wantWrapped: true},
		{name: "403 preserved (no sentinel)", in: &cfgo.Error{StatusCode: http.StatusForbidden}, wantWrapped: false},
		{name: "500 preserved (no sentinel)", in: &cfgo.Error{StatusCode: http.StatusInternalServerError}, wantWrapped: false},
		{name: "non-cfgo error preserved", in: errors.New("boom"), wantWrapped: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyDNSAPIErr(tc.in)
			if tc.wantNil {
				require.NoError(t, got)
				return
			}
			require.Error(t, got)
			if tc.wantWrapped {
				require.ErrorIs(t, got, ErrRecordNotFound)
			} else {
				require.NotErrorIs(t, got, ErrRecordNotFound)
			}
		})
	}
}

func TestMapRecordResponse_TXTCanonicalized(t *testing.T) {
	r := &dns.RecordResponse{
		ID:      "r1",
		Name:    "cf-txt.test",
		Type:    dns.RecordResponseTypeTXT,
		Content: `"foo" "bar"`,
		TTL:     1,
	}
	got := mapRecordResponse(r)
	require.Equal(t, "foobar", got.Content, "TXT content must be canonicalized at the SDK boundary")
}

func TestMapRecordResponse_NonTXTUntouched(t *testing.T) {
	r := &dns.RecordResponse{
		ID:      "r2",
		Name:    "a.test",
		Type:    dns.RecordResponseTypeCNAME,
		Content: `"weird"`,
		TTL:     1,
	}
	got := mapRecordResponse(r)
	require.Equal(t, `"weird"`, got.Content, "non-TXT content must NOT be canonicalized")
}

func TestWireContent_TXTEncoded(t *testing.T) {
	logical := `{"v":1,"k":"CloudflareDNSRecord","ns":"network","n":"x","h":"sha256:abc"}`
	got := wireContent("TXT", logical)
	require.Equal(t, EncodeTXT(logical), got, "TXT content must be RFC1035-encoded for the wire")
	require.Equal(t, logical, CanonicalizeTXT(got), "must round-trip back to the logical value")
	require.True(t, strings.HasPrefix(got, `"`) && strings.HasSuffix(got, `"`), "presentation form is quoted")
}

func TestWireContent_NonTXTUntouched(t *testing.T) {
	require.Equal(t, "192.0.2.1", wireContent("A", "192.0.2.1"))
	require.Equal(t, `"weird"`, wireContent("CNAME", `"weird"`), "non-TXT passes through verbatim")
}

// TestMapRecordResponse_TXTAlreadyLogicalPassthrough anchors the steady-state
// no-churn property at the mapRecordResponse boundary: TXT content that is
// already in logical form (no RFC 1035 quoting) must pass through unchanged.
func TestMapRecordResponse_TXTAlreadyLogicalPassthrough(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{
			name:    "SPF value",
			content: "v=spf1 include:_spf.example.com ~all",
		},
		{
			name:    "registry JSON",
			content: `{"v":1,"k":"CloudflareDNSRecord"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &dns.RecordResponse{
				ID:      "r3",
				Name:    "txt.test",
				Type:    dns.RecordResponseTypeTXT,
				Content: tc.content,
				TTL:     1,
			}
			got := mapRecordResponse(r)
			require.Equal(t, tc.content, got.Content, "already-logical TXT content must be byte-identical at mapRecordResponse boundary")
		})
	}
}

// TestRecordTypeTXT_InSyncWithSDKReadConstant guards the read/write gate
// symmetry: the write path gates on the local recordTypeTXT constant while
// mapRecordResponse (read) gates on the cloudflare-go SDK constant
// dns.RecordResponseTypeTXT. They are coupled only by convention. If a future
// SDK change ever altered that value, the write side would silently stop
// RFC1035-encoding TXT content while the read side kept decoding it —
// silently re-introducing the Cloudflare quote-bearing TXT bug with no other
// failing test. This assertion makes that drift fail loudly instead.
func TestRecordTypeTXT_InSyncWithSDKReadConstant(t *testing.T) {
	require.Equal(t, string(dns.RecordResponseTypeTXT), recordTypeTXT,
		"write-side recordTypeTXT must equal the SDK read-side dns.RecordResponseTypeTXT; "+
			"if this fails, an SDK change desynced the TXT read/write gates")
}
