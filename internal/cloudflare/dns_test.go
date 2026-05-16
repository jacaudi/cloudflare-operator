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
	"testing"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
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
