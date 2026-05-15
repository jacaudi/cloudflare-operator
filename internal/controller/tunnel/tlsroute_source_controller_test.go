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
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	reconcilelib "github.com/jacaudi/cloudflare-operator/internal/reconcile"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

// tlsRtScheme registers core + gateway-api v1 + v1alpha2 + operator CRDs.
// TLSRoute lives in v1alpha2; the embedded CommonRouteSpec / RouteStatus are
// aliases to the v1 types, so v1 must also be installed.
func tlsRtScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, gwv1.Install(s))
	require.NoError(t, gwv1a2.Install(s))
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

// mkTLSParentGw constructs a tunnel-targeted Gateway with a TLS listener.
// The cloudflare.io/gateway-service annotation is REQUIRED (no label fallback).
func mkTLSParentGw(name, ns string) *gwv1.Gateway {
	h := gwv1.Hostname("tls.example.com")
	return &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:         "true",
				conventions.AnnotationTunnelName:     "edge",
				conventions.AnnotationGatewayService: ns + "/gw-svc",
			},
		},
		Spec: gwv1.GatewaySpec{
			Listeners: []gwv1.Listener{{
				Name: "tls", Hostname: &h, Port: 443, Protocol: gwv1.TLSProtocolType,
			}},
		},
	}
}

// mkTLSGwSvc returns the underlying Service the Gateway annotation points at.
// Port is the IN-CLUSTER port cloudflared dials, not the listener's public port.
func mkTLSGwSvc(name, ns string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8443}}},
	}
}

// TestTLSRouteSource_HappyPath_StampsClientSideClientRequired drives the
// canonical attach path: a TLSRoute attached to a tunnel-targeted Gateway's
// TLS listener produces one tcp:// IngressContribution per hostname AND
// always surfaces the ClientSideClientRequired PartiallyInvalid condition per
// design §4.3 (TLSRoute hostnames are browser-unreachable; clients must reach
// them via `cloudflared access tcp` or WARP).
func TestTLSRouteSource_HappyPath_StampsClientSideClientRequired(t *testing.T) {
	gw := mkTLSParentGw("gw", "gw-ns")
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v1alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	gwSvc := mkTLSGwSvc("gw-svc", "gw-ns")
	rt := &gwv1a2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1a2.TLSRouteSpec{
			Hostnames: []gwv1.Hostname{"secure.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{
					Name:      gwv1.ObjectName("gw"),
					Namespace: ptrNs("gw-ns"),
				}},
			},
		},
	}
	base := fake.NewClientBuilder().WithScheme(tlsRtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1a2.TLSRoute{}, &v1alpha1.CloudflareTunnel{}, &v1alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	cache := tunnelsynth.NewCache()
	r := &TLSRouteSourceReconciler{Client: c, Scheme: tlsRtScheme(t), Cache: cache}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	snap := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "gw-ns", Name: "cf-gw-ns-edge"})
	require.Len(t, snap, 1)
	require.Equal(t, "secure.example.com", snap[0].Hostname)

	// DNSRecord CR emitted: CNAME secure.example.com → tls.example.com (chain hop).
	var list v1alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &list))
	require.Len(t, list.Items, 1)
	require.Equal(t, "secure.example.com", list.Items[0].Spec.Name)
	require.NotNil(t, list.Items[0].Spec.Content)
	require.Equal(t, "tls.example.com", *list.Items[0].Spec.Content)
	require.Equal(t, "CNAME", list.Items[0].Spec.Type)

	// Parent status: Accepted=True/TunnelAttached AND PartiallyInvalid=True/
	// ClientSideClientRequired (the latter always stamped per §4.3).
	var got gwv1a2.TLSRoute
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: "r"}, &got))
	require.Len(t, got.Status.Parents, 1)
	require.Equal(t, gwv1.ObjectName("gw"), got.Status.Parents[0].ParentRef.Name)
	require.Equal(t, gwv1.GatewayController("cloudflare.io/tunnel-controller"), got.Status.Parents[0].ControllerName)
	var sawAccepted, sawClientSide bool
	for _, cond := range got.Status.Parents[0].Conditions {
		if cond.Type == conventions.ConditionTypeAccepted && cond.Status == metav1.ConditionTrue && cond.Reason == conventions.ReasonTunnelAttached {
			sawAccepted = true
		}
		if cond.Type == conventions.ConditionTypePartiallyInvalid && cond.Status == metav1.ConditionTrue && cond.Reason == conventions.ReasonClientSideClientRequired {
			sawClientSide = true
		}
	}
	require.True(t, sawAccepted, "expected Accepted=True/TunnelAttached")
	require.True(t, sawClientSide, "expected PartiallyInvalid=True/ClientSideClientRequired always stamped")
}

