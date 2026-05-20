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

package tunnel

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEmittedDNSRecordName_NewShape_DropsOwnerName closes backlog #6: the
// emitted CR's metadata.Name is derived from the hostname alone, NOT the
// owner-name. Locks the new shape `<sanitizedHostname>-<8hex>` against
// regression to the old `<owner>-<host>-<hash>` doubling.
//
// Cases mirror the real prod CRs from the 2026-05-19 post-mortem:
//   - external.jacaudi.dev → external-jacaudi-dev-<8hex>
//   - jellyfin.jacaudi.dev → jellyfin-jacaudi-dev-<8hex>
func TestEmittedDNSRecordName_NewShape_DropsOwnerName(t *testing.T) {
	cases := []struct {
		name     string
		hostname string
		want     *regexp.Regexp
	}{
		{"prod_external", "external.jacaudi.dev", regexp.MustCompile(`^external-jacaudi-dev-[0-9a-f]{8}$`)},
		{"prod_jellyfin", "jellyfin.jacaudi.dev", regexp.MustCompile(`^jellyfin-jacaudi-dev-[0-9a-f]{8}$`)},
		{"single_label", "foo", regexp.MustCompile(`^foo-[0-9a-f]{8}$`)},
		{"already_hyphenated", "foo-bar.baz.example.com", regexp.MustCompile(`^foo-bar-baz-example-com-[0-9a-f]{8}$`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := emittedDNSRecordName(tc.hostname)
			require.Regexp(t, tc.want, got, "hostname=%q produced %q", tc.hostname, got)
			require.LessOrEqual(t, len(got), 63, "DNS-1123 label budget violated: %q (%d chars)", got, len(got))
		})
	}
}

// TestEmittedDNSRecordName_HashDisambiguatesAliasedHosts: two hostnames that
// sanitize to the same prefix still produce distinct CR names because the
// hash is keyed on the ORIGINAL hostname.
func TestEmittedDNSRecordName_HashDisambiguatesAliasedHosts(t *testing.T) {
	a := emittedDNSRecordName("foo.example.com")
	b := emittedDNSRecordName("foo-example-com")
	require.NotEqual(t, a, b, "aliasing hostnames must produce distinct CR names (hash differs)")
}

// TestEmittedDNSRecordName_TruncatesLongHostname: a hostname whose sanitized
// form alone exceeds the 54-char middle budget must be truncated so the
// total CR name stays ≤63 chars. Result must end alphanumerically (DNS-1123).
func TestEmittedDNSRecordName_TruncatesLongHostname(t *testing.T) {
	long := "a23456789.b23456789.c23456789.d23456789.e23456789.f23456789.example.com"
	got := emittedDNSRecordName(long)
	require.LessOrEqual(t, len(got), 63, "got %q (%d chars)", got, len(got))
	require.Regexp(t, `^[a-z0-9][a-z0-9-]*[a-z0-9]-[0-9a-f]{8}$`, got)
	require.NotContains(t, got[:len(got)-9], "--", "truncation must not produce double-hyphens just before the hash sep")
}

// TestEmittedDNSRecordName_PathologicalHostname_FallsBackToHashOnly: a
// hostname with no surviving alphanumerics must still produce a valid
// CR name — the hash alone is DNS-1123 (hex digits are alphanumeric).
func TestEmittedDNSRecordName_PathologicalHostname_FallsBackToHashOnly(t *testing.T) {
	got := emittedDNSRecordName("...___...")
	require.Regexp(t, `^[0-9a-f]{8}$`, got,
		"pathological hostname must fall back to hash-only; got %q", got)
}
