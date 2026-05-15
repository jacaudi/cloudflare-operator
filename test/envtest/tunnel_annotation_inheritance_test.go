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

package envtest_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// TestEnvtest_HTTPRoute_InheritsAdoptFromGateway pins Design E1 §5 against a
// real apiserver: when an HTTPRoute omits cloudflare.io/adopt but the parent
// Gateway carries it, the emitted CloudflareDNSRecord must reflect the
// Gateway's value.
//
// Gotchas pre-empted (per project Phase 3 execution lessons):
//   - DNSRecord admission has CEL "has(zoneID) || has(zoneRef)": the fixture
//     creates a CloudflareZone CR and sets cloudflare.io/zone-ref on the Gateway
//     so every emitted DNSRecord carries a valid zoneRef. Routes omit zone-ref
//     deliberately — they must inherit it from the Gateway.
//   - Tunnel Status.TunnelCNAME must populate before the HTTPRoute is created
//     so the HTTPRoute reconciler's deferred-emission guard never fires. The
//     fixture waits for this before creating the route (mirrors
//     createGatewayForRouteTest in httproute_source_envtest_test.go).
//   - setupHTTPRouteEnv is reused without modification — all reconcilers
//     (Gateway, Tunnel, HTTPRoute sources) are wired and share one
//     tunnelsynth.Cache, identical to the existing HTTPRoute envtests.
func TestEnvtest_HTTPRoute_InheritsAdoptFromGateway(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupHTTPRouteEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Zone CR — admission requires has(zoneRef); the Gateway carries zone-ref,
	// so the emitted DNSRecord inherits it and passes CEL validation.
	zone := &v1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: f.ns},
		Spec: v1alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, f.c.Create(ctx, zone))

	// Backing Service for the Gateway.
	gwSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-svc", Namespace: f.ns},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	require.NoError(t, f.c.Create(ctx, gwSvc))

	// Tunnel-targeted Gateway with adopt=true AND zone-ref so inheritance
	// propagates both to the emitted DNSRecord.
	gwHostname := gwv1.Hostname("ext.example.com")
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gw", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:         "true",
				conventions.AnnotationTunnelName:     "edge",
				conventions.AnnotationGatewayService: f.ns + "/gw-svc",
				conventions.AnnotationAdopt:          "true",
				conventions.AnnotationZoneRef:        "example-com",
			},
		},
		Spec: gwv1.GatewaySpec{
			GatewayClassName: "any-class",
			Listeners: []gwv1.Listener{{
				Name: "h", Hostname: &gwHostname, Port: 80, Protocol: gwv1.HTTPProtocolType,
			}},
		},
	}
	require.NoError(t, f.c.Create(ctx, gw))

	// Wait for Status.TunnelCNAME so the HTTPRoute reconciler advances past the
	// deferred-emission guard on the first reconcile pass.
	expectedTunnel := "cf-" + f.ns + "-edge"
	require.Eventually(t, func() bool {
		var tn v1alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: expectedTunnel}, &tn); err != nil {
			return false
		}
		return tn.Status.TunnelCNAME != ""
	}, 30*time.Second, 250*time.Millisecond, "parent Gateway's tunnel %q must reach Status.TunnelCNAME", expectedTunnel)

	// HTTPRoute deliberately omits adopt and zone-ref — must inherit both from
	// the parent Gateway.
	nsRef := gwv1.Namespace(f.ns)
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "r",
			Namespace: f.ns,
			// No annotations — adopt + zone-ref must fall through from Gateway.
		},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"notes.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: &nsRef}},
			},
			Rules: []gwv1.HTTPRouteRule{{}},
		},
	}
	require.NoError(t, f.c.Create(ctx, rt))

	// The emitted CloudflareDNSRecord must carry Spec.Adopt=true (inherited
	// from the Gateway's cloudflare.io/adopt annotation).
	require.Eventually(t, func() bool {
		var list v1alpha1.CloudflareDNSRecordList
		if err := f.c.List(ctx, &list, client.InNamespace(f.ns)); err != nil {
			return false
		}
		for _, dr := range list.Items {
			if dr.Spec.Name == "notes.example.com" && dr.Spec.Type == "CNAME" {
				return dr.Spec.Adopt
			}
		}
		return false
	}, 20*time.Second, 250*time.Millisecond,
		"emitted DNSRecord for notes.example.com must carry Spec.Adopt=true (inherited from Gateway)")
}

