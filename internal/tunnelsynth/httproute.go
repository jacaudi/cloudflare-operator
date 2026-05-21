/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package tunnelsynth

import (
	"regexp"
	"strings"

	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// GatewayOrigin is the tunnel-facing origin derived from a Gateway. The
// HTTPRoute and TLSRoute translators route to the Gateway's internal Service,
// not directly to the route's backendRefs, so the origin URL is the same for
// every route attached to a given Gateway.
type GatewayOrigin struct {
	// Hostname is the Gateway listener hostname (the tunnel-apex hostname).
	// HTTPRoutes attached to this Gateway CNAME to it; it is informational
	// here — the ingress service URL is taken from Service below.
	Hostname string
	// Service is the cloudflared service URL pointing at the Gateway's
	// internal Service (e.g. http://envoy-gw.gw-ns.svc.cluster.local:80).
	Service string
	// IsTLS indicates the listener is a TLS-mode listener. TLSRoute callers
	// supply a tcp:// service; HTTPRoute callers supply http(s)://.
	IsTLS bool
}

// TranslateHTTPRoute converts an HTTPRoute attached to a tunnel-targeted
// Gateway into ingress contributions, one per hostname/rule pair. Pure
// function; no K8s client, no logger.
//
// Mapping rules:
//   - rules with any filters set produce no contributions and emit a
//     IncompatibleFilters warning; cloudflared has no equivalent surface.
//   - header / queryParam matches keep the rule but emit UnsupportedValue
//     since cloudflared routing has no header/query predicates.
//   - rules with multiple BackendRefs keep the rule (first backend wins
//     conceptually — translator routes to the Gateway origin regardless)
//     and emit UnsupportedValue.
//   - path matches are translated to a regex string on the contribution:
//     Exact -> "^value$" with regex meta-characters quoted, PathPrefix ->
//     "^value" quoted, RegularExpression -> "^value" (passthrough; anchor
//     added when missing).
func TranslateHTTPRoute(rt *gwv1.HTTPRoute, gw GatewayOrigin, defaults Defaults) ([]IngressContribution, []TranslateWarning) {
	var contribs []IngressContribution
	var warns []TranslateWarning

	hostnames := make([]string, 0, len(rt.Spec.Hostnames))
	for _, h := range rt.Spec.Hostnames {
		hostnames = append(hostnames, string(h))
	}

	for _, rule := range rt.Spec.Rules {
		if len(rule.Filters) > 0 {
			warns = append(warns, TranslateWarning{
				Reason:  conventions.ReasonIncompatibleFilters,
				Message: "HTTPRoute filters are not supported; rule dropped",
			})
			continue
		}
		// Gateway API rule.Matches are OR-ed; each path-bearing match must
		// reach the wire. Collect every path produced by the rule's matches.
		// Rules with no path-bearing match still emit one contribution per
		// hostname with empty Path.
		paths := []string{}
		for _, m := range rule.Matches {
			if m.Path != nil && m.Path.Value != nil {
				paths = append(paths, pathToRegex(m.Path.Type, *m.Path.Value))
			}
		}
		if len(paths) == 0 {
			paths = []string{""}
		}
		for _, m := range rule.Matches {
			if len(m.Headers) > 0 {
				warns = append(warns, TranslateWarning{
					Reason:  conventions.ReasonUnsupportedValue,
					Message: "header match ignored (cloudflared has no equivalent)",
				})
				break
			}
		}
		for _, m := range rule.Matches {
			if len(m.QueryParams) > 0 {
				warns = append(warns, TranslateWarning{
					Reason:  conventions.ReasonUnsupportedValue,
					Message: "queryParam match ignored",
				})
				break
			}
		}
		if len(rule.BackendRefs) > 1 {
			warns = append(warns, TranslateWarning{
				Reason:  conventions.ReasonUnsupportedValue,
				Message: "weighted backends not supported; first backend wins",
			})
		}
		for _, h := range hostnames {
			for _, p := range paths {
				contribs = append(contribs, IngressContribution{
					Hostname:         h,
					Path:             p,
					Service:          gw.Service,
					NoTLSVerify:      copyBoolPtr(defaults.NoTLSVerifyDefault),
					OriginServerName: copyStringPtr(defaults.OriginServerNameDefault),
				})
			}
		}
	}
	return contribs, warns
}

// copyBoolPtr returns a new pointer to the same value, or nil if input is nil.
// Used by translators to avoid sharing default-pointer state across
// contributions when callers rebuild Defaults mid-tick.
func copyBoolPtr(p *bool) *bool {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// copyStringPtr returns a new pointer to the same value, or nil if input is
// nil. Mirrors copyBoolPtr; used for the same defensive reason.
func copyStringPtr(p *string) *string {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// pathToRegex maps an HTTPRoute path match to the cloudflared `path` regex.
// Returns "" when type is nil or unrecognized. Exact and Prefix match values
// are quoted via regexp.QuoteMeta so values like "/api/v1" with regex
// meta-characters do not behave unexpectedly. RegularExpression values are
// passed through verbatim (anchor added when missing).
func pathToRegex(t *gwv1.PathMatchType, value string) string {
	if t == nil {
		return ""
	}
	switch *t {
	case gwv1.PathMatchExact:
		return "^" + regexp.QuoteMeta(value) + "$"
	case gwv1.PathMatchPathPrefix:
		return "^" + regexp.QuoteMeta(value)
	case gwv1.PathMatchRegularExpression:
		if strings.HasPrefix(value, "^") {
			return value
		}
		return "^" + value
	default:
		return ""
	}
}
