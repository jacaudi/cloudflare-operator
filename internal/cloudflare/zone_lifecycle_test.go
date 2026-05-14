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

// TestClassifyZoneAPIErr covers the table of inputs the classifier should
// handle: nil pass-through, 404 wrapping with the typed sentinel, and
// non-404 raw-error preservation. These cases lock in the contract that
// DeleteZone, GetZone, and any future zone-path call relies on for
// errors.Is(err, ErrZoneNotFound) traversal.
func TestClassifyZoneAPIErr(t *testing.T) {
	tests := []struct {
		name        string
		in          error
		wantNil     bool
		wantWrapped bool // true → errors.Is(out, ErrZoneNotFound) must hold
	}{
		{
			name:    "nil input returns nil",
			in:      nil,
			wantNil: true,
		},
		{
			name:        "404 cfgo.Error wraps ErrZoneNotFound",
			in:          &cfgo.Error{StatusCode: http.StatusNotFound},
			wantWrapped: true,
		},
		{
			name:        "403 cfgo.Error preserved (no sentinel)",
			in:          &cfgo.Error{StatusCode: http.StatusForbidden},
			wantWrapped: false,
		},
		{
			name:        "500 cfgo.Error preserved (no sentinel)",
			in:          &cfgo.Error{StatusCode: http.StatusInternalServerError},
			wantWrapped: false,
		},
		{
			name:        "non-cfgo error preserved",
			in:          errors.New("boom"),
			wantWrapped: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyZoneAPIErr(tc.in)
			if tc.wantNil {
				require.NoError(t, got)
				return
			}
			require.Error(t, got)
			if tc.wantWrapped {
				require.ErrorIs(t, got, ErrZoneNotFound)
			} else {
				require.NotErrorIs(t, got, ErrZoneNotFound)
			}
		})
	}
}
