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
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	reconcilelib "github.com/jacaudi/cloudflare-operator/internal/reconcile"
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
	require.NoError(t, v2alpha1.AddToScheme(s))
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
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v2alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
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
	base := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}, &v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

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

	// DNSRecord CR emitted: CNAME notes.example.com → tunnel CNAME (chain hop).
	// Bug D behavior change: concrete-listener no-override path chains directly
	// to the tunnel CNAME (not the listener hostname).
	var list v2alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &list))
	require.Len(t, list.Items, 1)
	require.Equal(t, "notes.example.com", list.Items[0].Spec.Name)
	require.NotNil(t, list.Items[0].Spec.Content)
	require.Equal(t, "tnl-1.cfargotunnel.com", *list.Items[0].Spec.Content)
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
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v2alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
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
	base := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

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
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v2alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
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
	base := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, otherGw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
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
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v2alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
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
	base := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, otherGw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

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
	tn := &v2alpha1.CloudflareTunnel{
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
	base := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	cache := tunnelsynth.NewCache()
	r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: cache}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	// Cache entry written for the tunnel reconciler to consume.
	snap := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "gw-ns", Name: "cf-gw-ns-edge"})
	require.Len(t, snap, 1)
	// But no DNSRecord — chainContent resolves, BUT the design requires
	// deferring DNS emission until the tunnel CR populates its status (so the
	// per-route chain CNAME isn't created before the apex CNAME exists).
	// T11 has the same guard.
	var list v2alpha1.CloudflareDNSRecordList
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
	base := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(otherGw, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: cache}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	var list v2alpha1.CloudflareDNSRecordList
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
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v2alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
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
	base := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
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

// TestHTTPRouteSource_MultipleHostnames_EmitsPerHostname verifies that a
// Route with N hostnames produces N DNSRecord CRs and N cache contributions,
// each pinned to the same gateway-apex chain hop. Per design §4.2 hostnames
// fan out per-Route, not per-rule.
func TestHTTPRouteSource_MultipleHostnames_EmitsPerHostname(t *testing.T) {
	gw := mkParentGw("gw", "gw-ns")
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v2alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	gwSvc := mkGwSvc("gw-svc", "gw-ns")
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{
				"a.example.com",
				"b.example.com",
				"c.example.com",
			},
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}},
			},
			Rules: []gwv1.HTTPRouteRule{{}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	cache := tunnelsynth.NewCache()
	r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: cache}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	// Cache: one contribution per hostname.
	snap := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "gw-ns", Name: "cf-gw-ns-edge"})
	require.Len(t, snap, 3, "expected one cache contribution per hostname")
	gotHostnames := map[string]bool{}
	for _, c := range snap {
		gotHostnames[c.Hostname] = true
	}
	require.True(t, gotHostnames["a.example.com"])
	require.True(t, gotHostnames["b.example.com"])
	require.True(t, gotHostnames["c.example.com"])

	// DNSRecord CRs: one CNAME per hostname, all chaining to the tunnel CNAME.
	// Bug D behavior change: concrete-listener no-override path chains directly
	// to the tunnel CNAME (not the listener hostname).
	var list v2alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &list))
	require.Len(t, list.Items, 3, "expected one DNSRecord CR per hostname")
	gotNames := map[string]string{}
	for _, dr := range list.Items {
		require.Equal(t, "CNAME", dr.Spec.Type)
		require.NotNil(t, dr.Spec.Content)
		require.Equal(t, "tnl-1.cfargotunnel.com", *dr.Spec.Content, "every record chains to the tunnel CNAME directly")
		gotNames[dr.Spec.Name] = *dr.Spec.Content
	}
	require.Contains(t, gotNames, "a.example.com")
	require.Contains(t, gotNames, "b.example.com")
	require.Contains(t, gotNames, "c.example.com")
}

// TestHTTPRouteSource_TwoTunnelTargetedParents_FirstWins verifies the design
// §4.2 / Q3 lock: when a Route lists multiple tunnel-targeted parents (an
// abuse case the design is silent on), only the FIRST parent in parentRefs
// is honored. Subsequent tunnel-targeted parents are NOT attached — neither
// cache contributions nor a parent status entry.
func TestHTTPRouteSource_TwoTunnelTargetedParents_FirstWins(t *testing.T) {
	// First parent: tunnel "edge" → CR "cf-gw-ns-edge".
	gw1 := mkParentGw("gw1", "gw-ns")
	tn1 := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v2alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
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
			Listeners: []gwv1.Listener{{Name: "h", Hostname: &h2, Port: 80, Protocol: gwv1.HTTPProtocolType}},
		},
	}
	tn2 := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge2", Namespace: "gw-ns"},
		Status:     v2alpha1.CloudflareTunnelStatus{TunnelID: "tnl-2", TunnelCNAME: "tnl-2.cfargotunnel.com"},
	}
	gwSvc := mkGwSvc("gw-svc", "gw-ns")
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"x.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{
				{Name: "gw1", Namespace: ptrNs("gw-ns")}, // first wins
				{Name: "gw2", Namespace: ptrNs("gw-ns")},
			}},
			Rules: []gwv1.HTTPRouteRule{{}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw1, gw2, tn1, tn2, gwSvc, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: cache}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	// Only the first tunnel-key has a contribution; the second is untouched.
	snap1 := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "gw-ns", Name: "cf-gw-ns-edge"})
	require.Len(t, snap1, 1, "first parent's tunnel attached")
	snap2 := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "gw-ns", Name: "cf-gw-ns-edge2"})
	require.Empty(t, snap2, "second tunnel-targeted parent must NOT be attached")

	// Status: exactly one entry, for the FIRST parent.
	var got gwv1.HTTPRoute
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: "r"}, &got))
	require.Len(t, got.Status.Parents, 1, "only the first tunnel-targeted parent gets a status entry")
	require.Equal(t, gwv1.ObjectName("gw1"), got.Status.Parents[0].ParentRef.Name)
}

