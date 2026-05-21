/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package tunnel

import (
	"testing"

	"github.com/stretchr/testify/require"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

func gwWith(anns map[string]string, listeners ...gwv1.Listener) *gwv1.Gateway {
	g := &gwv1.Gateway{}
	g.SetAnnotations(anns)
	g.Spec.Listeners = listeners
	return g
}
func httpL(host string) gwv1.Listener {
	h := gwv1.Hostname(host)
	return gwv1.Listener{Hostname: &h, Protocol: gwv1.HTTPProtocolType}
}
func tcpL(host string) gwv1.Listener {
	h := gwv1.Hostname(host)
	return gwv1.Listener{Hostname: &h, Protocol: gwv1.TCPProtocolType}
}
func tnCNAME(s string) *v2alpha1.CloudflareTunnel {
	tn := &v2alpha1.CloudflareTunnel{}
	tn.Status.TunnelCNAME = s
	return tn
}

func TestChainContentFor(t *testing.T) {
	const A = conventions.AnnotationGatewayApex
	tn := tnCNAME("uuid.cfargotunnel.com")
	cases := []struct {
		name        string
		gw          *gwv1.Gateway
		wantContent string
		wantBlocked bool
		wantInvalid bool
	}{
		{"valid-override-wildcard-listener", gwWith(map[string]string{A: "external.example.com"}, httpL("*.example.com")), "external.example.com", false, false},
		{"valid-override-concrete-listener", gwWith(map[string]string{A: "edge.example.com"}, httpL("app.example.com")), "edge.example.com", false, false},
		{"no-override-concrete-listener", gwWith(nil, httpL("app.example.com")), "uuid.cfargotunnel.com", false, false},
		{"no-override-wildcard-only", gwWith(nil, httpL("*.example.com")), "", true, false},
		{"no-override-mixed-has-concrete", gwWith(nil, httpL("*.example.com"), httpL("app.example.com")), "uuid.cfargotunnel.com", false, false},
		{"invalid-override-concrete", gwWith(map[string]string{A: "*.bad"}, httpL("app.example.com")), "uuid.cfargotunnel.com", false, true},
		{"invalid-override-wildcard-only", gwWith(map[string]string{A: "not a host"}, httpL("*.example.com")), "", true, true},
		{"empty-override-concrete", gwWith(map[string]string{A: "  "}, httpL("app.example.com")), "uuid.cfargotunnel.com", false, false},
		{"tcp-only-concrete-not-published-is-wildcard-only", gwWith(nil, httpL("*.example.com"), tcpL("raw.example.com")), "", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			content, blocked, invalid := chainContentFor(c.gw, tn)
			require.Equal(t, c.wantContent, content)
			require.Equal(t, c.wantBlocked, blocked)
			require.Equal(t, c.wantInvalid, invalid)
		})
	}
}
