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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalizeTXT_Table(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"single-quoted", `"hello"`, "hello"},
		{"multi-string-concat", `"foo" "bar"`, "foobar"},
		{"multi-string-tab-sep", "\"a\"\t\"b\"", "ab"},
		{"escaped-quote", `"a\"b"`, `a"b`},
		{"escaped-backslash", `"a\\b"`, `a\b`},
		{"decimal-escape-A", `"\065"`, "A"},
		{"decimal-escape-zero", `"\000"`, "\x00"},
		{"decimal-escape-255", `"\255"`, "\xff"},
		{"escaped-literal-nondigit", `"a\xb"`, "axb"},
		{"empty-quoted", `""`, ""},
		{"json-passthrough", `{"v":1,"k":"X"}`, `{"v":1,"k":"X"}`},
		{"v1-passthrough", "v1:QUJD:REVG", "v1:QUJD:REVG"},
		{"bare-token-passthrough", "hello", "hello"},
		{"empty-passthrough", "", ""},
		{"unterminated-passthrough", `"abc`, `"abc`},
		{"trailing-garbage-passthrough", `"abc"x`, `"abc"x`},
		{"short-numeric-escape-passthrough", `"\65"`, `"\65"`},
		{"overflow-numeric-escape-passthrough", `"\300"`, `"\300"`},
		{"dangling-backslash-at-eof", `"\`, `"\`},
		{"dangling-numeric-1digit-at-eof", `"\1`, `"\1`},
		{"dangling-numeric-2digit-at-eof", `"\12`, `"\12`},
		{"whitespace-only-passthrough", "   ", "   "},
		{"leading-space-then-quote", `  "hi"`, "hi"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CanonicalizeTXT(c.in)
			require.Equal(t, c.want, got)
		})
	}
}

func TestCanonicalizeTXT_Idempotent(t *testing.T) {
	for _, in := range []string{`"hello"`, `"a" "b"`, `{"v":1}`, "v1:AAA:BBB", "plain", ""} {
		once := CanonicalizeTXT(in)
		require.Equal(t, once, CanonicalizeTXT(once), "f(f(x)) must equal f(x) for %q", in)
	}
}

// TestCanonicalizeTXT_NonIdempotentLimitation pins the documented "accepted
// limitation": a logical value that itself begins with '"' and is byte-valid
// presentation form is re-parsed on a second pass, so CanonicalizeTXT is NOT
// idempotent for that class. This does not occur for registry payloads
// (JSON / "v1:" base64) or SPF/DKIM/DMARC/ACME values, which is why it is an
// accepted limitation rather than a defect. This test is a regression anchor:
// if this behavior ever changes, the limitation's scope must be re-evaluated
// and the doc comment + design §8 updated accordingly.
func TestCanonicalizeTXT_NonIdempotentLimitation(t *testing.T) {
	once := CanonicalizeTXT(`"\"hello\""`)
	require.Equal(t, `"hello"`, once, "first pass strips the outer quoted string, yielding a value that is itself presentation form")
	twice := CanonicalizeTXT(once)
	require.Equal(t, "hello", twice, "second pass re-parses that value (the documented limitation)")
	require.NotEqual(t, once, twice, "demonstrates CanonicalizeTXT is NOT idempotent on a logical value that is itself valid presentation form")
}

func TestCanonicalizeTXT_LargeAESEnvelopeSplit(t *testing.T) {
	c := aesCodec{key: makeKey(t, 7)}
	p := RegistryPayload{V: 1, K: "CloudflareDNSRecord", NS: "media", N: strings.Repeat("x", 300)}
	envelope, err := c.Encode(p)
	require.NoError(t, err)
	require.Greater(t, len(envelope), 255, "test setup: envelope must exceed 255 bytes")

	// Emulate Cloudflare splitting into <=255-byte quoted character-strings.
	var b strings.Builder
	for i := 0; i < len(envelope); i += 255 {
		end := i + 255
		if end > len(envelope) {
			end = len(envelope)
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('"')
		b.WriteString(envelope[i:end]) // base64 + "v1:" + ":" contain no '"' or '\'
		b.WriteByte('"')
	}
	got := CanonicalizeTXT(b.String())
	require.Equal(t, envelope, got, "split character-strings must reassemble to the exact envelope")

	decoded, err := c.Decode(got)
	require.NoError(t, err)
	require.Equal(t, p, decoded, "reassembled envelope must decode back to the payload")
}
