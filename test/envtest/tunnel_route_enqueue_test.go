/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package envtest_test

// Tests for simplify finding A: tunnelToHTTPRoutes / tunnelToTLSRoutes
// annotation-filter bug.
//
// Before the fix: both MapFuncs filtered routes by cloudflare.io/tunnel=true —
// an annotation that lives on Gateways, not on Routes. The filter matched zero
// Routes, so the watch was a no-op: Routes did not re-reconcile when the
// tunnel's Status.TunnelCNAME populated. DNS emission was deferred until
// some other event fired.
//
// After the fix: both MapFuncs enqueue every Route in the tunnel's namespace.
// Non-opted-in Routes short-circuit cleanly in their own Reconcile via
// DeriveTunnelName (microsecond no-op, no CF or apiserver writes).
//
// Non-vacuity guarantee (per plan constraint #12):
//   The HTTPRoute and TLSRoute are created BEFORE the tunnel's TunnelCNAME is
//   set. Their reconcilers run, see TunnelCNAME=="", and defer emission — so
//   no DNSRecord exists at quiesce. We snapshot that absence, then patch the
//   TunnelCNAME. The assertion that a DNSRecord subsequently appears is load-
//   bearing: it can only happen if the CNAME patch triggered the route reconcile
//   via the tunnelToHTTPRoutes / tunnelToTLSRoutes watch.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/controller/tunnel"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

// tunnelRouteEnqueueFixture holds the minimal wiring for the route-enqueue
// envtests: a client + namespace. No mock CF client is needed because no
// CloudflareTunnel reconciler is wired — we control TunnelCNAME manually.
type tunnelRouteEnqueueFixture struct {
	c  client.Client
	ns string
}

// setupTunnelRouteHTTPEnv wires the HTTPRouteSourceReconciler with the
// production tunnelToHTTPRoutes MapFunc (exported as tunnel.TunnelToHTTPRoutesFunc).
// No CloudflareTunnelReconciler is wired so TunnelCNAME stays empty until the
// test patches it via Status().Update.
func setupTunnelRouteHTTPEnv(t *testing.T) *tunnelRouteEnqueueFixture {
	t.Helper()

	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	sch := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(sch))
	utilruntime.Must(v2alpha1.AddToScheme(sch))
	utilruntime.Must(gwv1.Install(sch))

	// Start from an empty cluster: earlier tests' CRs outlive them in the
	// shared apiserver and every manager watches cluster-wide.
	purgeCloudflareCRs(t)

	mgr, err := ctrl.NewManager(sharedConfig, ctrl.Options{
		Scheme:  sch,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	require.NoError(t, err)

	cache := tunnelsynth.NewCache()

	// HTTPRouteSource wired with the production tunnelToHTTPRoutes MapFunc —
	// this is the function under test. At RED the MapFunc filters by route
	// annotation → returns 0 requests → the reconcile never fires on CNAME
	// change. At GREEN the filter is removed → every route in the namespace
	// is enqueued → the reconcile fires and emits the DNSRecord.
	rtR := &tunnel.HTTPRouteSourceReconciler{
		Client:   mgr.GetClient(),
		Scheme:   sch,
		Cache:    cache,
		Recorder: mgr.GetEventRecorderFor("cloudflare-operator-httproute-source-enqueue-test"),
	}
	require.NoError(t, ctrl.NewControllerManagedBy(mgr).
		Named("httproutesource-enqueue-"+sanitizeTestName(t.Name())).
		For(&gwv1.HTTPRoute{}).
		Owns(&v2alpha1.CloudflareDNSRecord{}).
		Watches(&gwv1.Gateway{},
			handler.EnqueueRequestsFromMapFunc(gatewayToHTTPRoutesTestMapFunc(mgr))).
		Watches(&v2alpha1.CloudflareTunnel{},
			handler.EnqueueRequestsFromMapFunc(tunnel.TunnelToHTTPRoutesFunc(mgr))).
		Complete(rtR))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = mgr.Start(ctx) }()

	syncCtx, syncCancel := context.WithTimeout(ctx, 30*time.Second)
	defer syncCancel()
	require.True(t, mgr.GetCache().WaitForCacheSync(syncCtx), "manager cache failed to sync")

	ns := shortUniqueNamespace(t)
	require.NoError(t, mgr.GetClient().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}))

	return &tunnelRouteEnqueueFixture{c: mgr.GetClient(), ns: ns}
}