// TestHTTPRouteSource_ParentGatewayNotFound_Skips verifies that a Route
// referencing a non-existent Gateway is a silent no-op: findTunnelTargetedParent
// skips the missing parent (its Get fails), nothing else qualifies, and the
// reconcile returns without error, cache write, CR emission, or status touch.
func TestHTTPRouteSource_ParentGatewayNotFound_Skips(t *testing.T) {
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"x.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{
				{Name: "missing-gw", Namespace: ptrNs("gw-ns")},
			}},
			Rules: []gwv1.HTTPRouteRule{{}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: cache}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err, "missing parent must NOT fail the reconcile")

	// No CR emitted.
	var list v2alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &list))
	require.Empty(t, list.Items)

	// No status entries written.
	var got gwv1.HTTPRoute
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: "r"}, &got))
	require.Empty(t, got.Status.Parents, "no parent qualified — status untouched")
}

// TestHTTPRouteSource_GatewayServiceUnresolved_Skips verifies that a Route
// whose parent Gateway carries cloudflare.io/tunnel=true but lacks
// cloudflare.io/gateway-service is a silent no-op for THIS reconciler. The
// Gateway source reconciler is responsible for surfacing
// GatewayServiceUnspecified on the Gateway itself — the HTTPRoute reconciler
// just skips the parent.
func TestHTTPRouteSource_GatewayServiceUnresolved_Skips(t *testing.T) {
	h := gwv1.Hostname("external.example.com")
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
			Listeners: []gwv1.Listener{{Name: "h", Hostname: &h, Port: 80, Protocol: gwv1.HTTPProtocolType}},
		},
	}
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v2alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"x.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{
				{Name: "gw", Namespace: ptrNs("gw-ns")},
			}},
			Rules: []gwv1.HTTPRouteRule{{}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, tn, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	cache := tunnelsynth.NewCache()
	r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: cache}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err, "gateway-service unresolved must NOT fail the reconcile")

	// No cache contribution — the parent did not qualify.
	snap := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: "gw-ns", Name: "cf-gw-ns-edge"})
	require.Empty(t, snap)

	// No DNSRecord CR emitted.
	var list v2alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &list))
	require.Empty(t, list.Items)

	// No status entry on the Route (the Gateway reconciler reports on the Gateway).
	var got gwv1.HTTPRoute
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: "r"}, &got))
	require.Empty(t, got.Status.Parents)
}

// TestHTTPRouteSource_InheritsAdoptFromGateway verifies Design E1 §5: when the
// HTTPRoute omits cloudflare.io/adopt, the value falls through from the parent
// Gateway's annotation. The emitted CloudflareDNSRecord must carry Spec.Adopt
// reflecting the Gateway's setting.
func TestHTTPRouteSource_InheritsAdoptFromGateway(t *testing.T) {
	// Gateway carries adopt=true; HTTPRoute has no adopt annotation.
	gw := mkParentGw("gw", "gw-ns")
	gw.Annotations[conventions.AnnotationAdopt] = "true"

	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v2alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	gwSvc := mkGwSvc("gw-svc", "gw-ns")
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "r",
			Namespace: "app",
			// No adopt annotation — must inherit from Gateway.
		},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames:       []gwv1.Hostname{"notes.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}}},
			Rules:           []gwv1.HTTPRouteRule{{}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}, &v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: tunnelsynth.NewCache()}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	// Re-Get the emitted DNSRecord and assert Spec.Adopt reflects the Gateway's annotation.
	drName := emittedDNSRecordName("r", "notes.example.com")
	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: drName}, &got))
	require.True(t, got.Spec.Adopt, "Spec.Adopt must be true (inherited from parent Gateway)")
}

// TestHTTPRouteSource_RouteOverridesGatewayAdopt verifies Design E1 §5: when
// the HTTPRoute explicitly sets cloudflare.io/adopt, its value wins over the
// parent Gateway's value. Route-side annotations have priority.
func TestHTTPRouteSource_RouteOverridesGatewayAdopt(t *testing.T) {
	// Gateway carries adopt=true; HTTPRoute overrides to adopt=false.
	gw := mkParentGw("gw", "gw-ns")
	gw.Annotations[conventions.AnnotationAdopt] = "true"

	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v2alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	gwSvc := mkGwSvc("gw-svc", "gw-ns")
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "r",
			Namespace: "app",
			Annotations: map[string]string{
				conventions.AnnotationAdopt: "false",
			},
		},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames:       []gwv1.Hostname{"notes.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}}},
			Rules:           []gwv1.HTTPRouteRule{{}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}, &v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: tunnelsynth.NewCache()}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	// Re-Get the emitted DNSRecord and assert the route's own annotation wins.
	drName := emittedDNSRecordName("r", "notes.example.com")
	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: drName}, &got))
	require.False(t, got.Spec.Adopt, "Spec.Adopt must be false (HTTPRoute annotation overrides Gateway)")
}

