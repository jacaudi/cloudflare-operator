/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package tunnel

import (
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

// inheritAnnotation returns the route's own value for `key` if set, falling
// back to the parent Gateway's value. Per tunnel-controller design §5:
// HTTPRoute and TLSRoute annotation values flow from the route, with
// fallback to the Gateway the route attaches to.
//
// A nil Gateway is tolerated (returns the route's value or empty).
func inheritAnnotation(routeAnnotations map[string]string, gw *gwv1.Gateway, key string) string {
	if v, ok := routeAnnotations[key]; ok && v != "" {
		return v
	}
	if gw == nil {
		return ""
	}
	return gw.Annotations[key]
}

// inheritedAnnotations returns a merged annotation map where route values
// take precedence over Gateway values, scoped to the cloudflare.io/* family
// the source-emit path cares about. Threaded into EmitOpts.Annotations so
// each emitted DNSRecord reflects the effective per-Gateway defaults +
// per-route overrides.
//
// Returned map is always non-nil (callers index into it without guarding).
func inheritedAnnotations(routeAnnotations map[string]string, gw *gwv1.Gateway) map[string]string {
	keys := []string{
		conventions.AnnotationZoneRef,
		conventions.AnnotationZoneRefNamespace,
		conventions.AnnotationAdopt,
		conventions.AnnotationProxied,
		conventions.AnnotationTTL,
		conventions.AnnotationNoTLSVerify,
		conventions.AnnotationOriginServerName,
		conventions.AnnotationScheme,
	}
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		if v := inheritAnnotation(routeAnnotations, gw, k); v != "" {
			out[k] = v
		}
	}
	return out
}

// defaultsFromAnnotations reads the originRequest-shaped annotations off a
// merged annotation map (typically the output of inheritedAnnotations) and
// returns a tunnelsynth.Defaults populated for the translator call sites.
// The spec parameter supplies tunnel-CR-spec fallback values that take
// effect when the annotation map does not set the corresponding field.
//
// Precedence per field: annotation > spec > nil.
//
//   - cloudflare.io/no-tls-verify is parsed via conventions.ParseTruthy.
//     Unrecognized values (per ParseTruthy: empty string, "1", arbitrary
//     strings) leave NoTLSVerifyDefault at its spec value.
//   - cloudflare.io/origin-server-name is read verbatim. Empty leaves the
//     default at its spec value.
//   - Other cloudflared originRequest fields (caPool, connectTimeoutSeconds,
//     etc.) are deliberately not modeled. The operator's TunnelOriginRequest
//     type was trimmed to the two fields actually plumbed end-to-end.
func defaultsFromAnnotations(ann map[string]string, spec tunnelsynth.Defaults) tunnelsynth.Defaults {
	d := spec // pointers in spec are unique per call (DefaultsFor deep-copies)
	if v, err := conventions.ParseTruthy(ann[conventions.AnnotationNoTLSVerify]); err == nil {
		d.NoTLSVerifyDefault = &v
	}
	if s := ann[conventions.AnnotationOriginServerName]; s != "" {
		d.OriginServerNameDefault = &s
	}
	return d
}