// TestTLSRouteSource_TCPProtocolURL asserts that the synthesized service URL
// uses the tcp:// scheme (not http(s)://) per design §4.3.
func TestTLSRouteSource_TCPProtocolURL(t *testing.T) {
	gw := mkTLSParentGw("gw", "gw-ns")
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v1alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	gwSvc := mkTLSGwSvc("gw-svc", "gw-ns")
	rt := &gwv1a2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1a2.TLSRouteSpec{
			Hostnames:       []gwv1.Hostname{"secure.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(tlsRtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1a2.TLSRoute{}, &v1alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &TLSRouteSourceReconciler{Client: c, Scheme: tlsRtScheme(t), Cache: cache}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	snap := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "gw-ns", Name: "cf-gw-ns-edge"})
	require.Len(t, snap, 1)
	require.True(t, strings.HasPrefix(snap[0].Service, "tcp://"), "expected tcp:// scheme, got %q", snap[0].Service)
	// Port comes from the resolved Gateway service (annotation/first-port fallback),
	// NOT the listener port (443). The Service's first port is 8443.
	require.Equal(t, "tcp://gw-svc.gw-ns.svc.cluster.local:8443", snap[0].Service)
}

// TestTLSRouteSource_NoTunnelTargetedParent verifies a TLSRoute whose parent
// is not tunnel-targeted is a silent no-op: no cache write, no DNSRecord
// emission, no status touched.
func TestTLSRouteSource_NoTunnelTargetedParent(t *testing.T) {
	otherGw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "other-gw", Namespace: "gw-ns"},
	}
	rt := &gwv1a2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1a2.TLSRouteSpec{
			Hostnames:       []gwv1.Hostname{"x.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{{Name: "other-gw", Namespace: ptrNs("gw-ns")}}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(tlsRtScheme(t)).WithObjects(otherGw, rt).
		WithStatusSubresource(&gwv1a2.TLSRoute{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &TLSRouteSourceReconciler{Client: c, Scheme: tlsRtScheme(t), Cache: cache}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	var list v1alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &list))
	require.Empty(t, list.Items)

	var got gwv1a2.TLSRoute
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: "r"}, &got))
	require.Empty(t, got.Status.Parents, "no tunnel-targeted parent — no status touched")
}

// TestTLSRouteSource_MultiParent_OnlyTunnelTargetedTouched verifies that when
// a TLSRoute attaches to multiple parents (only one tunnel-targeted), the
// status entries we write are confined to the tunnel-targeted parent.
func TestTLSRouteSource_MultiParent_OnlyTunnelTargetedTouched(t *testing.T) {
	gw := mkTLSParentGw("gw", "gw-ns")
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v1alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	otherGw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "other-gw", Namespace: "gw-ns"},
	}
	gwSvc := mkTLSGwSvc("gw-svc", "gw-ns")
	rt := &gwv1a2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1a2.TLSRouteSpec{
			Hostnames: []gwv1.Hostname{"x.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{
				{Name: "other-gw", Namespace: ptrNs("gw-ns")},
				{Name: "gw", Namespace: ptrNs("gw-ns")},
			}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(tlsRtScheme(t)).WithObjects(gw, otherGw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1a2.TLSRoute{}, &v1alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	r := &TLSRouteSourceReconciler{Client: c, Scheme: tlsRtScheme(t), Cache: tunnelsynth.NewCache()}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	var got gwv1a2.TLSRoute
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: "r"}, &got))
	require.Len(t, got.Status.Parents, 1)
	require.Equal(t, gwv1.ObjectName("gw"), got.Status.Parents[0].ParentRef.Name)
}

