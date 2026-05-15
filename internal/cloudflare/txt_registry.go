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
	"strings"
)

// RegistryPayload is the JSON schema for a TXT companion record (V=1).
// Field names are compact because Cloudflare TXT records are capped at
// 1024 bytes; every character counts when a record may carry multiple
// ownership claims. Decoders must reject payloads whose V field is not
// equal to 1.
type RegistryPayload struct {
	// V is the schema version. Only V=1 is recognised.
	V int `json:"v"`
	// K is the Kubernetes resource kind (e.g. "CloudflareDNSRecord").
	K string `json:"k"`
	// NS is the Kubernetes namespace of the owning object.
	NS string `json:"ns"`
	// N is the Kubernetes name of the owning object.
	N string `json:"n"`
	// H is an optional content hash of the owned record (sha256:<hex>).
	// Omitted when unknown or not yet computed.
	H string `json:"h,omitempty"`
}

// AffixName returns the name of the companion TXT record for a DNS
// record identified by name.
//
// Convention:
//   - apex (no '.' in name):  prefix + "." + name
//   - subdomain:              prefix + "-" + collapsed-labels + "." + tld
//
// All-but-last dotted segments of name are joined with hyphens and
// appended to prefix, then the last segment becomes the zone label.
// For example:
//
//	AffixName("cf-txt", "test")        → "cf-txt.test"
//	AffixName("cf-txt", "foo.test")    → "cf-txt-foo.test"
//	AffixName("cf-txt", "foo.bar.test") → "cf-txt-foo-bar.test"
func AffixName(prefix, name string) string {
	dot := strings.IndexByte(name, '.')
	if dot < 0 {
		return prefix + "." + name
	}
	segs := strings.Split(name, ".")
	if len(segs) == 2 {
		return prefix + "-" + segs[0] + "." + segs[1]
	}
	head := strings.Join(segs[:len(segs)-1], "-")
	tail := segs[len(segs)-1]
	return prefix + "-" + head + "." + tail
}

// ErrUnrecognizedCodec is returned by codec decoders when the TXT
// record value is not a recognised format or the version field is
// unknown. Reconcilers should map this error to
// Reason=AdoptRefusedNoTXT so the record is left unowned.
var ErrUnrecognizedCodec = errors.New("txt registry: unrecognized codec or malformed payload")
