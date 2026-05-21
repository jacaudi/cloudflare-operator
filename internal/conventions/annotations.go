/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
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
	// AnnotationGatewayApex (on a tunnel Gateway) sets the public apex
	// hostname per-route chain records CNAME to (and which the gateway-source
	// publishes as <apex> CNAME -> tunnel CNAME). Required for wildcard-only
	// Gateways; optional otherwise (concrete Gateways chain directly to the
	// tunnel CNAME). Empty/invalid -> Warning + listener-derived fallback or
	// (wildcard-only) blocked.
	AnnotationGatewayApex = "cloudflare.io/gateway-apex"
)

// DNS-only family. Zone-controller-side; no tunnel involvement.
const (
	AnnotationDNSRecord = "cloudflare.io/dns-record"
	AnnotationDNSTarget = "cloudflare.io/dns-target"
)

// Shared (applied to emitted CloudflareDNSRecord CRs).
const (
	AnnotationZoneRef = "cloudflare.io/zone-ref"
	// AnnotationZoneRefNamespace overrides the namespace the emitted
	// DNSRecord's spec.zoneRef resolves in. Unset → the source object's
	// namespace (back-compatible). Enables a source in namespace A to
	// resolve a CloudflareZone in namespace B.
	AnnotationZoneRefNamespace = "cloudflare.io/zone-ref-namespace"
	AnnotationProxied          = "cloudflare.io/proxied"
	AnnotationTTL              = "cloudflare.io/ttl"
	AnnotationAdopt            = "cloudflare.io/adopt"
)

// Force-reconcile family. Applies to any of the 5 CRD types.
const (
	// AnnotationReconcileAt is an opaque-token annotation that admins set
	// to force a full re-check on the next reconcile of any of the 5 CRDs
	// (Zone, ZoneConfig, DNSRecord, Ruleset, Tunnel). The operator never
	// parses the value as a time; only string equality vs.
	// status.lastReconcileToken is checked.
	AnnotationReconcileAt = "cloudflare.io/reconcile-at"
)

// Tunnel auto-management family. Applied to CloudflareTunnel CRs themselves.
const (
	// AnnotationAutoCreated is stamped on CloudflareTunnel CRs that were created
	// by EnsureTunnelCR (i.e., annotation-driven auto-creation from a Service /
	// Gateway / HTTPRoute / TLSRoute opt-in). Direct-create CRs (user
	// `kubectl apply`) lack this annotation and are never subject to auto-GC.
	// Immutable after the create.
	AnnotationAutoCreated = "cloudflare.io/auto-created"
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