// TestTLSRouteSource_PreservesOtherParentStatusEntry verifies that when
// another controller has already written a status entry for a NON-tunnel
// parent, our reconcile does NOT clobber it. Mirror of T12's regression test
// against the parent-only-status-write contract (§4.2 / Q3 lock).
func TestTLSRouteSource_PreservesOtherParentStatusEntry(t *testing.T) {
	gw := mkTLSParentGw("gw", "gw-ns")
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v1alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	otherGw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "other-gw", Namespace: "other-ns"},
	}
	gwSvc := mkTLSGwSvc("gw-svc", "gw-ns")
	rt := &gwv1a2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1a2.TLSRouteSpec{
			Hostnames: []gwv1.Hostname{"x.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{
				{Name: "other-gw", Namespace: ptrNs("other-ns")},
				{Name: "gw", Namespace: ptrNs("gw-ns")},
			}},
		},
		Status: gwv1a2.TLSRouteStatus{
			RouteStatus: gwv1.RouteStatus{
				Parents: []gwv1.RouteParentStatus{{
					ParentRef:      gwv1.ParentReference{Name: "other-gw", Namespace: ptrNs("other-ns")},
					ControllerName: gwv1.GatewayController("other.io/other-controller"),
					Conditions: []metav1.Condition{{
						Type:               "Accepted",
						Status:             metav1.ConditionTrue,
						Reason:             "OtherReason",
						LastTransitionTime: metav1.Now(),
					}},
				}},
			},
		},
	}
	base := fake.NewClientBuilder().WithScheme(tlsRtScheme(t)).WithObjects(gw, otherGw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1a2.TLSRoute{}, &v1alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	r := &TLSRouteSourceReconciler{Client: c, Scheme: tlsRtScheme(t), Cache: tunnelsynth.NewCache()}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	var got gwv1a2.TLSRoute
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: "r"}, &got))
	require.Len(t, got.Status.Parents, 2, "both entries preserved")

	var foundOther, foundOurs bool
	for _, ps := range got.Status.Parents {
		if ps.ParentRef.Name == "other-gw" {
			foundOther = true
			require.Equal(t, gwv1.GatewayController("other.io/other-controller"), ps.ControllerName)
			require.NotEmpty(t, ps.Conditions)
			require.Equal(t, "OtherReason", ps.Conditions[0].Reason)
		}
		if ps.ParentRef.Name == "gw" {
			foundOurs = true
			require.Equal(t, gwv1.GatewayController("cloudflare.io/tunnel-controller"), ps.ControllerName)
		}
	}
	require.True(t, foundOther, "other-controller parent entry must be preserved")
	require.True(t, foundOurs, "our tunnel-controller parent entry must be present")
}

