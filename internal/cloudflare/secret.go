/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package cloudflare

// Secret is a credential string that redacts itself on every fmt path —
// %s/%v/%q/%x/%X all route through the Stringer, %#v through GoStringer,
// structured loggers through String(), and JSON through MarshalJSON. (Go's
// fmt applies a Stringer for %q/%x/%X too, so e.g. %q prints "****" and %x
// prints the hex of "****", not the plaintext.)
//
// The ONLY way to obtain the plaintext is an explicit string()/[]byte()
// conversion — which is exactly what Expose() is. That makes every leak
// path a grep-able, compiler-visible string()/Expose() call rather than an
// accidental format-verb mistake.
type Secret string

// String redacts. Empty stays empty (not "****") so absence is distinguishable.
func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return "****"
}

// GoString redacts under %#v too.
func (s Secret) GoString() string { return s.String() }

// MarshalJSON renders the redaction marker, never the plaintext.
func (s Secret) MarshalJSON() ([]byte, error) {
	if s == "" {
		return []byte(`""`), nil
	}
	return []byte(`"****"`), nil
}

// Expose returns the raw secret. Call ONLY at the trust boundary (the
// cloudflare-go client construction); never log or format the result.
func (s Secret) Expose() string { return string(s) }
