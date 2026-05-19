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
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// publishableListenerHostnames returns listener hostnames the gateway-source
// actually publishes records for: HTTP/HTTPS and TLS listeners with a
// non-empty hostname. TCP/UDP (and unset) are excluded — not published, can't
// back a route chain. Single source of truth for the protocol filter.
func publishableListenerHostnames(gw *gwv1.Gateway) []string {
	out := make([]string, 0, len(gw.Spec.Listeners))
	for _, l := range gw.Spec.Listeners {
		if l.Hostname == nil || *l.Hostname == "" {
			continue
		}
		switch l.Protocol {
		case gwv1.HTTPProtocolType, gwv1.HTTPSProtocolType, gwv1.TLSProtocolType:
			out = append(out, string(*l.Hostname))
		}
	}
	return out
}

// isWildcardHost reports whether h is a DNS wildcard: the bare "*" or a
// "*."-prefixed name. A wildcard is an invalid CNAME target (Cloudflare 9007),
// so a wildcard-only Gateway cannot back a route chain without an apex override.
func isWildcardHost(h string) bool { return strings.HasPrefix(h, "*.") || h == "*" }

// gatewayApexOverride parses cloudflare.io/gateway-apex. valid=true only when
// present, non-empty (trimmed), and a DNS1123 subdomain (which also rejects
// '*'). present=true whenever the key exists with a non-empty trimmed value
// (used to distinguish "set but invalid" -> Warning).
func gatewayApexOverride(gw *gwv1.Gateway) (host string, valid bool, present bool) {
	raw, ok := gw.GetAnnotations()[conventions.AnnotationGatewayApex]
	if !ok {
		return "", false, false
	}
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", false, false
	}
	if len(validation.IsDNS1123Subdomain(v)) != 0 {
		return "", false, true // present but invalid
	}
	return v, true, true
}

// chainContentFor resolves the CNAME content a per-route chain record must use
// (design hybrid):
//
//	valid override                                   -> (override,       false, false)
//	no/invalid override + >=1 concrete published host -> (tn.TunnelCNAME, false, overrideInvalid)
//	no/invalid override + wildcard-only published     -> ("",            true,  overrideInvalid)
func chainContentFor(gw *gwv1.Gateway, tn *v2alpha1.CloudflareTunnel) (content string, blocked bool, overrideInvalid bool) {
	host, valid, present := gatewayApexOverride(gw)
	if valid {
		return host, false, false
	}
	// valid is false here; if the annotation was present it was set but
	// failed DNS1123 validation -> treat as an invalid override.
	overrideInvalid = present
	hasConcrete := false
	for _, h := range publishableListenerHostnames(gw) {
		if !isWildcardHost(h) {
			hasConcrete = true
			break
		}
	}
	if hasConcrete {
		return tn.Status.TunnelCNAME, false, overrideInvalid
	}
	return "", true, overrideInvalid
}