// setupTunnelRouteTLSEnv mirrors setupTunnelRouteHTTPEnv for TLSRoute.
func setupTunnelRouteTLSEnv(t *testing.T) *tunnelRouteEnqueueFixture {
	t.Helper()

	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	sch := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(sch))
	utilruntime.Must(v2alpha1.AddToScheme(sch))
	utilruntime.Must(gwv1.Install(sch))
	utilruntime.Must(gwv1a2.Install(sch))

	// Start from an empty cluster: earlier tests' CRs outlive them in the
	// shared apiserver and every manager watches cluster-wide.
	purgeCloudflareCRs(t)

	mgr, err := ctrl.NewManager(sharedConfig, ctrl.Options{
		Scheme:  sch,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	require.NoError(t, err)

	cache := tunnelsynth.NewCache()

	// TLSRouteSource wired with the production tunnelToTLSRoutes MapFunc.
	rtR := &tunnel.TLSRouteSourceReconciler{
		Client:   mgr.GetClient(),
		Scheme:   sch,
		Cache:    cache,
		Recorder: mgr.GetEventRecorderFor("cloudflare-operator-tlsroute-source-enqueue-test"),
	}
	require.NoError(t, ctrl.NewControllerManagedBy(mgr).
		Named("tlsroutesource-enqueue-"+sanitizeTestName(t.Name())).
		For(&gwv1a2.TLSRoute{}).
		Owns(&v2alpha1.CloudflareDNSRecord{}).
		Watches(&gwv1.Gateway{},
			handler.EnqueueRequestsFromMapFunc(gatewayToTLSRoutesTestMapFunc(mgr))).
		Watches(&v2alpha1.CloudflareTunnel{},
			handler.EnqueueRequestsFromMapFunc(tunnel.TunnelToTLSRoutesFunc(mgr))).
		Complete(rtR))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = mgr.Start(ctx) }()

	syncCtx, syncCancel := context.WithTimeout(ctx, 30*time.Second)
	defer syncCancel()
	require.True(t, mgr.GetCache().WaitForCacheSync(syncCtx), "manager cache failed to sync")

	ns := shortUniqueNamespace(t)
	require.NoError(t, mgr.GetClient().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}))

	return &tunnelRouteEnqueueFixture{c: mgr.GetClient(), ns: ns}
}