// TestTLSRouteSource_DeleteSweepsCache verifies that deleting a TLSRoute
// clears the cache entry written by the prior reconcile via the shared
// cacheTracker.
func TestTLSRouteSource_DeleteSweepsCache(t *testing.T) {
	gw := mkTLSParentGw("gw", "gw-ns")
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v1alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	gwSvc := mkTLSGwSvc("gw-svc", "gw-ns")
	rt := &gwv1a2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1a2.TLSRouteSpec{
			Hostnames:       []gwv1.Hostname{"secure.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(tlsRtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1a2.TLSRoute{}, &v1alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &TLSRouteSourceReconciler{Client: c, Scheme: tlsRtScheme(t), Cache: cache}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)
	tk := tunnelsynth.TunnelKey{Namespace: "gw-ns", Name: "cf-gw-ns-edge"}
	require.Len(t, cache.Snapshot(tk), 1)

	require.NoError(t, c.Delete(context.Background(), rt))
	_, err = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)
	require.Empty(t, cache.Snapshot(tk), "cache cleared when TLSRoute deleted")
}

// TestTLSRouteSource_MultipleHostnames_EmitsPerHostname verifies that one
// TLSRoute with N hostnames emits N contributions and N DNSRecord CRs (one
// CNAME chain per hostname).
func TestTLSRouteSource_MultipleHostnames_EmitsPerHostname(t *testing.T) {
	gw := mkTLSParentGw("gw", "gw-ns")
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v1alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	gwSvc := mkTLSGwSvc("gw-svc", "gw-ns")
	rt := &gwv1a2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1a2.TLSRouteSpec{
			Hostnames:       []gwv1.Hostname{"a.example.com", "b.example.com", "c.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(tlsRtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1a2.TLSRoute{}, &v1alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &TLSRouteSourceReconciler{Client: c, Scheme: tlsRtScheme(t), Cache: cache}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	snap := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "gw-ns", Name: "cf-gw-ns-edge"})
	require.Len(t, snap, 3, "one contribution per hostname")
	for _, ic := range snap {
		require.True(t, strings.HasPrefix(ic.Service, "tcp://"), "expected tcp:// scheme")
	}

	var list v1alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &list))
	require.Len(t, list.Items, 3, "one CloudflareDNSRecord per hostname")
}

// TestTLSRouteSource_DeferredOnEmptyTunnelCNAME verifies the no-CNAME guard:
// when the resolved tunnel CR has no TunnelCNAME populated yet, we still
// write the cache entry (so the tunnel reconciler can compute its ingress
// list) but defer DNSRecord emission. Mirror of T12's analogous HTTPRoute test.
func TestTLSRouteSource_DeferredOnEmptyTunnelCNAME(t *testing.T) {
	gw := mkTLSParentGw("gw", "gw-ns")
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		// TunnelCNAME deliberately empty — tunnel reconciler hasn't run yet.
	}
	gwSvc := mkTLSGwSvc("gw-svc", "gw-ns")
	rt := &gwv1a2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1a2.TLSRouteSpec{
			Hostnames:       []gwv1.Hostname{"secure.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(tlsRtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1a2.TLSRoute{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	cache := tunnelsynth.NewCache()
	r := &TLSRouteSourceReconciler{Client: c, Scheme: tlsRtScheme(t), Cache: cache}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	// Cache entry written for the tunnel reconciler to consume.
	snap := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "gw-ns", Name: "cf-gw-ns-edge"})
	require.Len(t, snap, 1, "cache contribution still written when tunnel CNAME is empty")

	// But no DNSRecord — DNS emission deferred until the tunnel CR populates
	// its status (so the per-route chain CNAME isn't created before the apex
	// CNAME exists). Mirrors the HTTPRoute guard.
	var list v1alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &list))
	require.Empty(t, list.Items, "DNSRecord emission deferred until tunnel CNAME populates")
}

// TestTLSRouteSource_TwoTunnelTargetedParents_FirstWins verifies design §4.2 /
// Q3 lock for TLSRoute: when a Route lists multiple tunnel-targeted parents,
// only the FIRST is honored — neither cache contributions nor a status entry
// touch the second parent. Mirror of T12's analogous HTTPRoute test.
func TestTLSRouteSource_TwoTunnelTargetedParents_FirstWins(t *testing.T) {
	// First parent: tunnel "edge" → CR "cf-gw-ns-edge".
	gw1 := mkTLSParentGw("gw1", "gw-ns")
	tn1 := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v1alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	// Second parent: distinct tunnel "edge2" → CR "cf-gw-ns-edge2".
	h2 := gwv1.Hostname("other.example.com")
	gw2 := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gw2", Namespace: "gw-ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:         "true",
				conventions.AnnotationTunnelName:     "edge2",
				conventions.AnnotationGatewayService: "gw-ns/gw-svc",
			},
		},
		Spec: gwv1.GatewaySpec{
			Listeners: []gwv1.Listener{{Name: "tls", Hostname: &h2, Port: 443, Protocol: gwv1.TLSProtocolType}},
		},
	}
	tn2 := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge2", Namespace: "gw-ns"},
		Status:     v1alpha1.CloudflareTunnelStatus{TunnelID: "tnl-2", TunnelCNAME: "tnl-2.cfargotunnel.com"},
	}
	gwSvc := mkTLSGwSvc("gw-svc", "gw-ns")
	rt := &gwv1a2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1a2.TLSRouteSpec{
			Hostnames: []gwv1.Hostname{"x.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{
				{Name: "gw1", Namespace: ptrNs("gw-ns")}, // first wins
				{Name: "gw2", Namespace: ptrNs("gw-ns")},
			}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(tlsRtScheme(t)).WithObjects(gw1, gw2, tn1, tn2, gwSvc, rt).
		WithStatusSubresource(&gwv1a2.TLSRoute{}, &v1alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &TLSRouteSourceReconciler{Client: c, Scheme: tlsRtScheme(t), Cache: cache}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	// Only the first tunnel-key has a contribution; the second is untouched.
	snap1 := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "gw-ns", Name: "cf-gw-ns-edge"})
	require.Len(t, snap1, 1, "first parent's tunnel attached")
	snap2 := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "gw-ns", Name: "cf-gw-ns-edge2"})
	require.Empty(t, snap2, "second tunnel-targeted parent must NOT be attached")

	// Status: exactly one entry, for the FIRST parent.
	var got gwv1a2.TLSRoute
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: "r"}, &got))
	require.Len(t, got.Status.Parents, 1, "only the first tunnel-targeted parent gets a status entry")
	require.Equal(t, gwv1.ObjectName("gw1"), got.Status.Parents[0].ParentRef.Name)
}