// TestHTTPRouteSource_NoHostnameReason verifies MIN-16: when an HTTPRoute has
// Spec.Hostnames == nil (empty) but carries a valid rule (no bad filters), the
// reconciler must report Accepted=False with Reason=NoListenerHostname — NOT
// IncompatibleFilters / "all rules dropped during translation", which is
// factually incorrect (nothing was dropped; the route simply has no hostnames
// to bind). This test is RED against current code: the existing fallback block
// (~line 355-362 of httproute_source_controller.go) fires with
// Reason=IncompatibleFilters and message "all rules dropped during translation".
func TestHTTPRouteSource_NoHostnameReason(t *testing.T) {
	gw := mkParentGw("gw", "gw-ns")
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v2alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	gwSvc := mkGwSvc("gw-svc", "gw-ns")
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1.HTTPRouteSpec{
			// Spec.Hostnames is intentionally empty (nil) — the triggering condition
			// for MIN-16. The rule itself is clean (no filters, one simple match)
			// so zero contribs come from missing hostnames, not bad filters.
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{
					Name:      gwv1.ObjectName("gw"),
					Namespace: ptrNs("gw-ns"),
				}},
			},
			Rules: []gwv1.HTTPRouteRule{{}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}, &v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: tunnelsynth.NewCache()}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	// Mirror the happy-path pattern (lines 124-135) for locating the Accepted
	// condition on the tunnel-targeted parent.
	var got gwv1.HTTPRoute
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "app", Name: "r"}, &got))
	require.Len(t, got.Status.Parents, 1)
	require.Equal(t, gwv1.ObjectName("gw"), got.Status.Parents[0].ParentRef.Name)

	var foundAccepted bool
	var acceptedCond metav1.Condition
	for _, cond := range got.Status.Parents[0].Conditions {
		if cond.Type == conventions.ConditionTypeAccepted {
			foundAccepted = true
			acceptedCond = cond
		}
	}
	require.True(t, foundAccepted, "Accepted condition must be present on the tunnel-targeted parent")
	require.Equal(t, metav1.ConditionFalse, acceptedCond.Status,
		"Accepted must be False when HTTPRoute has no hostnames")
	// Phase B will set this to ReasonNoListenerHostname; current code sets
	// ReasonIncompatibleFilters — this assertion is the RED line.
	require.Equal(t, conventions.ReasonNoListenerHostname, acceptedCond.Reason,
		"Reason must be NoListenerHostname, not IncompatibleFilters, for empty Spec.Hostnames")
	// The message must not mislead operators into thinking rules were dropped.
	require.NotContains(t, acceptedCond.Message, "all rules dropped",
		"message must not claim rules were dropped when the issue is missing hostnames")
}

// TestHTTPRouteChain_ValidOverride asserts that when the parent Gateway carries a
// valid cloudflare.io/gateway-apex annotation, the chain DNSRecord uses that
// override host as its CNAME content — even when all listeners are wildcards.
// This is the Bug D fix: a wildcard-only Gateway with a gateway-apex override
// must NOT emit a wildcard CNAME content (which would cause Cloudflare 9007).
func TestHTTPRouteChain_ValidOverride(t *testing.T) {
	// Gateway with a wildcard listener AND a valid gateway-apex annotation.
	wild := gwv1.Hostname("*.example.com")
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gw", Namespace: "gw-ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:         "true",
				conventions.AnnotationTunnelName:     "edge",
				conventions.AnnotationGatewayService: "gw-ns/gw-svc",
				conventions.AnnotationGatewayApex:    "external.example.com",
			},
		},
		Spec: gwv1.GatewaySpec{
			Listeners: []gwv1.Listener{{
				Name: "http", Hostname: &wild, Port: 80, Protocol: gwv1.HTTPProtocolType,
			}},
		},
	}
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v2alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	gwSvc := mkGwSvc("gw-svc", "gw-ns")
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"jellyfin.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}},
			},
			Rules: []gwv1.HTTPRouteRule{{}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}, &v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	rec := record.NewFakeRecorder(8)
	r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: tunnelsynth.NewCache(), Recorder: rec}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	var list v2alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &list))
	require.Len(t, list.Items, 1, "one chain DNSRecord must be emitted")
	require.NotNil(t, list.Items[0].Spec.Content)
	require.Equal(t, "external.example.com", *list.Items[0].Spec.Content,
		"chain content must be the gateway-apex override, not a wildcard")
	require.Equal(t, "CNAME", list.Items[0].Spec.Type)
}

// TestHTTPRouteChain_NoOverride_ConcreteListener_ChainsToTunnelCNAME asserts
// that when the parent Gateway has a concrete (non-wildcard) listener and no
// gateway-apex annotation, the chain DNSRecord's content is the tunnel CNAME —
// not the listener hostname. This is the intended behavior change in Bug D:
// concrete-listener chain target is now the tunnel CNAME (direct), not a
// double-hop via the listener hostname.
func TestHTTPRouteChain_NoOverride_ConcreteListener_ChainsToTunnelCNAME(t *testing.T) {
	concrete := gwv1.Hostname("app.example.com")
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gw", Namespace: "gw-ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:         "true",
				conventions.AnnotationTunnelName:     "edge",
				conventions.AnnotationGatewayService: "gw-ns/gw-svc",
				// No gateway-apex annotation.
			},
		},
		Spec: gwv1.GatewaySpec{
			Listeners: []gwv1.Listener{{
				Name: "http", Hostname: &concrete, Port: 80, Protocol: gwv1.HTTPProtocolType,
			}},
		},
	}
	const tunnelCNAME = "tnl-2.cfargotunnel.com"
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v2alpha1.CloudflareTunnelStatus{TunnelID: "tnl-2", TunnelCNAME: tunnelCNAME},
	}
	gwSvc := mkGwSvc("gw-svc", "gw-ns")
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"app.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}},
			},
			Rules: []gwv1.HTTPRouteRule{{}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}, &v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: tunnelsynth.NewCache()}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err)

	var list v2alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &list))
	require.Len(t, list.Items, 1, "one chain DNSRecord must be emitted")
	require.NotNil(t, list.Items[0].Spec.Content)
	require.Equal(t, tunnelCNAME, *list.Items[0].Spec.Content,
		"concrete-listener no-override: chain content must be the tunnel CNAME (direct), not the listener hostname")
}

