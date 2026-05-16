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
	"github.com/cloudflare/cloudflare-go/v6/shared"
	"github.com/stretchr/testify/require"
)

func TestZoneConfigClient_ConstructorSmoke(t *testing.T) {
	// NewZoneConfigClientFromCF stores cf without dereferencing it, so nil
	// is legal at construction time and must produce a non-nil client.
	require.NotNil(t, NewZoneConfigClientFromCF(nil))
}

// TestClassifyZoneConfigAPIErr covers the zone-config error classifier. The
// contract is: a 403 whose error message looks plan-tier ("plan",
// "subscription", "upgrade") is wrapped with ErrPlanTierInsufficient; other
// 403s and non-403 errors pass through unwrapped so callers don't mislabel
// token-scope or account-suspension 403s as plan limitations.
func TestClassifyZoneConfigAPIErr(t *testing.T) {
	tests := []struct {
		name        string
		in          error
		wantNil     bool
		wantWrapped bool // true → errors.Is(out, ErrPlanTierInsufficient) must hold
	}{
		{name: "nil input returns nil", in: nil, wantNil: true},
		{
			name: "403 plan-tier message wraps ErrPlanTierInsufficient",
			in: &cfgo.Error{
				StatusCode: http.StatusForbidden,
				Errors:     []shared.ErrorData{{Message: "feature not available on your plan"}},
			},
			wantWrapped: true,
		},
		{
			name: "403 subscription message wraps ErrPlanTierInsufficient",
			in: &cfgo.Error{
				StatusCode: http.StatusForbidden,
				Errors:     []shared.ErrorData{{Message: "requires Pro subscription"}},
			},
			wantWrapped: true,
		},
		{
			name: "403 upgrade message wraps ErrPlanTierInsufficient",
			in: &cfgo.Error{
				StatusCode: http.StatusForbidden,
				Errors:     []shared.ErrorData{{Message: "please upgrade to access"}},
			},
			wantWrapped: true,
		},
		{
			name: "403 token-scope message preserved (no sentinel)",
			in: &cfgo.Error{
				StatusCode: http.StatusForbidden,
				Errors:     []shared.ErrorData{{Message: "insufficient token scope"}},
			},
			wantWrapped: false,
		},
		{
			name:        "404 preserved (no sentinel)",
			in:          &cfgo.Error{StatusCode: http.StatusNotFound},
			wantWrapped: false,
		},
		{
			name:        "500 preserved (no sentinel)",
			in:          &cfgo.Error{StatusCode: http.StatusInternalServerError},
			wantWrapped: false,
		},
		{name: "non-cfgo error preserved", in: errors.New("boom"), wantWrapped: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyZoneConfigAPIErr(tc.in)
			if tc.wantNil {
				require.NoError(t, got)
				return
			}
			require.Error(t, got)
			if tc.wantWrapped {
				require.ErrorIs(t, got, ErrPlanTierInsufficient)
			} else {
				require.NotErrorIs(t, got, ErrPlanTierInsufficient)
			}
		})
	}
}

// TestIsPlanTier403 covers the pure keyword match used by
// classifyZoneConfigAPIErr to decide whether a 403 is plan-tier shaped.
// Tests the message-keyword logic in isolation from the StatusCode gate.
func TestIsPlanTier403(t *testing.T) {
	tests := []struct {
		name string
		msgs []string
		want bool
	}{
		{name: "no errors slice", msgs: nil, want: false},
		{name: "empty message", msgs: []string{""}, want: false},
		{name: "message contains plan (lowercase)", msgs: []string{"requires paid plan"}, want: true},
		{name: "message contains PLAN (case-insensitive)", msgs: []string{"upgrade your PLAN"}, want: true},
		{name: "message contains subscription", msgs: []string{"requires subscription"}, want: true},
		{name: "message contains upgrade", msgs: []string{"please Upgrade"}, want: true},
		{name: "unrelated message", msgs: []string{"token scope insufficient"}, want: false},
		{name: "second message matches", msgs: []string{"foo", "wants plan"}, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := make([]shared.ErrorData, 0, len(tc.msgs))
			for _, m := range tc.msgs {
				errs = append(errs, shared.ErrorData{Message: m})
			}
			apiErr := &cfgo.Error{StatusCode: http.StatusForbidden, Errors: errs}
			require.Equal(t, tc.want, isPlanTier403(apiErr))
		})
	}
}