// TestTLSRouteSource_ParentGatewayNotFound_Skips verifies that a Route
// referencing a non-existent Gateway is a silent no-op: findTunnelTargetedParent
// skips the missing parent (its Get fails), nothing else qualifies, and the
// reconcile returns without error, cache write, CR emission, or status touch.
// Mirror of T12's analogous HTTPRoute test.
func TestTLSRouteSource_ParentGatewayNotFound_Skips(t *testing.T) {
	rt := &gwv1a2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1a2.TLSRouteSpec{
			Hostnames: []gwv1.Hostname{"x.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{
				{Name: "missing-gw", Namespace: ptrNs("gw-ns")},
			}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(tlsRtScheme(t)).WithObjects(rt).
		WithStatusSubresource(&gwv1a2.TLSRoute{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &TLSRouteSourceReconciler{Client: c, Scheme: tlsRtScheme(t), Cache: cache}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err, "missing parent must NOT fail the reconcile")

	// No CR emitted.
	var list v1alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &list))
	require.Empty(t, list.Items)

	// No cache write.
	snap := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "gw-ns", Name: "cf-gw-ns-edge"})
	require.Empty(t, snap)

	// No status entries written.
	var got gwv1a2.TLSRoute
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: "r"}, &got))
	require.Empty(t, got.Status.Parents, "no parent qualified — status untouched")
}

// TestTLSRouteSource_GatewayServiceUnresolved_Skips verifies that a Route
// whose parent Gateway carries cloudflare.io/tunnel=true but lacks
// cloudflare.io/gateway-service is a silent no-op for THIS reconciler. The
// Gateway source reconciler is responsible for surfacing
// GatewayServiceUnspecified on the Gateway itself — the TLSRoute reconciler
// just skips the parent. Mirror of T12's analogous HTTPRoute test.
func TestTLSRouteSource_GatewayServiceUnresolved_Skips(t *testing.T) {
	h := gwv1.Hostname("tls.example.com")
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gw", Namespace: "gw-ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:     "true",
				conventions.AnnotationTunnelName: "edge",
				// cloudflare.io/gateway-service deliberately absent.
			},
		},
		Spec: gwv1.GatewaySpec{
			Listeners: []gwv1.Listener{{Name: "tls", Hostname: &h, Port: 443, Protocol: gwv1.TLSProtocolType}},
		},
	}
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v1alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	rt := &gwv1a2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1a2.TLSRouteSpec{
			Hostnames: []gwv1.Hostname{"x.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{
				{Name: "gw", Namespace: ptrNs("gw-ns")},
			}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(tlsRtScheme(t)).WithObjects(gw, tn, rt).
		WithStatusSubresource(&gwv1a2.TLSRoute{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &TLSRouteSourceReconciler{Client: c, Scheme: tlsRtScheme(t), Cache: cache}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err, "gateway-service unresolved must NOT fail the reconcile")

	// No cache contribution — the parent did not qualify.
	snap := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "gw-ns", Name: "cf-gw-ns-edge"})
	require.Empty(t, snap)

	// No DNSRecord CR emitted.
	var list v1alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &list))
	require.Empty(t, list.Items)

	// No status entry on the Route (the Gateway reconciler reports on the Gateway).
	var got gwv1a2.TLSRoute
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: "r"}, &got))
	require.Empty(t, got.Status.Parents)
}

