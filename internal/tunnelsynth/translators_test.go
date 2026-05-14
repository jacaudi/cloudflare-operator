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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

func TestServiceTranslator_HappyPath_SingleHostname(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "svc",
			Namespace: "app-foo",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:    "true",
				conventions.AnnotationHostnames: "foo.example.com",
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	contribs, warns := TranslateService(svc, Defaults{})
	require.Empty(t, warns)
	require.Len(t, contribs, 1)
	require.Equal(t, "foo.example.com", contribs[0].Hostname)
	require.Equal(t, "http://svc.app-foo.svc.cluster.local:80", contribs[0].Service)
}

func TestServiceTranslator_MultipleHostnames(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:    "true",
				conventions.AnnotationHostnames: "a.example.com,b.example.com, c.example.com",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}},
	}
	contribs, warns := TranslateService(svc, Defaults{})
	require.Empty(t, warns)
	require.Len(t, contribs, 3)
	require.Equal(t, "a.example.com", contribs[0].Hostname)
	require.Equal(t, "b.example.com", contribs[1].Hostname)
	require.Equal(t, "c.example.com", contribs[2].Hostname)
}

func TestServiceTranslator_NoHostnamesAnnotation(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "ns",
			Annotations: map[string]string{conventions.AnnotationTunnel: "true"},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	contribs, warns := TranslateService(svc, Defaults{})
	require.Empty(t, contribs)
	require.Len(t, warns, 1)
	require.Contains(t, warns[0].Reason, "Hostnames")
}

func TestServiceTranslator_PortOverrideHonored(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:    "true",
				conventions.AnnotationHostnames: "x.example.com",
				conventions.AnnotationPort:      "9000",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}, {Port: 9000}}},
	}
	contribs, warns := TranslateService(svc, Defaults{})
	require.Empty(t, warns)
	require.Len(t, contribs, 1)
	require.Contains(t, contribs[0].Service, ":9000")
}

func TestServiceTranslator_SchemeOverrideHonored(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:    "true",
				conventions.AnnotationHostnames: "x.example.com",
				conventions.AnnotationScheme:    "https",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 443}}},
	}
	contribs, _ := TranslateService(svc, Defaults{})
	require.Len(t, contribs, 1)
	require.True(t, strings.HasPrefix(contribs[0].Service, "https://"),
		"expected https scheme, got %q", contribs[0].Service)
}

func TestServiceTranslator_NoTLSVerify(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:      "true",
				conventions.AnnotationHostnames:   "x.example.com",
				conventions.AnnotationScheme:      "https",
				conventions.AnnotationNoTLSVerify: "true",
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 443}}},
	}
	contribs, _ := TranslateService(svc, Defaults{})
	require.Len(t, contribs, 1)
	require.NotNil(t, contribs[0].NoTLSVerify)
	require.True(t, *contribs[0].NoTLSVerify)
}

// TestServiceTranslator_UnparseableNoTLSVerifyFallsThroughToDefault verifies
// that an annotation value outside the truthy vocabulary falls through to the
// tunnel-level default rather than being treated as an error.
func TestServiceTranslator_UnparseableNoTLSVerifyFallsThroughToDefault(t *testing.T) {
	dflt := true
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:      "true",
				conventions.AnnotationHostnames:   "x.example.com",
				conventions.AnnotationNoTLSVerify: "maybe", // unparseable
			},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	contribs, _ := TranslateService(svc, Defaults{NoTLSVerifyDefault: &dflt})
	require.Len(t, contribs, 1)
	require.NotNil(t, contribs[0].NoTLSVerify)
	require.True(t, *contribs[0].NoTLSVerify, "unparseable annotation must fall through to default")
}

func TestServiceTranslator_NoPortsErrors(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "svc", Namespace: "ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:    "true",
				conventions.AnnotationHostnames: "x.example.com",
			},
		},
		Spec: corev1.ServiceSpec{},
	}
	contribs, warns := TranslateService(svc, Defaults{})
	require.Empty(t, contribs)
	require.NotEmpty(t, warns)
}