// TestHTTPRouteChain_WildcardOnly_NoOverride_BlockedNoEmit asserts that when
// the parent Gateway has ONLY wildcard listeners and no gateway-apex annotation,
// the route controller emits NO chain DNSRecord, fires a Warning event with
// reason GatewayApexRequired, and returns a bounded RequeueAfter (no hot-loop).
// This is the core Bug D fix.
func TestHTTPRouteChain_WildcardOnly_NoOverride_BlockedNoEmit(t *testing.T) {
	wild := gwv1.Hostname("*.example.com")
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gw", Namespace: "gw-ns",
			Annotations: map[string]string{
				conventions.AnnotationTunnel:         "true",
				conventions.AnnotationTunnelName:     "edge",
				conventions.AnnotationGatewayService: "gw-ns/gw-svc",
				// No gateway-apex annotation — wildcard-only, blocked.
			},
		},
		Spec: gwv1.GatewaySpec{
			Listeners: []gwv1.Listener{{
				Name: "http", Hostname: &wild, Port: 80, Protocol: gwv1.HTTPProtocolType,
			}},
		},
	}
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
		Status:     v2alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	gwSvc := mkGwSvc("gw-svc", "gw-ns")
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"jellyfin.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}},
			},
			Rules: []gwv1.HTTPRouteRule{{}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}, &v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	rec := record.NewFakeRecorder(8)
	r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: tunnelsynth.NewCache(), Recorder: rec}
	result, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
	require.NoError(t, err, "blocked path must not return an error (no hot-loop retry)")

	// No chain DNSRecord emitted.
	var list v2alpha1.CloudflareDNSRecordList
	require.NoError(t, c.List(context.Background(), &list))
	require.Empty(t, list.Items, "no chain DNSRecord must be emitted for wildcard-only gateway without apex annotation")

	// RequeueAfter must be set (bounded, non-zero) to avoid infinite hot-loop.
	require.Greater(t, result.RequeueAfter, time.Duration(0),
		"blocked path must set a positive RequeueAfter to avoid hot-loop")

	// A Warning event with reason GatewayApexRequired must be recorded.
	select {
	case ev := <-rec.Events:
		require.Contains(t, ev, conventions.ReasonGatewayApexRequired,
			"event must carry GatewayApexRequired reason")
	default:
		t.Fatal("expected a Warning event with reason GatewayApexRequired")
	}
}

