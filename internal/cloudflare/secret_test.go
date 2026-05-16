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
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSecret_RedactsButExposes(t *testing.T) {
	s := Secret("super-secret-token")
	require.Equal(t, "****", s.String())
	//nolint:staticcheck // S1025: intentionally exercises the fmt %s path (Stringer integration), distinct from s.String() asserted above
	require.Equal(t, "****", fmt.Sprintf("%s", s))
	require.Equal(t, "****", fmt.Sprintf("%v", s))
	require.Equal(t, "****", fmt.Sprintf("%#v", s))
	b, err := json.Marshal(struct{ T Secret }{s})
	require.NoError(t, err)
	require.NotContains(t, string(b), "super-secret-token")
	require.Equal(t, "super-secret-token", s.Expose())
	require.Equal(t, "", Secret("").String()) // empty stays empty, not "****"
}

// TestSecret_AllFmtVerbsRedact pins the real contract: Go's fmt applies the
// Stringer for %q/%x/%X too, so EVERY fmt verb redacts. The only residual is
// an explicit string()/[]byte() conversion (i.e. Expose()) — asserted here
// so a future regression that drops the Stringer (re-exposing %q/%x) breaks
// loudly and the Secret doc stays accurate.
func TestSecret_AllFmtVerbsRedact(t *testing.T) {
	s := Secret("super-secret-token")
	require.NotContains(t, fmt.Sprintf("%q", s), "super-secret-token")
	require.Equal(t, `"****"`, fmt.Sprintf("%q", s))
	require.NotContains(t, fmt.Sprintf("%x", s), "7375706572") // hex of "super"
	require.Equal(t, "2a2a2a2a", fmt.Sprintf("%x", s))         // hex of "****"
	require.Equal(t, "2A2A2A2A", fmt.Sprintf("%X", s))
	// The sole residual is an explicit conversion — exactly what Expose() is.
	require.Equal(t, "super-secret-token", string(s))
}