// TestEnvtest_TLSRoute_InheritsAdoptFromGateway is the TLSRoute counterpart
// to TestEnvtest_HTTPRoute_InheritsAdoptFromGateway. Pins Design E1 §5 against
// a real apiserver: when a TLSRoute omits cloudflare.io/adopt but the parent
// Gateway carries it, the emitted CloudflareDNSRecord must reflect the
// Gateway's value.
//
// Gotchas pre-empted (per project Phase 3 execution lessons):
//   - DNSRecord admission has CEL "has(zoneID) || has(zoneRef)": the fixture
//     creates a CloudflareZone CR and sets cloudflare.io/zone-ref on the Gateway
//     so every emitted DNSRecord carries a valid zoneRef. The TLSRoute omits
//     zone-ref deliberately — it must inherit it from the Gateway.
//   - Tunnel Status.TunnelCNAME must populate before the TLSRoute is created
//     so the TLSRoute reconciler's deferred-emission guard never fires.
//   - setupTLSRouteEnv is reused without modification — all reconcilers
//     (Gateway, Tunnel, TLSRoute sources) are wired and share one cache.
func TestEnvtest_TLSRoute_InheritsAdoptFromGateway(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupTLSRouteEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Zone CR — admission requires has(zoneRef); the Gateway carries zone-ref,
	// so the emitted DNSRecord inherits it and passes CEL validation.
	zone := &v1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: f.ns},
		Spec: v1alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, f.c.Create(ctx, zone))

	// Backing Service for the Gateway.
	gwSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-svc", Namespace: f.ns},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: 443}},
		},
	}
	require.NoError(t, f.c.Create(ctx, gwSvc))

	// Tunnel-targeted Gateway with adopt=true AND zone-ref so inheritance
	// propagates both to the emitted DNSRecord.
	gwHostname := gwv1.Hostname("ext.example.com")
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gw", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:         "true",
				conventions.AnnotationTunnelName:     "edge",
				conventions.AnnotationGatewayService: f.ns + "/gw-svc",
				conventions.AnnotationAdopt:          "true",
				conventions.AnnotationZoneRef:        "example-com",
			},
		},
		Spec: gwv1.GatewaySpec{
			GatewayClassName: "any-class",
			Listeners: []gwv1.Listener{{
				Name: "tls", Hostname: &gwHostname, Port: 443, Protocol: gwv1.TLSProtocolType,
			}},
		},
	}
	require.NoError(t, f.c.Create(ctx, gw))

	// Wait for Status.TunnelCNAME so the TLSRoute reconciler advances past the
	// deferred-emission guard on the first reconcile pass.
	expectedTunnel := "cf-" + f.ns + "-edge"
	require.Eventually(t, func() bool {
		var tn v1alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: expectedTunnel}, &tn); err != nil {
			return false
		}
		return tn.Status.TunnelCNAME != ""
	}, 30*time.Second, 250*time.Millisecond, "parent Gateway's tunnel %q must reach Status.TunnelCNAME", expectedTunnel)

	// TLSRoute deliberately omits adopt and zone-ref — must inherit both from
	// the parent Gateway.
	nsRef := gwv1.Namespace(f.ns)
	rt := &gwv1a2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "r",
			Namespace: f.ns,
			// No annotations — adopt + zone-ref must fall through from Gateway.
		},
		Spec: gwv1a2.TLSRouteSpec{
			Hostnames: []gwv1.Hostname{"tls.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: &nsRef}},
			},
			// TLSRoute CRD admission requires spec.rules to be present.
			Rules: []gwv1a2.TLSRouteRule{{}},
		},
	}
	require.NoError(t, f.c.Create(ctx, rt))

	// The emitted CloudflareDNSRecord must carry Spec.Adopt=true (inherited
	// from the Gateway's cloudflare.io/adopt annotation).
	require.Eventually(t, func() bool {
		var list v1alpha1.CloudflareDNSRecordList
		if err := f.c.List(ctx, &list, client.InNamespace(f.ns)); err != nil {
			return false
		}
		for _, dr := range list.Items {
			if dr.Spec.Name == "tls.example.com" && dr.Spec.Type == "CNAME" {
				return dr.Spec.Adopt
			}
		}
		return false
	}, 20*time.Second, 250*time.Millisecond,
		"emitted DNSRecord for tls.example.com must carry Spec.Adopt=true (inherited from Gateway)")
}