// TestDogfooding_EmittedChainCRsNeverWildcard is a consolidated invariant guard
// for the §6 dogfooding-validity invariant: every chain CloudflareDNSRecord
// emitted by the HTTPRoute source controller must have a non-nil, non-empty,
// non-wildcard Spec.Content. A wildcard CNAME target causes Cloudflare error
// 9007 on the zone controller's push; this test ensures no such CR is ever
// dogfooded into the operator's own reconciliation loop.
//
// Three scenarios are verified:
//  1. Valid gateway-apex override with wildcard listener → override host used as
//     chain content (non-wildcard).
//  2. Concrete listener + no override → tunnel CNAME used as chain content
//     (non-wildcard).
//  3. Wildcard-only listeners + no override → ZERO chain CRs emitted (nothing
//     invalid is dogfooded).
//
// Non-vacuity: the test fetches emitted CRs and would FAIL if any chain CR had
// wildcard or empty content (the strings.HasPrefix("*") and empty-string checks
// are real assertions), and would FAIL if the blocked scenario emitted even one
// CR (require.Empty on the list). The Task-2/3 tests prove the behavior is
// correct on HEAD; this test is the standing invariant guard.
func TestDogfooding_EmittedChainCRsNeverWildcard(t *testing.T) {
	t.Run("override_wildcard_listener", func(t *testing.T) {
		// Scenario 1: Gateway has a wildcard listener AND a valid gateway-apex
		// override annotation. The chain CR content must be the override host
		// (non-wildcard), not the listener wildcard pattern.
		wild := gwv1.Hostname("*.example.com")
		gw := &gwv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name: "gw", Namespace: "gw-ns",
				Annotations: map[string]string{
					conventions.AnnotationTunnel:         "true",
					conventions.AnnotationTunnelName:     "edge",
					conventions.AnnotationGatewayService: "gw-ns/gw-svc",
					conventions.AnnotationGatewayApex:    "external.example.com",
				},
			},
			Spec: gwv1.GatewaySpec{
				Listeners: []gwv1.Listener{{
					Name: "http", Hostname: &wild, Port: 80, Protocol: gwv1.HTTPProtocolType,
				}},
			},
		}
		tn := &v2alpha1.CloudflareTunnel{
			ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
			Status:     v2alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
		}
		gwSvc := mkGwSvc("gw-svc", "gw-ns")
		rt := &gwv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
			Spec: gwv1.HTTPRouteSpec{
				Hostnames: []gwv1.Hostname{"jellyfin.example.com"},
				CommonRouteSpec: gwv1.CommonRouteSpec{
					ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}},
				},
				Rules: []gwv1.HTTPRouteRule{{}},
			},
		}
		base := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
			WithStatusSubresource(&gwv1.HTTPRoute{}, &v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
		c := reconcilelib.SSATranslatingClient(t, base)

		rec := record.NewFakeRecorder(8)
		r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: tunnelsynth.NewCache(), Recorder: rec}
		_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
		require.NoError(t, err)

		var list v2alpha1.CloudflareDNSRecordList
		require.NoError(t, c.List(context.Background(), &list))
		require.NotEmpty(t, list.Items, "override case must emit at least one chain CR")

		// Invariant: every emitted chain CR has non-nil, non-empty, non-wildcard content.
		for i, cr := range list.Items {
			require.NotNil(t, cr.Spec.Content, "chain CR[%d] Spec.Content must not be nil", i)
			require.NotEmpty(t, *cr.Spec.Content, "chain CR[%d] Spec.Content must not be empty", i)
			require.False(t, strings.HasPrefix(*cr.Spec.Content, "*"),
				"chain CR[%d] Spec.Content %q must not be a wildcard (would cause Cloudflare error 9007)", i, *cr.Spec.Content)
		}
	})

	t.Run("concrete_listener_no_override", func(t *testing.T) {
		// Scenario 2: Gateway has a concrete (non-wildcard) listener and no
		// gateway-apex annotation. The chain CR content must be the tunnel CNAME
		// (non-wildcard), not the listener hostname acting as a double-hop.
		concrete := gwv1.Hostname("app.example.com")
		gw := &gwv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name: "gw", Namespace: "gw-ns",
				Annotations: map[string]string{
					conventions.AnnotationTunnel:         "true",
					conventions.AnnotationTunnelName:     "edge",
					conventions.AnnotationGatewayService: "gw-ns/gw-svc",
					// No gateway-apex annotation.
				},
			},
			Spec: gwv1.GatewaySpec{
				Listeners: []gwv1.Listener{{
					Name: "http", Hostname: &concrete, Port: 80, Protocol: gwv1.HTTPProtocolType,
				}},
			},
		}
		const tunnelCNAME = "tnl-2.cfargotunnel.com"
		tn := &v2alpha1.CloudflareTunnel{
			ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
			Status:     v2alpha1.CloudflareTunnelStatus{TunnelID: "tnl-2", TunnelCNAME: tunnelCNAME},
		}
		gwSvc := mkGwSvc("gw-svc", "gw-ns")
		rt := &gwv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
			Spec: gwv1.HTTPRouteSpec{
				Hostnames: []gwv1.Hostname{"app.example.com"},
				CommonRouteSpec: gwv1.CommonRouteSpec{
					ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}},
				},
				Rules: []gwv1.HTTPRouteRule{{}},
			},
		}
		base := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
			WithStatusSubresource(&gwv1.HTTPRoute{}, &v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
		c := reconcilelib.SSATranslatingClient(t, base)

		r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: tunnelsynth.NewCache()}
		_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
		require.NoError(t, err)

		var list v2alpha1.CloudflareDNSRecordList
		require.NoError(t, c.List(context.Background(), &list))
		require.NotEmpty(t, list.Items, "concrete-listener no-override case must emit at least one chain CR")

		// Invariant: every emitted chain CR has non-nil, non-empty, non-wildcard content.
		for i, cr := range list.Items {
			require.NotNil(t, cr.Spec.Content, "chain CR[%d] Spec.Content must not be nil", i)
			require.NotEmpty(t, *cr.Spec.Content, "chain CR[%d] Spec.Content must not be empty", i)
			require.False(t, strings.HasPrefix(*cr.Spec.Content, "*"),
				"chain CR[%d] Spec.Content %q must not be a wildcard (would cause Cloudflare error 9007)", i, *cr.Spec.Content)
		}
	})

	t.Run("wildcard_only_no_override_blocked", func(t *testing.T) {
		// Scenario 3: Gateway has ONLY wildcard listeners and no gateway-apex
		// annotation. The route is blocked — ZERO chain CRs must be emitted.
		// Nothing invalid is dogfooded into the zone reconciler.
		wild := gwv1.Hostname("*.example.com")
		gw := &gwv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name: "gw", Namespace: "gw-ns",
				Annotations: map[string]string{
					conventions.AnnotationTunnel:         "true",
					conventions.AnnotationTunnelName:     "edge",
					conventions.AnnotationGatewayService: "gw-ns/gw-svc",
					// No gateway-apex annotation — wildcard-only, blocked.
				},
			},
			Spec: gwv1.GatewaySpec{
				Listeners: []gwv1.Listener{{
					Name: "http", Hostname: &wild, Port: 80, Protocol: gwv1.HTTPProtocolType,
				}},
			},
		}
		tn := &v2alpha1.CloudflareTunnel{
			ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: "gw-ns"},
			Status:     v2alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
		}
		gwSvc := mkGwSvc("gw-svc", "gw-ns")
		rt := &gwv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
			Spec: gwv1.HTTPRouteSpec{
				Hostnames: []gwv1.Hostname{"jellyfin.example.com"},
				CommonRouteSpec: gwv1.CommonRouteSpec{
					ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}},
				},
				Rules: []gwv1.HTTPRouteRule{{}},
			},
		}
		base := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
			WithStatusSubresource(&gwv1.HTTPRoute{}, &v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
		c := reconcilelib.SSATranslatingClient(t, base)

		rec := record.NewFakeRecorder(8)
		r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: tunnelsynth.NewCache(), Recorder: rec}
		_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "app", Name: "r"}})
		require.NoError(t, err, "blocked path must not return an error")

		// Invariant: ZERO chain CRs emitted — nothing invalid is dogfooded.
		var list v2alpha1.CloudflareDNSRecordList
		require.NoError(t, c.List(context.Background(), &list))
		require.Empty(t, list.Items,
			"wildcard-only gateway without apex annotation must emit ZERO chain DNSRecord CRs — "+
				"a non-empty list would mean a wildcard CNAME target was dogfooded to the zone reconciler")
	})
}

// mkTunnelGwWithListeners builds a tunnel-targeted Gateway whose listeners are
// caller-supplied. Mirrors mkParentGw's annotation block (tunnel + tunnel-name
// + gateway-service) so findTunnelTargetedParentRef qualifies the parent;
// listener shape is the variable under test.
func mkTunnelGwWithListeners(name, ns string, listeners []gwv1.Listener, extraAnns map[string]string) *gwv1.Gateway {
	ann := map[string]string{
		conventions.AnnotationTunnel:         "true",
		conventions.AnnotationTunnelName:     "edge",
		conventions.AnnotationGatewayService: ns + "/gw-svc",
	}
	for k, v := range extraAnns {
		ann[k] = v
	}
	return &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Annotations: ann},
		Spec:       gwv1.GatewaySpec{Listeners: listeners},
	}
}

func hostnamePtr(s string) *gwv1.Hostname {
	h := gwv1.Hostname(s)
	return &h
}

func sectionNamePtr(s string) *gwv1.SectionName {
	n := gwv1.SectionName(s)
	return &n
}