// TestEnvtest_Tunnel_RouteEnqueue_OnCNAMEStatusChange verifies that when
// a CloudflareTunnel's Status.TunnelCNAME changes from empty to non-empty,
// attached HTTPRoutes re-reconcile and emit their DNSRecord CRs.
//
// Non-vacuity: the HTTPRoute is created before TunnelCNAME is set. The
// reconciler defers emission (TunnelCNAME == ""), so no DNSRecord exists at
// quiesce. After the CNAME patch, the DNSRecord must appear — proving the
// tunnelToHTTPRoutes watch triggered the reconcile (not the HTTPRoute creation
// event itself).
func TestEnvtest_Tunnel_RouteEnqueue_OnCNAMEStatusChange(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupTunnelRouteHTTPEnv(t)
	ctx := context.Background()

	// CloudflareZone required by DNSRecord CEL admission (has(zoneRef) || has(zoneID)).
	zone := &v2alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: f.ns},
		Spec: v2alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, f.c.Create(ctx, zone))

	// Backing service for the Gateway.
	gwSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-svc", Namespace: f.ns},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	require.NoError(t, f.c.Create(ctx, gwSvc))

	// CloudflareTunnel CR with empty Status.TunnelCNAME. Created manually
	// (no tunnel reconciler wired) so TunnelCNAME stays "" until we patch it.
	tunnelName := f.ns + "-edge"
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tunnelName,
			Namespace: f.ns,
		},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name: "edge",
			Connector: v2alpha1.ConnectorSpec{
				Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30,
			},
		},
	}
	require.NoError(t, f.c.Create(ctx, tn))

	// Gateway annotated with cloudflare.io/tunnel=true (the opt-in annotation
	// lives on the Gateway, NOT the Route — this is the exact bug the fix addresses).
	gwHostname := gwv1.Hostname("ext.example.com")
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gw",
			Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:         "true",
				conventions.AnnotationTunnelName:     "edge",
				conventions.AnnotationGatewayService: f.ns + "/gw-svc",
			},
		},
		Spec: gwv1.GatewaySpec{
			GatewayClassName: "any-class",
			Listeners: []gwv1.Listener{{
				Name:     "h",
				Hostname: &gwHostname,
				Port:     80,
				Protocol: gwv1.HTTPProtocolType,
			}},
		},
	}
	require.NoError(t, f.c.Create(ctx, gw))

	// HTTPRoute attached to the Gateway. No cloudflare.io/tunnel annotation on
	// the route — the fix removes the filter that incorrectly required it.
	nsRef := gwv1.Namespace(f.ns)
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "r",
			Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationZoneRef: "example-com",
			},
		},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"app.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: &nsRef}},
			},
			Rules: []gwv1.HTTPRouteRule{{}},
		},
	}
	require.NoError(t, f.c.Create(ctx, rt))

	// Quiesce: wait for at least one reconcile cycle to run. The reconciler
	// sees TunnelCNAME=="" and defers emission, so no DNSRecord should exist.
	// Snapshot the absence as the pre-patch baseline.
	time.Sleep(2 * time.Second)
	var preList v2alpha1.CloudflareDNSRecordList
	require.NoError(t, f.c.List(ctx, &preList, client.InNamespace(f.ns)))
	for _, dr := range preList.Items {
		if dr.Spec.Name == "app.example.com" {
			t.Fatalf("pre-patch snapshot: DNSRecord for app.example.com already exists — "+
				"test is vacuous (TunnelCNAME was set before the patch). "+
				"dr.Name=%q dr.Spec.Type=%q", dr.Name, dr.Spec.Type)
		}
	}

	// Action: patch the tunnel's Status.TunnelCNAME to a non-empty value.
	// This is the event that tunnelToHTTPRoutes must respond to by enqueueing
	// the HTTPRoute for re-reconciliation.
	var latestTn v2alpha1.CloudflareTunnel
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: tunnelName}, &latestTn))
	latestTn.Status.TunnelCNAME = "tunnel-abc.cfargotunnel.com"
	require.NoError(t, f.c.Status().Update(ctx, &latestTn))

	// Assertion: the HTTPRoute reconciler fires (triggered by the watch) and
	// emits a chain CNAME DNSRecord for app.example.com → ext.example.com.
	// This can only happen if tunnelToHTTPRoutes enqueued the route.
	require.Eventually(t, func() bool {
		var list v2alpha1.CloudflareDNSRecordList
		if err := f.c.List(ctx, &list, client.InNamespace(f.ns)); err != nil {
			return false
		}
		for _, dr := range list.Items {
			if dr.Spec.Type == "CNAME" && dr.Spec.Name == "app.example.com" {
				return true
			}
		}
		return false
	}, 15*time.Second, 250*time.Millisecond,
		"CNAME DNSRecord for app.example.com must appear after TunnelCNAME patch "+
			"(proves tunnelToHTTPRoutes watch enqueued the route)")
}

