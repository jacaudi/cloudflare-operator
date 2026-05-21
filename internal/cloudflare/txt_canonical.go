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

import "strings"

// CanonicalizeTXT converts Cloudflare's RFC 1035 TXT presentation form — one
// or more whitespace-separated double-quoted character-strings — into the
// logical content the operator reasons about.
//
// It is tolerant and — outside the documented ambiguous case below — idempotent:
// any input that is not valid presentation form (no leading quote, unterminated
// quote, malformed escape, trailing non-whitespace after the final closing
// quote) is returned UNCHANGED. It never errors and never panics. This makes
// already-logical input ({"v":1,...}, v1:..., v=spf1 ..., test doubles,
// pre-fix data) a no-op and guarantees
// CanonicalizeTXT(CanonicalizeTXT(x)) == CanonicalizeTXT(x) for every input
// except the documented ambiguous case below.
//
// Escapes (RFC 1035 §5.1): \DDD is exactly three decimal digits (value
// 0-255); \X (X a non-digit) is the literal X. A 1-2 digit numeric escape or
// a >255 value is treated as malformed -> passthrough.
//
// Accepted limitation: a logical value that both begins with '"' AND is
// itself byte-valid presentation form is ambiguous. This does not occur for
// registry payloads or common SPF/DKIM/DMARC/ACME values and matches the
// limitation external-dns / terraform-provider-cloudflare carry.
func CanonicalizeTXT(raw string) string {
	s := strings.TrimSpace(raw)
	if len(s) == 0 || s[0] != '"' {
		return raw
	}
	var b strings.Builder
	i, n := 0, len(s)
	for i < n {
		if s[i] != '"' {
			return raw // expected the start of a quoted character-string
		}
		i++ // opening quote
		for {
			if i >= n {
				return raw // unterminated
			}
			c := s[i]
			if c == '"' {
				i++ // closing quote
				break
			}
			if c == '\\' {
				if i+1 >= n {
					return raw // dangling escape
				}
				e := s[i+1]
				if e >= '0' && e <= '9' {
					if i+3 >= n || !isASCIIDigit(s[i+2]) || !isASCIIDigit(s[i+3]) {
						return raw // numeric escape must be exactly 3 digits
					}
					val := int(s[i+1]-'0')*100 + int(s[i+2]-'0')*10 + int(s[i+3]-'0')
					if val > 255 {
						return raw
					}
					b.WriteByte(byte(val)) //nolint:gosec // G115: bounds-checked above (val <= 255)
					i += 4
					continue
				}
				b.WriteByte(e) // \X literal
				i += 2
				continue
			}
			b.WriteByte(c)
			i++
		}
		for i < n && (s[i] == ' ' || s[i] == '\t') {
			i++ // whitespace between character-strings
		}
	}
	return b.String()
}

func isASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }

// EncodeTXT renders a logical TXT value into RFC 1035 presentation form: the
// logical bytes are split into <=255-byte character-strings, each with '\'
// escaped as \\ and '"' escaped as \", wrapped in double quotes, joined by a
// single space. The empty value yields one empty character-string ("").
//
// It is the exact inverse of CanonicalizeTXT for any input:
// CanonicalizeTXT(EncodeTXT(x)) == x. Cloudflare requires this form for the
// TXT content field; sending content that contains '"' raw is rejected
// (API error 9207) or stored lossily. The 255 boundary is on LOGICAL bytes
// (RFC 1035 §3.3: each character-string is length-prefixed by one octet on
// the wire, so its content is limited to 255 octets). Presentation escapes
// expand the character count but not the logical-byte count: \\ and \" each
// encode one logical byte regardless of their two-character presentation width.
// Apply ONLY to logical content, never to content
// already in presentation form (see CanonicalizeTXT's documented ambiguity).
func EncodeTXT(logical string) string {
	const maxChunk = 255 // RFC 1035 §3.3: character-string content limit, in logical bytes
	var b strings.Builder
	for i := 0; i < len(logical); i += maxChunk {
		end := i + maxChunk
		if end > len(logical) {
			end = len(logical)
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('"')
		for j := i; j < end; j++ {
			switch logical[j] {
			case '\\':
				b.WriteString(`\\`)
			case '"':
				b.WriteString(`\"`)
			default:
				b.WriteByte(logical[j])
			}
		}
		b.WriteByte('"')
	}
	if b.Len() == 0 {
		return `""`
	}
	return b.String()
}