// TestTLSRouteSource_NoListenerHostname_ZeroContribs_AcceptedFalse covers the
// degenerate TLSRoute-specific branch where neither Spec.Hostnames nor the
// parent Gateway's listener hostname yields anything to attach.
//
// Reconciler flow in this case:
//   - hostnames slice is empty after the listener-fallback (gwApex is "").
//   - TranslateTLSRoute returns zero contributions + the always-on
//     ClientSideClientRequired warning.
//   - DNSRecord emission is deferred (gwApex == "" trips the guard).
//   - writeParentStatus stamps Accepted=False/NoListenerHostname (hasContribs
//     is false) AND PartiallyInvalid=True/ClientSideClientRequired (the
//     translator warning is always present for TLSRoute).
//
// No T12 analogue — HTTPRoute has different semantics for empty hostnames.
func TestTLSRouteSource_NoListenerHostname_ZeroContribs_AcceptedFalse(t *testing.T) {
	// Tunnel-targeted parent Gateway with a listener that has NO hostname.
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gw", Namespace: "gw-ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:         "true",
				conventions.AnnotationTunnelName:     "edge",
				conventions.AnnotationGatewayService: "gw-ns/gw-svc",
			},
		},
		Spec: gwv1.GatewaySpec{
			Listeners: []gwv1.Listener{{
				Name: "tls", Port: 443, Protocol: gwv1.TLSProtocolType,
				// Hostname deliberately omitted.
			}},
		},
	}
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v1alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	gwSvc := mkTLSGwSvc("gw-svc", "gw-ns")
	rt := &gwv1a2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1a2.TLSRouteSpec{
			// Hostnames deliberately empty.
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{
				{Name: "gw", Namespace: ptrNs("gw-ns")},
			}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(tlsRtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1a2.TLSRoute{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &TLSRouteSourceReconciler{Client: c, Scheme: tlsRtScheme(t), Cache: cache}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	// Zero contributions emitted (no hostnames to fan out over).
	snap := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "gw-ns", Name: "cf-gw-ns-edge"})
	require.Empty(t, snap, "no hostnames resolved → no contributions")

	// No DNSRecord — emission deferred because gwApex == "".
	var list v1alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &list))
	require.Empty(t, list.Items, "DNSRecord emission deferred when gwApex empty")

	// Status: Accepted=False/NoListenerHostname AND
	// PartiallyInvalid=True/ClientSideClientRequired (translator warning always
	// present for TLSRoute, even when there are zero contributions).
	var got gwv1a2.TLSRoute
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: "r"}, &got))
	require.Len(t, got.Status.Parents, 1)
	require.Equal(t, gwv1.ObjectName("gw"), got.Status.Parents[0].ParentRef.Name)
	require.Equal(t, gwv1.GatewayController("cloudflare.io/tunnel-controller"), got.Status.Parents[0].ControllerName)

	var sawAcceptedFalse, sawClientSide bool
	for _, cond := range got.Status.Parents[0].Conditions {
		if cond.Type == conventions.ConditionTypeAccepted &&
			cond.Status == metav1.ConditionFalse &&
			cond.Reason == conventions.ReasonNoListenerHostname {
			sawAcceptedFalse = true
		}
		if cond.Type == conventions.ConditionTypePartiallyInvalid &&
			cond.Status == metav1.ConditionTrue &&
			cond.Reason == conventions.ReasonClientSideClientRequired {
			sawClientSide = true
		}
	}
	require.True(t, sawAcceptedFalse, "expected Accepted=False/NoListenerHostname")
	require.True(t, sawClientSide, "expected PartiallyInvalid=True/ClientSideClientRequired always stamped")
}

