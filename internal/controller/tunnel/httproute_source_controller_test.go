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
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

// rtScheme builds the scheme needed by the HTTPRoute source tests: core for
// the underlying Gateway service, gateway-api/v1 for HTTPRoute + Gateway, and
// the operator's CRDs for the CloudflareTunnel + CloudflareDNSRecord emits.
func rtScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, gwv1.Install(s))
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

// mkParentGw constructs a tunnel-targeted Gateway with the REQUIRED
// cloudflare.io/gateway-service annotation (no label fallback per design §5).
func mkParentGw(name, ns string) *gwv1.Gateway {
	h := gwv1.Hostname("external.example.com")
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
			Listeners: []gwv1.Listener{{Name: "h", Hostname: &h, Port: 80, Protocol: gwv1.HTTPProtocolType}},
		},
	}
}

// mkGwSvc returns the underlying Service the Gateway annotation points at.
// No labels — discovery is annotation-driven, never label-driven.
func mkGwSvc(name, ns string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
}

func TestHTTPRouteSource_HappyPath(t *testing.T) {
	gw := mkParentGw("gw", "gw-ns")
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v1alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	gwSvc := mkGwSvc("gw-svc", "gw-ns")
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"notes.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{
					Name:      gwv1.ObjectName("gw"),
					Namespace: ptrNs("gw-ns"),
				}},
			},
			Rules: []gwv1.HTTPRouteRule{{}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}, &v1alpha1.CloudflareTunnel{}).Build()

	cache := tunnelsynth.NewCache()
	r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: cache}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	snap := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "gw-ns", Name: "cf-gw-ns-edge"})
	require.Len(t, snap, 1)
	require.Equal(t, "notes.example.com", snap[0].Hostname)
	// The service URL points at the Gateway's underlying Service, port from
	// annotation-resolved port (80 here, from the Service's first port).
	require.Equal(t, "http://gw-svc.gw-ns.svc.cluster.local:80", snap[0].Service)

	// DNSRecord CR emitted: CNAME notes.example.com → external.example.com (chain hop).
	var list v1alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &list))
	require.Len(t, list.Items, 1)
	require.Equal(t, "notes.example.com", list.Items[0].Spec.Name)
	require.NotNil(t, list.Items[0].Spec.Content)
	require.Equal(t, "external.example.com", *list.Items[0].Spec.Content)
	require.Equal(t, "CNAME", list.Items[0].Spec.Type)

	// Parent status: Accepted=True for the tunnel-targeted parent.
	var got gwv1.HTTPRoute
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: "r"}, &got))
	require.Len(t, got.Status.Parents, 1)
	require.Equal(t, gwv1.ObjectName("gw"), got.Status.Parents[0].ParentRef.Name)
	var sawAccepted bool
	for _, cond := range got.Status.Parents[0].Conditions {
		if cond.Type == conventions.ConditionTypeAccepted && cond.Status == metav1.ConditionTrue && cond.Reason == conventions.ReasonTunnelAttached {
			sawAccepted = true
		}
	}
	require.True(t, sawAccepted, "expected Accepted=True/TunnelAttached on the tunnel-targeted parent")
}

func TestHTTPRouteSource_FilterRejected_PartiallyInvalid(t *testing.T) {
	gw := mkParentGw("gw", "gw-ns")
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v1alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	gwSvc := mkGwSvc("gw-svc", "gw-ns")
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames:       []gwv1.Hostname{"x.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}}},
			Rules:           []gwv1.HTTPRouteRule{{Filters: []gwv1.HTTPRouteFilter{{Type: gwv1.HTTPRouteFilterRequestRedirect}}}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}).Build()

	r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: tunnelsynth.NewCache()}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	var got gwv1.HTTPRoute
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: "r"}, &got))
	require.Len(t, got.Status.Parents, 1, "status touches only the tunnel-targeted parent")
	var sawIncompatible bool
	for _, cond := range got.Status.Parents[0].Conditions {
		if cond.Reason == conventions.ReasonIncompatibleFilters {
			sawIncompatible = true
		}
	}
	require.True(t, sawIncompatible)
}

func TestHTTPRouteSource_MultiParent_OnlyTunnelTargetedTouched(t *testing.T) {
	gw := mkParentGw("gw", "gw-ns")
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v1alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	otherGw := &gwv1.Gateway{ // no tunnel annotation
		ObjectMeta: metav1.ObjectMeta{Name: "other-gw", Namespace: "gw-ns"},
		Spec:       gwv1.GatewaySpec{Listeners: []gwv1.Listener{{Port: 80, Protocol: gwv1.HTTPProtocolType}}},
	}
	gwSvc := mkGwSvc("gw-svc", "gw-ns")
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"x.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{
				{Name: "other-gw", Namespace: ptrNs("gw-ns")},
				{Name: "gw", Namespace: ptrNs("gw-ns")},
			}},
			Rules: []gwv1.HTTPRouteRule{{}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, otherGw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}).Build()
	r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: tunnelsynth.NewCache()}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	var got gwv1.HTTPRoute
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: "r"}, &got))
	// Exactly one parent status entry — the tunnel-targeted one.
	require.Len(t, got.Status.Parents, 1)
	require.Equal(t, gwv1.ObjectName("gw"), got.Status.Parents[0].ParentRef.Name)
}