func TestHTTPRouteTranslator_HappyPath(t *testing.T) {
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"foo.example.com"},
			Rules: []gwv1.HTTPRouteRule{{
				BackendRefs: []gwv1.HTTPBackendRef{{}}, // detail irrelevant — translator routes to gateway origin
			}},
		},
	}
	gwOrigin := GatewayOrigin{Hostname: "external.example.com", Service: "http://gw.gw-ns.svc.cluster.local:80"}
	contribs, warns := TranslateHTTPRoute(rt, gwOrigin, Defaults{})
	require.Empty(t, warns)
	require.Len(t, contribs, 1)
	require.Equal(t, "foo.example.com", contribs[0].Hostname)
	require.Equal(t, "http://gw.gw-ns.svc.cluster.local:80", contribs[0].Service)
}

func TestHTTPRouteTranslator_PathPrefixToRegex(t *testing.T) {
	pType := gwv1.PathMatchPathPrefix
	val := "/api"
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"x.example.com"},
			Rules: []gwv1.HTTPRouteRule{{
				Matches: []gwv1.HTTPRouteMatch{{Path: &gwv1.HTTPPathMatch{Type: &pType, Value: &val}}},
			}},
		},
	}
	contribs, _ := TranslateHTTPRoute(rt, GatewayOrigin{Hostname: "ext.example.com", Service: "http://gw.ns:80"}, Defaults{})
	require.Len(t, contribs, 1)
	require.Equal(t, "^/api", contribs[0].Path)
}

func TestHTTPRouteTranslator_RejectsFilters(t *testing.T) {
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"x.example.com"},
			Rules: []gwv1.HTTPRouteRule{
				{Filters: []gwv1.HTTPRouteFilter{{Type: gwv1.HTTPRouteFilterRequestRedirect}}}, // dropped
				{}, // kept
			},
		},
	}
	contribs, warns := TranslateHTTPRoute(rt, GatewayOrigin{Hostname: "ext.example.com", Service: "http://gw.ns:80"}, Defaults{})
	require.Len(t, contribs, 1, "filter rule dropped; rule without filter kept")
	require.NotEmpty(t, warns)
	var seen bool
	for _, w := range warns {
		if w.Reason == "IncompatibleFilters" {
			seen = true
		}
	}
	require.True(t, seen)
}

func TestHTTPRouteTranslator_HeaderMatchPartiallyInvalid(t *testing.T) {
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"x.example.com"},
			Rules: []gwv1.HTTPRouteRule{{
				Matches: []gwv1.HTTPRouteMatch{{Headers: []gwv1.HTTPHeaderMatch{{Name: "X-Test", Value: "v"}}}},
			}},
		},
	}
	contribs, warns := TranslateHTTPRoute(rt, GatewayOrigin{Hostname: "ext.example.com", Service: "http://gw.ns:80"}, Defaults{})
	require.Len(t, contribs, 1, "rule kept; header match ignored")
	require.NotEmpty(t, warns)
	var seen bool
	for _, w := range warns {
		if w.Reason == "UnsupportedValue" {
			seen = true
		}
	}
	require.True(t, seen)
}

func TestHTTPRouteTranslator_WeightedBackends(t *testing.T) {
	w50 := int32(50)
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"x.example.com"},
			Rules: []gwv1.HTTPRouteRule{{
				BackendRefs: []gwv1.HTTPBackendRef{
					{BackendRef: gwv1.BackendRef{Weight: &w50}},
					{BackendRef: gwv1.BackendRef{Weight: &w50}},
				},
			}},
		},
	}
	contribs, warns := TranslateHTTPRoute(rt, GatewayOrigin{Hostname: "ext.example.com", Service: "http://gw.ns:80"}, Defaults{})
	require.Len(t, contribs, 1, "first backend wins; weight dropped")
	require.NotEmpty(t, warns)
}

func TestTLSRouteTranslator_SurfacesClientSideRequired(t *testing.T) {
	rt := &gwv1a2.TLSRoute{ // sigs.k8s.io/gateway-api/apis/v1alpha2
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
	}
	gwOrigin := GatewayOrigin{Hostname: "ext.example.com", Service: "tcp://gw.ns:443", IsTLS: true}
	contribs, warns := TranslateTLSRoute(rt, []string{"tls.example.com"}, gwOrigin, Defaults{})
	require.Len(t, contribs, 1)
	require.Contains(t, contribs[0].Service, "tcp://")
	var seen bool
	for _, w := range warns {
		if w.Reason == "ClientSideClientRequired" {
			seen = true
		}
	}
	require.True(t, seen, "TLSRoute translator must surface ClientSideClientRequired")
}
