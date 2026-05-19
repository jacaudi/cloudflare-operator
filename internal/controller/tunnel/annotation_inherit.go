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
// returns a tunnelsynth.Defaults populated for the HTTPRoute/TLSRoute
// translator call sites.
//
//   - cloudflare.io/no-tls-verify is parsed via conventions.ParseTruthy.
//     Unrecognized values (per ParseTruthy: empty string, "1", arbitrary
//     strings) leave NoTLSVerifyDefault nil — translators then fall back to
//     the tunnel-level default. This matches the read-from-annotation contract
//     documented on Defaults.NoTLSVerifyDefault.
//   - cloudflare.io/origin-server-name is read verbatim. Empty leaves the
//     default nil.
//   - CAPoolPath is intentionally NOT set here. It's tunnel-level state
//     wired into Defaults at the Gateway-source layer (see gateway_source_*),
//     not derivable from per-route annotations.
func defaultsFromAnnotations(ann map[string]string) tunnelsynth.Defaults {
	var d tunnelsynth.Defaults
	if v, err := conventions.ParseTruthy(ann[conventions.AnnotationNoTLSVerify]); err == nil {
		d.NoTLSVerifyDefault = &v
	}
	if s := ann[conventions.AnnotationOriginServerName]; s != "" {
		d.OriginServerNameDefault = &s
	}
	return d
}
