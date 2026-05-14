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

package conventions

import (
	"errors"
	"fmt"
	"strings"
)

// AnnotationPrefix is the reserved prefix for operator-recognized annotations.
// Foundation §7 reserves the namespace; spec 3 populates specific names.
const AnnotationPrefix = "cloudflare.io/"

// IsReservedAnnotation reports whether a key lives under the operator's
// reserved annotation namespace.
func IsReservedAnnotation(key string) bool {
	return strings.HasPrefix(key, AnnotationPrefix)
}

// --- Append-only ---
// Specific annotation name constants land in spec 3's plan (per Foundation §6.1.1
// append-only contract). Foundation only declares the prefix above.

// --- Spec 3 appends: annotation name constants + truthiness parser ---

// Tunnel attachment family. Apply to Service or Gateway.
const (
	AnnotationTunnel     = "cloudflare.io/tunnel"
	AnnotationTunnelName = "cloudflare.io/tunnel-name"
	AnnotationHostnames  = "cloudflare.io/hostnames"
	// AnnotationGatewayService is REQUIRED on a Gateway when AnnotationTunnel
	// is set. Format: "<namespace>/<name>" or "<namespace>/<name>:<port>".
	// Names the K8s Service that cloudflared forwards requests to for this
	// Gateway's hostnames. No label-based fallback (Gateway implementations
	// expose their listener Service differently — explicit annotation is
	// the only reliable contract). Missing → GatewayServiceUnspecified.
	AnnotationGatewayService   = "cloudflare.io/gateway-service"
	AnnotationNoTLSVerify      = "cloudflare.io/no-tls-verify"
	AnnotationOriginServerName = "cloudflare.io/origin-server-name"
	AnnotationPort             = "cloudflare.io/port"
	AnnotationScheme           = "cloudflare.io/scheme"
)

// DNS-only family. Zone-controller-side; no tunnel involvement.
const (
	AnnotationDNSRecord = "cloudflare.io/dns-record"
	AnnotationDNSTarget = "cloudflare.io/dns-target"
)

// Shared (applied to emitted CloudflareDNSRecord CRs).
const (
	AnnotationZoneRef = "cloudflare.io/zone-ref"
	AnnotationProxied = "cloudflare.io/proxied"
	AnnotationTTL     = "cloudflare.io/ttl"
	AnnotationAdopt   = "cloudflare.io/adopt"
)

// ErrUnrecognizedTruthy is returned by ParseTruthy for values outside the
// accepted vocabulary.
var ErrUnrecognizedTruthy = errors.New("unrecognized truthy value")

// ParseTruthy interprets a string annotation value as a boolean.
//
// Recognized true values (case-insensitive, leading/trailing whitespace
// trimmed): "true", "yes", "enable", "enabled".
// Recognized false values: "false", "no", "disable", "disabled".
// Any other input — including empty string, "1", "0", numeric strings — is
// rejected with ErrUnrecognizedTruthy (callers branch on a typed condition).
//
// Case-sensitivity contract: the comparison is intentionally case-INsensitive.
// Whitespace handling: intentionally trimmed on both sides before comparison.
func ParseTruthy(v string) (bool, error) {
	s := strings.ToLower(strings.TrimSpace(v))
	switch s {
	case "true", "yes", "enable", "enabled":
		return true, nil
	case "false", "no", "disable", "disabled":
		return false, nil
	default:
		return false, fmt.Errorf("%w: %q", ErrUnrecognizedTruthy, v)
	}
}