// TestHTTPRouteSource_PreservesOtherParentStatusEntry verifies that when
// another controller has already written a status entry for a NON-tunnel
// parent, our reconcile does NOT clobber it. This is the production-shape
// contract — multi-parent Routes accumulate status entries from each parent's
// owning controller, and we touch only the tunnel-targeted parent's entry.
func TestHTTPRouteSource_PreservesOtherParentStatusEntry(t *testing.T) {
	gw := mkParentGw("gw", "gw-ns")
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v1alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	otherGw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "other-gw", Namespace: "other-ns"},
	}
	gwSvc := mkGwSvc("gw-svc", "gw-ns")
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"x.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{
				{Name: "other-gw", Namespace: ptrNs("other-ns")},
				{Name: "gw", Namespace: ptrNs("gw-ns")},
			}},
			Rules: []gwv1.HTTPRouteRule{{}},
		},
		Status: gwv1.HTTPRouteStatus{
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
	c := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, otherGw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}).Build()

	r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: tunnelsynth.NewCache()}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	var got gwv1.HTTPRoute
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: "r"}, &got))
	// Both entries present: the pre-existing other-controller's entry AND our new entry.
	require.Len(t, got.Status.Parents, 2)

	var foundOther, foundOurs bool
	for _, ps := range got.Status.Parents {
		if ps.ParentRef.Name == "other-gw" {
			foundOther = true
			require.Equal(t, gwv1.GatewayController("other.io/other-controller"), ps.ControllerName)
			require.NotEmpty(t, ps.Conditions)
			require.Equal(t, "OtherReason", ps.Conditions[0].Reason, "other-controller's reason preserved")
		}
		if ps.ParentRef.Name == "gw" {
			foundOurs = true
			require.Equal(t, gwv1.GatewayController("cloudflare.io/tunnel-controller"), ps.ControllerName)
		}
	}
	require.True(t, foundOther, "other-controller parent entry must be preserved")
	require.True(t, foundOurs, "our tunnel-controller parent entry must be present")
}

// TestHTTPRouteSource_DeferredOnEmptyTunnelCNAME verifies the no-CNAME guard:
// when the resolved tunnel CR has no TunnelCNAME populated yet, we still
// write the cache entry (so the tunnel reconciler can compute its ingress
// list) but defer DNSRecord emission. Mirrors the same guard in T10/T11.
func TestHTTPRouteSource_DeferredOnEmptyTunnelCNAME(t *testing.T) {
	gw := mkParentGw("gw", "gw-ns")
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		// TunnelCNAME deliberately empty — tunnel reconciler hasn't run yet.
	}
	gwSvc := mkGwSvc("gw-svc", "gw-ns")
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames:       []gwv1.Hostname{"notes.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}}},
			Rules:           []gwv1.HTTPRouteRule{{}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}).Build()

	cache := tunnelsynth.NewCache()
	r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: cache}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	// Cache entry written for the tunnel reconciler to consume.
	snap := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "gw-ns", Name: "cf-gw-ns-edge"})
	require.Len(t, snap, 1)
	// But no DNSRecord — gwApex via chain still resolves, BUT the design
	// requires deferring DNS emission until the tunnel CR populates its
	// status (so the per-route chain CNAME isn't created before the apex
	// CNAME exists). T11 has the same guard.
	var list v1alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &list))
	require.Empty(t, list.Items, "DNSRecord emission deferred until tunnel CNAME populates")
}

// TestHTTPRouteSource_NoTunnelTargetedParent verifies that a Route whose
// parents are not tunnel-targeted is a no-op: no cache write, no DNSRecord
// emission, no status touched.
func TestHTTPRouteSource_NoTunnelTargetedParent(t *testing.T) {
	otherGw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "other-gw", Namespace: "gw-ns"},
	}
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames:       []gwv1.Hostname{"x.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{{Name: "other-gw", Namespace: ptrNs("gw-ns")}}},
			Rules:           []gwv1.HTTPRouteRule{{}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(otherGw, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}).Build()
	cache := tunnelsynth.NewCache()
	r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: cache}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	var list v1alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &list))
	require.Empty(t, list.Items)

	var got gwv1.HTTPRoute
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: "r"}, &got))
	require.Empty(t, got.Status.Parents, "no tunnel-targeted parent — no status touched")
}

// TestHTTPRouteSource_DeleteSweepsCache verifies that a NotFound Reconcile
// (Route deleted) clears the tracker entry and the cache entry that the
// previous successful Reconcile wrote.
func TestHTTPRouteSource_DeleteSweepsCache(t *testing.T) {
	gw := mkParentGw("gw", "gw-ns")
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v1alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	gwSvc := mkGwSvc("gw-svc", "gw-ns")
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames:       []gwv1.Hostname{"notes.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}}},
			Rules:           []gwv1.HTTPRouteRule{{}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}).Build()
	cache := tunnelsynth.NewCache()
	r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: cache}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)
	tk := tunnelsynth.TunnelKey{Namespace: "gw-ns", Name: "cf-gw-ns-edge"}
	require.Len(t, cache.Snapshot(tk), 1)

	// Delete the route, reconcile again.
	require.NoError(t, c.Delete(context.Background(), rt))
	_, err = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)
	require.Empty(t, cache.Snapshot(tk), "cache cleared when Route deleted")
}

// ptrNs returns a pointer to a gwv1.Namespace for one-line literal use in
// ParentReference fixtures.
func ptrNs(s string) *gwv1.Namespace {
	n := gwv1.Namespace(s)
	return &n
}
