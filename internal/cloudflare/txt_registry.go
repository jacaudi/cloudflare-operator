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
	"fmt"
	"strings"
)

// RegistryPayload is the decoded external-dns-compatible TXT registry entry.
//
// Wire format:
//
//	"heritage=external-dns,external-dns/owner=<owner>,external-dns/resource=<kind>/<ns>/<name>"
//
// SourceKind / SourceNamespace / SourceName may be empty for legacy external-dns
// registry entries that only carry owner information. Callers should treat
// missing resource information as "adoptable but unlinked".
type RegistryPayload struct {
	Owner           string
	SourceKind      string
	SourceNamespace string
	SourceName      string
}

// AffixConfig controls how the companion TXT's record name is derived from the
// managed record's FQDN. Defaults (all empty strings) yield external-dns's
// default affix scheme.
type AffixConfig struct {
	Prefix              string
	Suffix              string
	WildcardReplacement string
}

// ErrRegistryMalformed is returned when a TXT payload cannot be parsed as an
// external-dns registry entry.
//
// This error deliberately covers BOTH cases:
//  1. The TXT is not an external-dns registry record at all (e.g. an SPF
//     record, a user-managed TXT, or any other non-registry payload whose
//     heritage key is missing or not "external-dns").
//  2. The TXT looks like a registry record but is structurally invalid
//     (e.g. missing owner, malformed resource tuple, missing '=' separator).
//
// Callers MUST NOT try to distinguish these two cases: both mean "do not
// treat this TXT as ownership metadata." Wrapping them under a single
// sentinel keeps adoption logic simple — any error from DecodeRegistryPayload
// means "ignore this record for registry purposes."
var ErrRegistryMalformed = errors.New("txt registry: malformed payload")

// EncodeRegistryPayload produces the canonical quoted wire form of a
// RegistryPayload.
func EncodeRegistryPayload(p RegistryPayload) string {
	var b strings.Builder
	b.WriteString(`"heritage=external-dns,external-dns/owner=`)
	b.WriteString(p.Owner)
	if p.SourceKind != "" {
		b.WriteString(",external-dns/resource=")
		b.WriteString(p.SourceKind)
		b.WriteString("/")
		b.WriteString(p.SourceNamespace)
		b.WriteString("/")
		b.WriteString(p.SourceName)
	}
	b.WriteString(`"`)
	return b.String()
}

// DecodeRegistryPayload parses the wire form produced by EncodeRegistryPayload.
// Accepts both quoted and unquoted forms (Cloudflare's DNS API sometimes strips
// surrounding quotes on read).
func DecodeRegistryPayload(raw string) (RegistryPayload, error) {
	s := strings.Trim(raw, `"`)
	kv := map[string]string{}
	for part := range strings.SplitSeq(s, ",") {
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			return RegistryPayload{}, fmt.Errorf("%w: missing '=' in %q", ErrRegistryMalformed, part)
		}
		kv[strings.TrimSpace(key)] = strings.TrimSpace(val)
	}
	if kv["heritage"] != "external-dns" {
		return RegistryPayload{}, fmt.Errorf("%w: heritage != external-dns", ErrRegistryMalformed)
	}
	owner, ok := kv["external-dns/owner"]
	if !ok || owner == "" {
		return RegistryPayload{}, fmt.Errorf("%w: missing external-dns/owner", ErrRegistryMalformed)
	}
	p := RegistryPayload{Owner: owner}
	if res := kv["external-dns/resource"]; res != "" {
		parts := strings.SplitN(res, "/", 3)
		if len(parts) != 3 {
			return RegistryPayload{}, fmt.Errorf("%w: resource must be kind/ns/name, got %q", ErrRegistryMalformed, res)
		}
		p.SourceKind = parts[0]
		p.SourceNamespace = parts[1]
		p.SourceName = parts[2]
	}
	return p, nil
}

// AffixName returns the companion-TXT record name for a managed record at fqdn
// with the given record type. Matches external-dns's default affix scheme.
func AffixName(fqdn, recordType string, cfg AffixConfig) string {
	// Replace wildcard leaf.
	if cfg.WildcardReplacement != "" && strings.HasPrefix(fqdn, "*.") {
		fqdn = cfg.WildcardReplacement + fqdn[1:] // "*.example.com" -> "any.example.com"
	}
	// Default type prefix if user hasn't configured a custom prefix/suffix.
	typePrefix := ""
	if cfg.Prefix == "" && cfg.Suffix == "" {
		switch recordType {
		case "A", "AAAA":
			typePrefix = "a-"
		case "CNAME":
			typePrefix = "cname-"
		default:
			typePrefix = strings.ToLower(recordType) + "-"
		}
	}
	switch {
	case cfg.Prefix != "":
		return cfg.Prefix + fqdn
	case cfg.Suffix != "":
		// Inject suffix before the first dot (the leaf label).
		dot := strings.IndexByte(fqdn, '.')
		if dot < 0 {
			return fqdn + cfg.Suffix
		}
		return fqdn[:dot] + cfg.Suffix + fqdn[dot:]
	default:
		return typePrefix + fqdn
	}
}