// TestTLSRouteSource_InheritsListenerHostname_WhenSpecEmpty covers the
// TLSRoute-specific Gateway-API fallback at tlsroute_source_controller.go
// lines 162-164: when Spec.Hostnames is empty AND the parent Gateway's
// listener has a hostname, the Route inherits that hostname.
//
// The implementation then emits a DNSRecord unconditionally in the per-
// hostname loop (line 200-204). Because the Route hostname IS the gateway
// apex in this branch, the emitted record is a degenerate self-CNAME
// (<gw-apex> → <gw-apex>). The implementation does NOT special-case this —
// it emits the record anyway. This test pins the actual behavior; downstream
// consumers (the DNS reconciler) are expected to flag the degenerate record,
// not the source reconciler.
//
// No T12 analogue — HTTPRoute does not inherit listener hostnames.
func TestTLSRouteSource_InheritsListenerHostname_WhenSpecEmpty(t *testing.T) {
	// Parent Gateway with a TLS listener bearing a concrete hostname.
	gw := mkTLSParentGw("gw", "gw-ns") // listener hostname == "tls.example.com"
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v1alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	gwSvc := mkTLSGwSvc("gw-svc", "gw-ns")
	rt := &gwv1a2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1a2.TLSRouteSpec{
			// Hostnames deliberately empty — should inherit listener hostname.
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{
				{Name: "gw", Namespace: ptrNs("gw-ns")},
			}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(tlsRtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1a2.TLSRoute{}, &v1alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &TLSRouteSourceReconciler{Client: c, Scheme: tlsRtScheme(t), Cache: cache}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	// Exactly one contribution using the inherited listener hostname.
	snap := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "gw-ns", Name: "cf-gw-ns-edge"})
	require.Len(t, snap, 1, "Spec.Hostnames empty + listener hostname present → 1 inherited contribution")
	require.Equal(t, "tls.example.com", snap[0].Hostname, "contribution uses the inherited listener hostname")
	require.True(t, strings.HasPrefix(snap[0].Service, "tcp://"))

	// Exactly one DNSRecord emitted. Per the implementation at lines 199-204,
	// the per-hostname emit loop runs unconditionally — even though
	// route-hostname == gwApex (degenerate self-CNAME), the reconciler does
	// NOT skip. This test pins that behavior; if the implementation ever
	// gains a self-CNAME guard, this assertion will fail and prompt a review.
	var list v1alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &list))
	require.Len(t, list.Items, 1, "one DNSRecord emitted even though it is a degenerate self-CNAME")
	require.Equal(t, "tls.example.com", list.Items[0].Spec.Name)
	require.NotNil(t, list.Items[0].Spec.Content)
	require.Equal(t, "tls.example.com", *list.Items[0].Spec.Content,
		"degenerate self-CNAME: route hostname inherits listener hostname, chain target IS the listener hostname")
	require.Equal(t, "CNAME", list.Items[0].Spec.Type)

	// Status: Accepted=True/TunnelAttached (one contribution landed) +
	// PartiallyInvalid=True/ClientSideClientRequired (always for TLSRoute).
	var got gwv1a2.TLSRoute
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: "r"}, &got))
	require.Len(t, got.Status.Parents, 1)
	var sawAccepted, sawClientSide bool
	for _, cond := range got.Status.Parents[0].Conditions {
		if cond.Type == conventions.ConditionTypeAccepted &&
			cond.Status == metav1.ConditionTrue &&
			cond.Reason == conventions.ReasonTunnelAttached {
			sawAccepted = true
		}
		if cond.Type == conventions.ConditionTypePartiallyInvalid &&
			cond.Status == metav1.ConditionTrue &&
			cond.Reason == conventions.ReasonClientSideClientRequired {
			sawClientSide = true
		}
	}
	require.True(t, sawAccepted, "expected Accepted=True/TunnelAttached")
	require.True(t, sawClientSide, "expected PartiallyInvalid=True/ClientSideClientRequired")
}