// reconcileHTTPRouteAndGetContrib runs the reconciler with the given fixture
// objects and returns the single IngressContribution written to the cache.
// Tests asserting Service / NoTLSVerify / OriginServerName use this helper to
// keep the per-test boilerplate small.
func reconcileHTTPRouteAndGetContrib(t *testing.T, gw *gwv1.Gateway, rt *gwv1.HTTPRoute) tunnelsynth.ContributionWithSource {
	t.Helper()
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "cf-gw-ns-edge", Namespace: gw.Namespace},
		Status:     v2alpha1.CloudflareTunnelStatus{TunnelID: "tnl-1", TunnelCNAME: "tnl-1.cfargotunnel.com"},
	}
	gwSvc := mkGwSvc("gw-svc", gw.Namespace)
	base := fake.NewClientBuilder().WithScheme(rtScheme(t)).WithObjects(gw, tn, gwSvc, rt).
		WithStatusSubresource(&gwv1.HTTPRoute{}, &v2alpha1.CloudflareTunnel{}, &v2alpha1.CloudflareDNSRecord{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	cache := tunnelsynth.NewCache()
	r := &HTTPRouteSourceReconciler{Client: c, Scheme: rtScheme(t), Cache: cache}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: rt.Namespace, Name: rt.Name}})
	require.NoError(t, err)

	snap := cache.Snapshot(tunnelsynth.TunnelKey{Namespace: gw.Namespace, Name: "cf-gw-ns-edge"})
	require.Len(t, snap, 1, "expected exactly one contribution")
	return snap[0]
}

// T1.1 — HTTPRoute pins a parentRef with SectionName pointing at the HTTPS
// listener; the contribution Service URL must use the https:// scheme.
func TestHTTPRouteSource_SchemeFromSectionName_HTTPS(t *testing.T) {
	// Underlying Service exposes both 80 and 443 so resolveGatewayService can
	// match the listener port. The Gateway annotation cf-gateway-service points
	// at it; port selection lives in resolveGatewayService.
	gw := mkTunnelGwWithListeners("gw", "gw-ns", []gwv1.Listener{
		{Name: "http", Hostname: hostnamePtr("notes.example.com"), Port: 80, Protocol: gwv1.HTTPProtocolType},
		{Name: "https", Hostname: hostnamePtr("notes.example.com"), Port: 443, Protocol: gwv1.HTTPSProtocolType},
	}, nil)
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"notes.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{{
				Name:        "gw",
				Namespace:   ptrNs("gw-ns"),
				SectionName: sectionNamePtr("https"),
			}}},
			Rules: []gwv1.HTTPRouteRule{{}},
		},
	}
	got := reconcileHTTPRouteAndGetContrib(t, gw, rt)
	require.True(t, strings.HasPrefix(got.Service, "https://"),
		"sectionName-pinned HTTPS listener must yield https:// service, got %q", got.Service)
}

// T1.2 — Mixed HTTP+HTTPS listeners on the parent, NO sectionName, route
// hostname matches both. HTTPS must win the preference.
func TestHTTPRouteSource_SchemeMixedListeners_HTTPSWins(t *testing.T) {
	gw := mkTunnelGwWithListeners("gw", "gw-ns", []gwv1.Listener{
		{Name: "http", Hostname: hostnamePtr("notes.example.com"), Port: 80, Protocol: gwv1.HTTPProtocolType},
		{Name: "https", Hostname: hostnamePtr("notes.example.com"), Port: 443, Protocol: gwv1.HTTPSProtocolType},
	}, nil)
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"notes.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{{
				Name: "gw", Namespace: ptrNs("gw-ns"),
			}}},
			Rules: []gwv1.HTTPRouteRule{{}},
		},
	}
	got := reconcileHTTPRouteAndGetContrib(t, gw, rt)
	require.True(t, strings.HasPrefix(got.Service, "https://"),
		"HTTPS listener must win over sibling HTTP on the same hostname, got %q", got.Service)
}

// T1.3 — Only an HTTP listener matches the route hostname; scheme must be http.
func TestHTTPRouteSource_SchemeHTTPOnly(t *testing.T) {
	gw := mkTunnelGwWithListeners("gw", "gw-ns", []gwv1.Listener{
		{Name: "http", Hostname: hostnamePtr("notes.example.com"), Port: 80, Protocol: gwv1.HTTPProtocolType},
	}, nil)
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames:       []gwv1.Hostname{"notes.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}}},
			Rules:           []gwv1.HTTPRouteRule{{}},
		},
	}
	got := reconcileHTTPRouteAndGetContrib(t, gw, rt)
	require.True(t, strings.HasPrefix(got.Service, "http://") && !strings.HasPrefix(got.Service, "https://"),
		"HTTP-only listener must yield http:// service, got %q", got.Service)
}

// T1.4 — Route's cloudflare.io/scheme=http overrides an HTTPS listener.
func TestHTTPRouteSource_SchemeRouteAnnotationOverridesHTTPS(t *testing.T) {
	gw := mkTunnelGwWithListeners("gw", "gw-ns", []gwv1.Listener{
		{Name: "https", Hostname: hostnamePtr("notes.example.com"), Port: 443, Protocol: gwv1.HTTPSProtocolType},
	}, nil)
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name: "r", Namespace: "app",
			Annotations: map[string]string{conventions.AnnotationScheme: "http"},
		},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames:       []gwv1.Hostname{"notes.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}}},
			Rules:           []gwv1.HTTPRouteRule{{}},
		},
	}
	got := reconcileHTTPRouteAndGetContrib(t, gw, rt)
	require.True(t, strings.HasPrefix(got.Service, "http://") && !strings.HasPrefix(got.Service, "https://"),
		"route cloudflare.io/scheme=http must override the HTTPS listener, got %q", got.Service)
}