// TestEnvtest_Tunnel_RouteEnqueue_TLSRoute_OnCNAMEStatusChange mirrors
// TestEnvtest_Tunnel_RouteEnqueue_OnCNAMEStatusChange for TLSRoute (v1alpha2).
func TestEnvtest_Tunnel_RouteEnqueue_TLSRoute_OnCNAMEStatusChange(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupTunnelRouteTLSEnv(t)
	ctx := context.Background()

	// CloudflareZone required by DNSRecord CEL admission.
	zone := &v2alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: f.ns},
		Spec: v2alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, f.c.Create(ctx, zone))

	// Backing service for the Gateway.
	gwSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-svc", Namespace: f.ns},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: 443}},
		},
	}
	require.NoError(t, f.c.Create(ctx, gwSvc))

	// CloudflareTunnel CR with empty Status.TunnelCNAME.
	tunnelName := f.ns + "-edge"
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tunnelName,
			Namespace: f.ns,
		},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name: "edge",
			Connector: v2alpha1.ConnectorSpec{
				Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30,
			},
		},
	}
	require.NoError(t, f.c.Create(ctx, tn))

	// Gateway with a TLS listener.
	gwHostname := gwv1.Hostname("ext.example.com")
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gw",
			Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:         "true",
				conventions.AnnotationTunnelName:     "edge",
				conventions.AnnotationGatewayService: f.ns + "/gw-svc",
			},
		},
		Spec: gwv1.GatewaySpec{
			GatewayClassName: "any-class",
			Listeners: []gwv1.Listener{{
				Name:     "tls",
				Hostname: &gwHostname,
				Port:     443,
				Protocol: gwv1.TLSProtocolType,
				TLS:      tlsPassthroughConfig(),
			}},
		},
	}
	require.NoError(t, f.c.Create(ctx, gw))

	// TLSRoute attached to the Gateway. No cloudflare.io/tunnel annotation.
	nsRef := gwv1.Namespace(f.ns)
	rt := &gwv1a2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "r",
			Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationZoneRef: "example-com",
			},
		},
		Spec: gwv1a2.TLSRouteSpec{
			Hostnames: []gwv1.Hostname{"tls.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: &nsRef}},
			},
			Rules: tlsRoutePlaceholderRules(),
		},
	}
	require.NoError(t, f.c.Create(ctx, rt))

	// Quiesce: assert no DNSRecord for tls.example.com before the patch.
	time.Sleep(2 * time.Second)
	var preList v2alpha1.CloudflareDNSRecordList
	require.NoError(t, f.c.List(ctx, &preList, client.InNamespace(f.ns)))
	for _, dr := range preList.Items {
		if dr.Spec.Name == "tls.example.com" {
			t.Fatalf("pre-patch snapshot: DNSRecord for tls.example.com already exists — "+
				"test is vacuous. dr.Name=%q dr.Spec.Type=%q", dr.Name, dr.Spec.Type)
		}
	}

	// Patch TunnelCNAME.
	var latestTn v2alpha1.CloudflareTunnel
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: tunnelName}, &latestTn))
	latestTn.Status.TunnelCNAME = "tunnel-abc.cfargotunnel.com"
	require.NoError(t, f.c.Status().Update(ctx, &latestTn))

	// Assertion: CNAME DNSRecord for tls.example.com appears.
	require.Eventually(t, func() bool {
		var list v2alpha1.CloudflareDNSRecordList
		if err := f.c.List(ctx, &list, client.InNamespace(f.ns)); err != nil {
			return false
		}
		for _, dr := range list.Items {
			if dr.Spec.Type == "CNAME" && dr.Spec.Name == "tls.example.com" {
				return true
			}
		}
		return false
	}, 15*time.Second, 250*time.Millisecond,
		"CNAME DNSRecord for tls.example.com must appear after TunnelCNAME patch "+
			"(proves tunnelToTLSRoutes watch enqueued the route)")
}
