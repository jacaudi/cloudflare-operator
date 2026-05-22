/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package cloudflare

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncodeTXT_RoundTripInverseOfCanonicalize(t *testing.T) {
	cases := []string{
		"hello",
		"v=spf1 include:_spf.example.com ~all",
		`{"v":1,"k":"CloudflareDNSRecord","ns":"network","n":"external-jacaudi-dev","h":"sha256:f2bb0441db266c09a1a176eefabb5d028ae21e3379b960f1ab6808fe32220bfe"}`,
		"",
		"a\\b\"c",                       // embedded backslash and quote
		strings.Repeat("x", 300),        // >255 -> multi character-string
		strings.Repeat(`{"k":"v"}`, 40), // >255 AND quote-bearing
		"v1:" + strings.Repeat("QUJD", 80) + ":" + strings.Repeat("REVG", 80), // AES-shaped, >255
		// Discriminating cases: first byte is '"', so CanonicalizeTXT(x) != x.
		// CanonicalizeTXT strips the outer quoting for these (x starts with '"'), so CanonicalizeTXT(x) != x; a no-op encoder therefore fails the round-trip here.
		// A no-op encoder that returns its argument unchanged FAILS these.
		"\"",               // lone double-quote
		`"already-quoted"`, // starts and ends with '"'
		`"foo" "bar"`,      // starts with '"', multi-chunk-looking
	}
	for _, in := range cases {
		enc := EncodeTXT(in)
		require.Equal(t, in, CanonicalizeTXT(enc),
			"CanonicalizeTXT(EncodeTXT(x)) must == x for %q", in)
	}
}

func TestEncodeTXT_PresentationForm(t *testing.T) {
	require.Equal(t, `"hello"`, EncodeTXT("hello"))
	require.Equal(t, `""`, EncodeTXT(""), "empty -> one empty character-string")
	require.Equal(t, `"a\"b"`, EncodeTXT(`a"b`), "interior quote escaped")
	require.Equal(t, `"a\\b"`, EncodeTXT(`a\b`), "interior backslash escaped")
	// JSON payload: every interior " becomes \" and the whole is wrapped.
	got := EncodeTXT(`{"v":1}`)
	require.Equal(t, `"{\"v\":1}"`, got)
	require.False(t, strings.Contains(got[1:len(got)-1], `"`) && !strings.Contains(got, `\"`),
		"no unescaped interior quote")
}

func TestEncodeTXT_ChunksAt255LogicalBytes(t *testing.T) {
	in := strings.Repeat("a", 600)
	enc := EncodeTXT(in)
	// 3 character-strings: 255 + 255 + 90, space-joined.
	parts := strings.Split(enc, " ")
	require.Equal(t, 3, len(parts), "600 bytes -> 3 chunks")
	for _, p := range parts {
		require.LessOrEqual(t, len(CanonicalizeTXT(p)), 255, "chunk must be <=255 logical bytes")
	}
	require.Equal(t, in, CanonicalizeTXT(enc))
}
