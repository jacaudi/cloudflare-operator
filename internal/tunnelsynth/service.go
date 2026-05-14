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

package tunnelsynth

import (
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// Defaults is the per-tunnel translator context: tunnel-level originRequest
// defaults + the configured caPool path (when the tunnel CR sets an
// originCASecretRef).
type Defaults struct {
	// CAPoolPath, when non-empty, is the absolute path inside cloudflared
	// Pods where the origin-CA bundle is mounted
	// (e.g. /etc/cloudflared/ca/bundle.crt). Threaded into HTTPS
	// contributions when the source has not opted out of TLS verification.
	CAPoolPath string
	// NoTLSVerifyDefault is the tunnel-level default applied when the source
	// has no own annotation (or has an unparseable value).
	NoTLSVerifyDefault *bool
	// OriginServerNameDefault is the tunnel-level default origin SAN applied
	// when the source has no own annotation.
	OriginServerNameDefault *string
}

// TranslateWarning is a non-fatal issue surfaced back to the reconciler for
// status writes on the originating source object. Warnings are values, not
// errors — a contribution slice may be returned alongside warnings about
// dropped or partially-translated rules.
type TranslateWarning struct {
	Reason  string
	Message string
}

// TranslateService converts a Service + its annotations into ingress
// contributions. Pure function; no K8s client, no logger.
//
// Required annotations:
//   - cloudflare.io/tunnel="true" — caller's responsibility to filter; this
//     function does not re-check the gate.
//   - cloudflare.io/hostnames — comma-separated list of public FQDNs.
//
// Optional annotations:
//   - cloudflare.io/port — override Service port selection (must match a
//     declared port on the Service).
//   - cloudflare.io/scheme — "http" (default) or "https".
//   - cloudflare.io/no-tls-verify — truthy vocabulary per
//     conventions.ParseTruthy; unparseable values fall through to
//     Defaults.NoTLSVerifyDefault.
//   - cloudflare.io/origin-server-name — verbatim string SAN override.
//
// When the resolved scheme is https, the source has not opted out of TLS
// verification, and Defaults.CAPoolPath is set, the contribution gets
// CAPoolPath threaded through so the resolver can emit originRequest.caPool.
func TranslateService(svc *corev1.Service, defaults Defaults) ([]IngressContribution, []TranslateWarning) {
	ann := svc.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	hostnames := splitCSV(ann[conventions.AnnotationHostnames])
	if len(hostnames) == 0 {
		return nil, []TranslateWarning{{
			Reason:  "MissingHostnames",
			Message: "service requires cloudflare.io/hostnames annotation",
		}}
	}

	port, perr := resolveServicePort(svc, ann[conventions.AnnotationPort])
	if perr != nil {
		return nil, []TranslateWarning{{Reason: "InvalidPort", Message: perr.Error()}}
	}

	scheme := ann[conventions.AnnotationScheme]
	if scheme == "" {
		scheme = "http"
	}

	noTLS := parseTruthyOrInherit(ann[conventions.AnnotationNoTLSVerify], defaults.NoTLSVerifyDefault)

	osn := ann[conventions.AnnotationOriginServerName]
	var osnPtr *string
	if osn != "" {
		osnPtr = &osn
	} else if defaults.OriginServerNameDefault != nil {
		// Copy so callers can mutate Defaults later without affecting cached
		// contributions.
		v := *defaults.OriginServerNameDefault
		osnPtr = &v
	}

	var caPool *string
	if scheme == "https" && (noTLS == nil || !*noTLS) && defaults.CAPoolPath != "" {
		p := defaults.CAPoolPath
		caPool = &p
	}

	svcURL := fmt.Sprintf("%s://%s.%s.svc.cluster.local:%d", scheme, svc.Name, svc.Namespace, port)

	out := make([]IngressContribution, 0, len(hostnames))
	for _, h := range hostnames {
		out = append(out, IngressContribution{
			Hostname:         h,
			Service:          svcURL,
			NoTLSVerify:      noTLS,
			OriginServerName: osnPtr,
			CAPoolPath:       caPool,
		})
	}
	return out, nil
}

// splitCSV splits a comma-separated annotation value, trimming whitespace
// around each entry and dropping empties. Returns nil for an empty/blank
// input. Case-sensitive: tokens are passed through as written. Hostnames
// are not lowercased here — Cloudflare normalizes them server-side.
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// resolveServicePort returns the Service port to forward to. When override is
// non-empty it must (a) parse as an int in [1, 65535] and (b) match a port
// declared on the Service. With no override the first declared port is used;
// a Service with no ports is an error.
func resolveServicePort(svc *corev1.Service, override string) (int32, error) {
	if override != "" {
		p, err := strconv.Atoi(override)
		if err != nil {
			return 0, fmt.Errorf("invalid port annotation %q: %w", override, err)
		}
		if p < 1 || p > 65535 {
			return 0, fmt.Errorf("port %d out of range", p)
		}
		for _, sp := range svc.Spec.Ports {
			if sp.Port == int32(p) {
				return int32(p), nil
			}
		}
		return 0, fmt.Errorf("port %d not present on Service", p)
	}
	if len(svc.Spec.Ports) == 0 {
		return 0, fmt.Errorf("service has no ports")
	}
	return svc.Spec.Ports[0].Port, nil
}

// parseTruthyOrInherit parses an annotation value into *bool, falling back to
// the supplied default (which may itself be nil) when the value is empty or
// outside the truthy vocabulary.
func parseTruthyOrInherit(v string, dflt *bool) *bool {
	if v == "" {
		return dflt
	}
	b, err := conventions.ParseTruthy(v)
	if err != nil {
		return dflt
	}
	return &b
}