// T1.5 — Gateway-level cloudflare.io/scheme=https, only HTTP listener present.
// The override on the Gateway must inherit into the route's effective scheme.
func TestHTTPRouteSource_SchemeGatewayAnnotationOverridesHTTP(t *testing.T) {
	gw := mkTunnelGwWithListeners("gw", "gw-ns", []gwv1.Listener{
		{Name: "http", Hostname: hostnamePtr("notes.example.com"), Port: 80, Protocol: gwv1.HTTPProtocolType},
	}, map[string]string{conventions.AnnotationScheme: "https"})
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames:       []gwv1.Hostname{"notes.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}}},
			Rules:           []gwv1.HTTPRouteRule{{}},
		},
	}
	got := reconcileHTTPRouteAndGetContrib(t, gw, rt)
	require.True(t, strings.HasPrefix(got.Service, "https://"),
		"Gateway cloudflare.io/scheme=https must override the HTTP-only listener, got %q", got.Service)
}

// T1.6 — cloudflare.io/no-tls-verify=true on the route must surface as
// NoTLSVerify=ptr(true) on the contribution (defaults flow through to
// TranslateHTTPRoute via defaultsFromAnnotations).
func TestHTTPRouteSource_NoTLSVerifyFromRoute(t *testing.T) {
	gw := mkTunnelGwWithListeners("gw", "gw-ns", []gwv1.Listener{
		{Name: "https", Hostname: hostnamePtr("notes.example.com"), Port: 443, Protocol: gwv1.HTTPSProtocolType},
	}, nil)
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name: "r", Namespace: "app",
			Annotations: map[string]string{conventions.AnnotationNoTLSVerify: "true"},
		},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames:       []gwv1.Hostname{"notes.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}}},
			Rules:           []gwv1.HTTPRouteRule{{}},
		},
	}
	got := reconcileHTTPRouteAndGetContrib(t, gw, rt)
	require.NotNil(t, got.NoTLSVerify, "NoTLSVerify must be populated from defaults")
	require.True(t, *got.NoTLSVerify, "NoTLSVerify must reflect the annotation value")
}

// T1.7 — cloudflare.io/origin-server-name on the Gateway flows into the route's
// contribution (inheritance via inheritedAnnotations + defaultsFromAnnotations).
func TestHTTPRouteSource_OriginServerNameInheritedFromGateway(t *testing.T) {
	gw := mkTunnelGwWithListeners("gw", "gw-ns", []gwv1.Listener{
		{Name: "https", Hostname: hostnamePtr("notes.example.com"), Port: 443, Protocol: gwv1.HTTPSProtocolType},
	}, map[string]string{conventions.AnnotationOriginServerName: "external.x.com"})
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames:       []gwv1.Hostname{"notes.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}}},
			Rules:           []gwv1.HTTPRouteRule{{}},
		},
	}
	got := reconcileHTTPRouteAndGetContrib(t, gw, rt)
	require.NotNil(t, got.OriginServerName, "OriginServerName must inherit from the Gateway annotation")
	require.Equal(t, "external.x.com", *got.OriginServerName)
}

// T1.8 — Route's origin-server-name overrides Gateway's.
func TestHTTPRouteSource_OriginServerNameRouteOverridesGateway(t *testing.T) {
	gw := mkTunnelGwWithListeners("gw", "gw-ns", []gwv1.Listener{
		{Name: "https", Hostname: hostnamePtr("notes.example.com"), Port: 443, Protocol: gwv1.HTTPSProtocolType},
	}, map[string]string{conventions.AnnotationOriginServerName: "gw-val"})
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name: "r", Namespace: "app",
			Annotations: map[string]string{conventions.AnnotationOriginServerName: "route-val"},
		},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames:       []gwv1.Hostname{"notes.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}}},
			Rules:           []gwv1.HTTPRouteRule{{}},
		},
	}
	got := reconcileHTTPRouteAndGetContrib(t, gw, rt)
	require.NotNil(t, got.OriginServerName)
	require.Equal(t, "route-val", *got.OriginServerName, "route annotation must override Gateway value")
}

// T1.9 — origin-server-name flows unconditionally; no dependency on
// no-tls-verify being set. Regression guard for an easy mistake where one
// annotation gates the other.
func TestHTTPRouteSource_OriginServerNameUnconditional(t *testing.T) {
	gw := mkTunnelGwWithListeners("gw", "gw-ns", []gwv1.Listener{
		{Name: "https", Hostname: hostnamePtr("notes.example.com"), Port: 443, Protocol: gwv1.HTTPSProtocolType},
	}, nil)
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name: "r", Namespace: "app",
			Annotations: map[string]string{
				conventions.AnnotationOriginServerName: "san.example.com",
				// no-tls-verify intentionally omitted.
			},
		},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames:       []gwv1.Hostname{"notes.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}}},
			Rules:           []gwv1.HTTPRouteRule{{}},
		},
	}
	got := reconcileHTTPRouteAndGetContrib(t, gw, rt)
	require.Nil(t, got.NoTLSVerify, "no-tls-verify unset -> NoTLSVerify stays nil")
	require.NotNil(t, got.OriginServerName, "origin-server-name must surface regardless of no-tls-verify")
	require.Equal(t, "san.example.com", *got.OriginServerName)
}

func TestFirstListenerHostname_IsLexicographicallyStable(t *testing.T) {
	h := func(s string) *gwv1.Hostname {
		v := gwv1.Hostname(s)
		return &v
	}
	gw := &gwv1.Gateway{
		Spec: gwv1.GatewaySpec{
			Listeners: []gwv1.Listener{
				{Name: "https-b", Hostname: h("beta.example.com")},
				{Name: "https-a", Hostname: h("alpha.example.com")},
				{Name: "no-host", Hostname: nil},
				{Name: "https-c", Hostname: h("gamma.example.com")},
			},
		},
	}
	got := firstListenerHostname(gw)
	require.Equal(t, "alpha.example.com", got,
		"must pick lex-smallest hostname; listener input order should not matter")

	// Reverse the input order; result must not change.
	reversed := &gwv1.Gateway{Spec: gwv1.GatewaySpec{}}
	for i := len(gw.Spec.Listeners) - 1; i >= 0; i-- {
		reversed.Spec.Listeners = append(reversed.Spec.Listeners, gw.Spec.Listeners[i])
	}
	require.Equal(t, "alpha.example.com", firstListenerHostname(reversed))
}

