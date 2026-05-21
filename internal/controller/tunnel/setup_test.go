/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package tunnel

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
)

// stubManager implements manager.Manager with only GetClient() — sufficient
// for testing tunnelToHTTPRoutes / tunnelToTLSRoutes, which call only
// mgr.GetClient().List(). All other methods are supplied by the embedded nil
// interface and panic if called; the tests must not trigger them.
type stubManager struct {
	manager.Manager // nil — panics if any non-overridden method is called
	c               client.Client
}

func (s stubManager) GetClient() client.Client { return s.c }

// TestAddToManager_DefaultConnectorResourcesPassthrough verifies that
// applyOptionDefaults does not clobber DefaultConnector.Resources — the
// caller-supplied ResourceRequirements must survive the defaulting pass
// unchanged while scalar fields (Replicas, Protocol, LogLevel,
// GracePeriodSeconds) are still filled in.
func TestAddToManager_DefaultConnectorResourcesPassthrough(t *testing.T) {
	want := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m")},
	}
	opts := Options{DefaultConnector: v2alpha1.ConnectorSpec{Resources: want}}
	applyOptionDefaults(&opts)
	require.Equal(t, want, opts.DefaultConnector.Resources)
	require.Equal(t, int32(2), opts.DefaultConnector.Replicas) // scalar defaulting still applies
}

// mapFuncScheme registers the types needed by the tunnelTo* MapFuncs in unit
// tests: CloudflareTunnel (the obj arg), HTTPRoute, and TLSRoute (the listed
// types).
func mapFuncScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v2alpha1.AddToScheme(s))
	require.NoError(t, gwv1.Install(s))
	require.NoError(t, gwv1a2.Install(s))
	return s
}

// TestTunnelToHTTPRoutes_EnqueuesAllRoutesInNamespace asserts that the fixed
// tunnelToHTTPRoutes MapFunc enqueues every HTTPRoute in the tunnel's namespace,
// regardless of whether the route carries a cloudflare.io/tunnel annotation.
//
// Before the fix the function filtered by rt.Annotations["cloudflare.io/tunnel"]
// == "true". Routes without that annotation returned 0 requests. After the fix
// it returns all routes in the namespace — non-opted-in routes short-circuit
// cleanly in their own Reconcile.
//
// At RED (before setup.go fix): the annotation filter drops 2 of 3 routes in
// ns "x" (only the one with the annotation is returned) and returns 0 for ns
// "y" routes entirely. The require.Len assertion fails (got 1, want 3).
// At GREEN (after setup.go fix): all 3 routes in ns "x" are returned,
// 0 routes from ns "y".
func TestTunnelToHTTPRoutes_EnqueuesAllRoutesInNamespace(t *testing.T) {
	s := mapFuncScheme(t)

	// 3 HTTPRoutes in namespace "x"; 2 in namespace "y".
	// Only rt1 carries the (formerly-required) cloudflare.io/tunnel annotation.
	rt1 := &gwv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{
		Name: "rt1", Namespace: "x",
		Annotations: map[string]string{"cloudflare.io/tunnel": "true"},
	}}
	rt2 := &gwv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "rt2", Namespace: "x"}}
	rt3 := &gwv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "rt3", Namespace: "x"}}
	rtY1 := &gwv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "rty1", Namespace: "y"}}
	rtY2 := &gwv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "rty2", Namespace: "y"}}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(rt1, rt2, rt3, rtY1, rtY2).
		Build()

	mgr := stubManager{c: fakeClient}

	// CloudflareTunnel in ns "x" — the MapFunc enqueues routes in tn.Namespace.
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tn", Namespace: "x"},
	}
	mapFn := tunnelToHTTPRoutes(mgr)
	reqs := mapFn(context.Background(), tn)

	// All 3 routes in ns "x" must be enqueued; none from ns "y".
	require.Len(t, reqs, 3, "all HTTPRoutes in the tunnel's namespace must be enqueued (annotation filter removed)")
	for _, r := range reqs {
		require.Equal(t, "x", r.Namespace, "enqueued route must be in ns 'x'")
	}
}

// TestTunnelToTLSRoutes_EnqueuesAllRoutesInNamespace mirrors
// TestTunnelToHTTPRoutes_EnqueuesAllRoutesInNamespace for TLSRoute.
func TestTunnelToTLSRoutes_EnqueuesAllRoutesInNamespace(t *testing.T) {
	s := mapFuncScheme(t)

	// 3 TLSRoutes in namespace "x"; 2 in namespace "y".
	rt1 := &gwv1a2.TLSRoute{ObjectMeta: metav1.ObjectMeta{
		Name: "rt1", Namespace: "x",
		Annotations: map[string]string{"cloudflare.io/tunnel": "true"},
	}}
	rt2 := &gwv1a2.TLSRoute{ObjectMeta: metav1.ObjectMeta{Name: "rt2", Namespace: "x"}}
	rt3 := &gwv1a2.TLSRoute{ObjectMeta: metav1.ObjectMeta{Name: "rt3", Namespace: "x"}}
	rtY1 := &gwv1a2.TLSRoute{ObjectMeta: metav1.ObjectMeta{Name: "rty1", Namespace: "y"}}
	rtY2 := &gwv1a2.TLSRoute{ObjectMeta: metav1.ObjectMeta{Name: "rty2", Namespace: "y"}}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(rt1, rt2, rt3, rtY1, rtY2).
		Build()

	mgr := stubManager{c: fakeClient}

	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tn", Namespace: "x"},
	}
	mapFn := tunnelToTLSRoutes(mgr)
	reqs := mapFn(context.Background(), tn)

	require.Len(t, reqs, 3, "all TLSRoutes in the tunnel's namespace must be enqueued (annotation filter removed)")
	for _, r := range reqs {
		require.Equal(t, "x", r.Namespace, "enqueued route must be in ns 'x'")
	}
}
