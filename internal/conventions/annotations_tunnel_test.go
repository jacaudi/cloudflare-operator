/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package conventions

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnnotation_TunnelAttachmentFamily(t *testing.T) {
	require.Equal(t, "cloudflare.io/tunnel", AnnotationTunnel)
	require.Equal(t, "cloudflare.io/tunnel-name", AnnotationTunnelName)
	require.Equal(t, "cloudflare.io/hostnames", AnnotationHostnames)
	require.Equal(t, "cloudflare.io/gateway-service", AnnotationGatewayService)
	require.Equal(t, "cloudflare.io/no-tls-verify", AnnotationNoTLSVerify)
	require.Equal(t, "cloudflare.io/origin-server-name", AnnotationOriginServerName)
	require.Equal(t, "cloudflare.io/port", AnnotationPort)
	require.Equal(t, "cloudflare.io/scheme", AnnotationScheme)
	require.Equal(t, "cloudflare.io/gateway-apex", AnnotationGatewayApex)
}

func TestAnnotation_DNSOnlyFamily(t *testing.T) {
	require.Equal(t, "cloudflare.io/dns-record", AnnotationDNSRecord)
	require.Equal(t, "cloudflare.io/dns-target", AnnotationDNSTarget)
}

func TestAnnotation_Shared(t *testing.T) {
	require.Equal(t, "cloudflare.io/zone-ref", AnnotationZoneRef)
	require.Equal(t, "cloudflare.io/zone-ref-namespace", AnnotationZoneRefNamespace)
	require.Equal(t, "cloudflare.io/proxied", AnnotationProxied)
	require.Equal(t, "cloudflare.io/ttl", AnnotationTTL)
	require.Equal(t, "cloudflare.io/adopt", AnnotationAdopt)
}

func TestAnnotation_TunnelAutoMgmtFamily(t *testing.T) {
	require.Equal(t, "cloudflare.io/auto-created", AnnotationAutoCreated)
}

func TestAnnotation_ForceReconcileFamily(t *testing.T) {
	require.Equal(t, "cloudflare.io/reconcile-at", AnnotationReconcileAt)
}

func TestParseTruthy_Cases(t *testing.T) {
	cases := []struct {
		in   string
		want bool
		err  bool
	}{
		{"true", true, false}, {"True", true, false}, {"TRUE", true, false},
		{"yes", true, false}, {"enable", true, false}, {"enabled", true, false},
		{"false", false, false}, {"False", false, false},
		{"no", false, false}, {"disable", false, false}, {"disabled", false, false},
		{"  true  ", true, false}, // trimmed
		{"", false, true}, {"banana", false, true}, {"1", false, true}, {"0", false, true},
	}
	for _, c := range cases {
		got, err := ParseTruthy(c.in)
		if c.err {
			require.Error(t, err, "input %q", c.in)
			require.True(t, errors.Is(err, ErrUnrecognizedTruthy), "input %q", c.in)
		} else {
			require.NoError(t, err, "input %q", c.in)
			require.Equal(t, c.want, got, "input %q", c.in)
		}
	}
}