// ---------------------------------------------------------------------------
// N1a — direct unit coverage for hostnameMatchesListener.
//
// Gateway-API listener vs. route hostname matching semantics: an empty
// listener hostname matches anything; "*.suffix" matches a SINGLE-LABEL
// subdomain but NOT the apex and NOT multi-label subdomains; otherwise the
// comparison is exact. This is the helper that backs the HTTPS-preferred
// listener pick during scheme resolution (see schemeForParent).
// ---------------------------------------------------------------------------

func TestHostnameMatchesListener(t *testing.T) {
	cases := []struct {
		name         string
		listenerHost string
		routeHost    string
		want         bool
	}{
		{
			name:         "empty_listener_matches_any_route",
			listenerHost: "",
			routeHost:    "foo.example.com",
			want:         true,
		},
		{
			name:         "exact_match",
			listenerHost: "example.com",
			routeHost:    "example.com",
			want:         true,
		},
		{
			name:         "exact_listener_does_not_implicit_suffix_match",
			listenerHost: "example.com",
			routeHost:    "foo.example.com",
			want:         false,
		},
		{
			name:         "wildcard_single_label_match",
			listenerHost: "*.example.com",
			routeHost:    "foo.example.com",
			want:         true,
		},
		{
			name:         "wildcard_does_not_match_multi_label",
			listenerHost: "*.example.com",
			routeHost:    "foo.bar.example.com",
			want:         false,
		},
		{
			name:         "wildcard_does_not_match_apex",
			listenerHost: "*.example.com",
			routeHost:    "example.com",
			want:         false,
		},
		{
			name:         "wildcard_suffix_mismatch",
			listenerHost: "*.example.com",
			routeHost:    "other.com",
			want:         false,
		},
		{
			// Gateway-API spec says only the LISTENER side carries wildcards;
			// a route hostname starting with "*." is not expected, but the
			// helper must not panic and must return false (it falls into the
			// concrete-vs-concrete exact-match branch and compares strings).
			name:         "route_side_wildcard_is_not_expanded",
			listenerHost: "foo.example.com",
			routeHost:    "*.example.com",
			want:         false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hostnameMatchesListener(tc.listenerHost, tc.routeHost)
			require.Equal(t, tc.want, got,
				"hostnameMatchesListener(%q, %q) = %v; want %v",
				tc.listenerHost, tc.routeHost, got, tc.want)
		})
	}
}

// ---------------------------------------------------------------------------
// N1b — integration test for the user's actual live failure.
//
// Production scenario: parent Gateway has a wildcard "*.example.com" hostname
// declared on BOTH an HTTP and an HTTPS sibling listener. An HTTPRoute names
// a concrete sub-hostname ("jellyfin.example.com") with NO parentRef
// SectionName and NO cloudflare.io/scheme annotation. The user was getting
// http:// in the contribution; the fix is HTTPS-preferred among the matching
// listeners. Also asserts cloudflare.io/origin-server-name inherits from the
// Gateway when not set on the route.
// ---------------------------------------------------------------------------

func TestHTTPRouteSource_BugScenario_WildcardListenerMatchesConcreteRoute(t *testing.T) {
	gw := mkTunnelGwWithListeners("gw", "gw-ns", []gwv1.Listener{
		// HTTP listed first to mirror the live failure ordering; the helper
		// must still pick HTTPS.
		{Name: "http", Hostname: hostnamePtr("*.example.com"), Port: 80, Protocol: gwv1.HTTPProtocolType},
		{Name: "https", Hostname: hostnamePtr("*.example.com"), Port: 443, Protocol: gwv1.HTTPSProtocolType},
	}, map[string]string{
		conventions.AnnotationOriginServerName: "external.example.com",
	})
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "app"},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"jellyfin.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{{
				// No SectionName: must traverse the hostname-match pass.
				Name: "gw", Namespace: ptrNs("gw-ns"),
			}}},
			Rules: []gwv1.HTTPRouteRule{{}},
		},
	}
	got := reconcileHTTPRouteAndGetContrib(t, gw, rt)
	require.True(t, strings.HasPrefix(got.Service, "https://"),
		"wildcard listener match on concrete sub-hostname must prefer HTTPS over HTTP sibling, got %q", got.Service)
	require.NotNil(t, got.OriginServerName,
		"Gateway-level cloudflare.io/origin-server-name must inherit when the route does not override it")
	require.Equal(t, "external.example.com", *got.OriginServerName)
}

// ---------------------------------------------------------------------------
// N2 — cloudflare.io/scheme=<garbage> silent fall-through (route side).
//
// Design contract (see schemeForParent doc + Bug 1 review): an unrecognized
// scheme value silently falls through to the listener-derived path. No error,
// no Warning event. This locks the regression at the route source.
// ---------------------------------------------------------------------------

func TestHTTPRouteSource_SchemeGarbage_FallsThroughToListener(t *testing.T) {
	gw := mkTunnelGwWithListeners("gw", "gw-ns", []gwv1.Listener{
		{Name: "https", Hostname: hostnamePtr("notes.example.com"), Port: 443, Protocol: gwv1.HTTPSProtocolType},
	}, nil)
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name: "r", Namespace: "app",
			Annotations: map[string]string{conventions.AnnotationScheme: "garbage"},
		},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames:       []gwv1.Hostname{"notes.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: ptrNs("gw-ns")}}},
			Rules:           []gwv1.HTTPRouteRule{{}},
		},
	}
	got := reconcileHTTPRouteAndGetContrib(t, gw, rt)
	require.True(t, strings.HasPrefix(got.Service, "https://"),
		"cloudflare.io/scheme=garbage must silently fall through to the HTTPS listener, got %q", got.Service)
}
