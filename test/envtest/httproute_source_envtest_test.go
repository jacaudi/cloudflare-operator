/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package envtest_test

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
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	mockcf "github.com/jacaudi/cloudflare-operator/internal/cloudflare/mock"
	"github.com/jacaudi/cloudflare-operator/internal/controller/tunnel"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

// httpRouteEnvFixture wires HTTPRouteSourceReconciler + GatewaySourceReconciler
// + CloudflareTunnelReconciler inline, sharing one tunnelsynth.Cache. The
// Gateway source is needed so the parent Gateway's tunnel CR gets auto-created
// (HTTPRoute source never auto-creates tunnels per design §4.2). The tunnel
// reconciler populates Status.TunnelCNAME so the HTTPRoute emission can
// advance past its deferred-emission guard.
type httpRouteEnvFixture struct {
	c    client.Client
	mock *mockcf.Mock
	ns   string
}

func setupHTTPRouteEnv(t *testing.T) *httpRouteEnvFixture {
	t.Helper()

	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	sch := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(sch))
	utilruntime.Must(v2alpha1.AddToScheme(sch))
	utilruntime.Must(gwv1.Install(sch))

	mgr, err := ctrl.NewManager(sharedConfig, ctrl.Options{
		Scheme:  sch,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	require.NoError(t, err)

	m := mockcf.New()
	cache := tunnelsynth.NewCache()

	// CloudflareTunnel reconciler.
	tunnelR := &tunnel.CloudflareTunnelReconciler{
		Client:   mgr.GetClient(),
		Scheme:   sch,
		Recorder: mgr.GetEventRecorderFor("cloudflare-operator-tunnel-rt-test"),
		TunnelClientFn: func(_ cloudflare.Credentials) (cloudflare.TunnelClient, error) {
			return m.Tunnel, nil
		},
		Cache:        cache,
		DefaultImage: tunnel.DefaultCloudflaredImage,
	}
	require.NoError(t, ctrl.NewControllerManagedBy(mgr).
		Named("cloudflaretunnel-"+sanitizeTestName(t.Name())).
		For(&v2alpha1.CloudflareTunnel{}).
		Complete(tunnelR))

	// GatewaySource — so the parent Gateway's tunnel CR is auto-created.
	gwR := &tunnel.GatewaySourceReconciler{
		Client:   mgr.GetClient(),
		Scheme:   sch,
		Cache:    cache,
		Recorder: mgr.GetEventRecorderFor("cloudflare-operator-gw-source-rt-test"),
		DefaultConnector: v2alpha1.ConnectorSpec{
			Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30,
		},
	}
	require.NoError(t, ctrl.NewControllerManagedBy(mgr).
		Named("gatewaysource-"+sanitizeTestName(t.Name())).
		For(&gwv1.Gateway{}).
		Watches(&v2alpha1.CloudflareTunnel{},
			handler.EnqueueRequestsFromMapFunc(tunnelToGatewaysTestMapFunc(mgr))).
		Complete(gwR))

	// HTTPRouteSource — the unit under test. Mirrors setup.go's wiring:
	// watches HTTPRoute (primary), Gateway (parent change), CloudflareTunnel
	// (deferred-emission retrigger).
	rtR := &tunnel.HTTPRouteSourceReconciler{
		Client:   mgr.GetClient(),
		Scheme:   sch,
		Cache:    cache,
		Recorder: mgr.GetEventRecorderFor("cloudflare-operator-httproute-source-test"),
	}
	// NB: production setup.go wires a third Watch (CloudflareTunnel →
	// HTTPRoutes) for deferred-emission retrigger. The envtest fixture skips
	// that watch because both tests wait for the parent Gateway's tunnel to
	// reach Status.TunnelCNAME populated BEFORE creating the HTTPRoute, so
	// the deferred-emission path is never taken. Keeping the wiring minimal
	// keeps the LOC budget honest without losing coverage of the
	// HTTPRouteSource's own state machine.
	require.NoError(t, ctrl.NewControllerManagedBy(mgr).
		Named("httproutesource-"+sanitizeTestName(t.Name())).
		For(&gwv1.HTTPRoute{}).
		Owns(&v2alpha1.CloudflareDNSRecord{}).
		Watches(&gwv1.Gateway{},
			handler.EnqueueRequestsFromMapFunc(gatewayToHTTPRoutesTestMapFunc(mgr))).
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

	return &httpRouteEnvFixture{c: mgr.GetClient(), mock: m, ns: ns}
}

// gatewayToHTTPRoutesTestMapFunc mirrors setup.go::gatewayToHTTPRoutes — list
// every HTTPRoute, enqueue those whose parentRefs include the changed Gateway.
func gatewayToHTTPRoutesTestMapFunc(mgr ctrl.Manager) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		gw, ok := obj.(*gwv1.Gateway)
		if !ok {
			return nil
		}
		var routes gwv1.HTTPRouteList
		if err := mgr.GetClient().List(ctx, &routes); err != nil {
			return nil
		}
		out := make([]reconcile.Request, 0)
		for _, rt := range routes.Items {
			for _, pr := range rt.Spec.ParentRefs {
				if pr.Kind != nil && *pr.Kind != "Gateway" {
					continue
				}
				if pr.Group != nil && *pr.Group != "gateway.networking.k8s.io" {
					continue
				}
				ns := rt.Namespace
				if pr.Namespace != nil {
					ns = string(*pr.Namespace)
				}
				if ns == gw.Namespace && string(pr.Name) == gw.Name {
					out = append(out, reconcile.Request{
						NamespacedName: types.NamespacedName{Namespace: rt.Namespace, Name: rt.Name},
					})
					break
				}
			}
		}
		return out
	}
}

// createGatewayForRouteTest stands up a backing Service + tunnel-targeted
// Gateway in the fixture namespace and waits for the auto-created tunnel CR
// to populate Status.TunnelCNAME — so HTTPRoutes attached afterwards see a
// ready tunnel and can emit DNSRecords on the first reconcile pass.
//
// gatewayApex sets the cloudflare.io/gateway-apex annotation when non-empty,
// flipping chainContentFor (apex.go) onto its override branch so emitted
// chain CNAMEs anchor at the supplied apex instead of the tunnel's own CNAME.
// Pass "" to exercise the default concrete-listener-no-override behavior.
func createGatewayForRouteTest(t *testing.T, f *httpRouteEnvFixture, gatewayApex string) {
	t.Helper()
	ctx := context.Background()

	gwSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-svc", Namespace: f.ns},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	require.NoError(t, f.c.Create(ctx, gwSvc))

	gwAnnotations := map[string]string{
		conventions.AnnotationTunnel:         "true",
		conventions.AnnotationTunnelName:     "edge",
		conventions.AnnotationGatewayService: f.ns + "/gw-svc",
	}
	if gatewayApex != "" {
		gwAnnotations[conventions.AnnotationGatewayApex] = gatewayApex
	}

	h := gwv1.Hostname("ext.example.com")
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "gw",
			Namespace:   f.ns,
			Annotations: gwAnnotations,
		},
		Spec: gwv1.GatewaySpec{
			GatewayClassName: "any-class",
			Listeners: []gwv1.Listener{{
				Name: "h", Hostname: &h, Port: 80, Protocol: gwv1.HTTPProtocolType,
			}},
		},
	}
	require.NoError(t, f.c.Create(ctx, gw))

	expectedTunnel := f.ns + "-edge"
	require.Eventually(t, func() bool {
		var tn v2alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: expectedTunnel}, &tn); err != nil {
			return false
		}
		return tn.Status.TunnelCNAME != ""
	}, 20*time.Second, 250*time.Millisecond, "parent Gateway's tunnel %q ready", expectedTunnel)
}

// TestHTTPRouteSourceEnvtest_AttachedEmitsChainCNAMEAndIngress covers design
// §12.5: an HTTPRoute attached (via parentRefs) to a tunnel-targeted Gateway
// emits a chain CNAME (route-hostname → gateway-apex) AND its hostnames /
// rules land in the cloudflared ingress configuration the tunnel reconciler
// PUTs to Cloudflare.
func TestHTTPRouteSourceEnvtest_AttachedEmitsChainCNAMEAndIngress(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupHTTPRouteEnv(t)
	ctx := context.Background()

	// Zone CR for example.com so the emitted DNSRecord has a valid zoneRef.
	zone := &v2alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: f.ns},
		Spec: v2alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, f.c.Create(ctx, zone))

	createGatewayForRouteTest(t, f, "")

	// Capture the parent tunnel's CNAME for the chain-content assertion below.
	// For Gateways without an explicit cloudflare.io/gateway-apex annotation,
	// chainContentFor (apex.go) returns tn.Status.TunnelCNAME directly — the
	// route's per-hostname CNAME hops straight to the tunnel, skipping any
	// implicit "listener hostname is the apex" interpretation. The
	// gateway-apex override path is covered by a sibling test.
	var parentTunnel v2alpha1.CloudflareTunnel
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: f.ns + "-edge"}, &parentTunnel))
	require.NotEmpty(t, parentTunnel.Status.TunnelCNAME,
		"createGatewayForRouteTest should leave Status.TunnelCNAME populated")
	expectedChainContent := parentTunnel.Status.TunnelCNAME

	// HTTPRoute attached to the tunnel-targeted Gateway in the same namespace.
	// (Cross-namespace ParentRef is exercised by the unit-tests; here we keep
	// the route co-located so the test reads cleanly.)
	nsRef := gwv1.Namespace(f.ns)
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name: "r", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationZoneRef: "example-com",
			},
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

	// Chain CNAME: notes.example.com → <tunnel CNAME> (Spec.Content is *string).
	require.Eventually(t, func() bool {
		var list v2alpha1.CloudflareDNSRecordList
		if err := f.c.List(ctx, &list, client.InNamespace(f.ns)); err != nil {
			return false
		}
		for _, dr := range list.Items {
			if dr.Spec.Type == "CNAME" && dr.Spec.Name == "notes.example.com" &&
				dr.Spec.Content != nil && *dr.Spec.Content == expectedChainContent {
				return true
			}
		}
		return false
	}, 15*time.Second, 250*time.Millisecond, "chain DNSRecord notes.example.com → %q emitted", expectedChainContent)

	// Nudge the tunnel CR so the tunnel reconciler re-reads the cache. The
	// HTTPRoute source writes to the cache but does NOT touch the tunnel CR;
	// production picks up the new contributions on the 30-min requeue or on
	// the next tunnel-CR event. We trigger one via a no-op annotation update.
	var tn v2alpha1.CloudflareTunnel
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: f.ns + "-edge"}, &tn))
	require.NotEmpty(t, tn.Status.TunnelID)
	if tn.Annotations == nil {
		tn.Annotations = map[string]string{}
	}
	tn.Annotations["test.cloudflare.io/nudge"] = "1"
	require.NoError(t, f.c.Update(ctx, &tn))

	require.Eventually(t, func() bool {
		cfg, err := f.mock.Tunnel.GetConfiguration(ctx, "acct-1", tn.Status.TunnelID)
		if err != nil {
			return false
		}
		for _, e := range cfg.Config.Ingress {
			if e.Hostname == "notes.example.com" {
				return true
			}
		}
		return false
	}, 15*time.Second, 250*time.Millisecond, "notes.example.com appears in cloudflared ingress config")
}

// TestHTTPRouteSourceEnvtest_FilterRejected_AcceptedFalse covers design §12.8:
// an HTTPRoute whose only rule carries a RequestRedirect filter (which
// cloudflared cannot enforce) is rejected with parent-status Accepted=False,
// Reason=IncompatibleFilters. Other parents are untouched (single-parent here;
// multi-parent preservation is covered by the unit tests).
func TestHTTPRouteSourceEnvtest_FilterRejected_AcceptedFalse(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupHTTPRouteEnv(t)
	ctx := context.Background()

	// Zone CR + zone-ref annotation: the HTTPRoute reconciler emits a chain
	// CNAME per rt.Spec.Hostnames BEFORE writing parent-status, regardless of
	// whether rules were filter-rejected. Without a valid zoneRef the
	// DNSRecord create fails and the reconcile errors before writeParentStatus.
	zone := &v2alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: f.ns},
		Spec: v2alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, f.c.Create(ctx, zone))

	createGatewayForRouteTest(t, f, "")

	nsRef := gwv1.Namespace(f.ns)
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name: "r", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationZoneRef: "example-com",
			},
		},
		Spec: gwv1.HTTPRouteSpec{
			Hostnames: []gwv1.Hostname{"x.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: &nsRef}},
			},
			Rules: []gwv1.HTTPRouteRule{{
				// CEL "requestRedirect must be specified for RequestRedirect
				// filter.type" requires a non-nil block — empty satisfies
				// the structural requirement. Unit tests skip this because
				// the fake client bypasses CRD validation.
				Filters: []gwv1.HTTPRouteFilter{{
					Type:            gwv1.HTTPRouteFilterRequestRedirect,
					RequestRedirect: &gwv1.HTTPRequestRedirectFilter{},
				}},
			}},
		},
	}
	require.NoError(t, f.c.Create(ctx, rt))

	// Parent-status entry: Accepted=False, Reason=IncompatibleFilters.
	require.Eventually(t, func() bool {
		var got gwv1.HTTPRoute
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: "r"}, &got); err != nil {
			return false
		}
		for _, p := range got.Status.Parents {
			for _, cond := range p.Conditions {
				if cond.Type == string(gwv1.RouteConditionAccepted) &&
					cond.Reason == conventions.ReasonIncompatibleFilters {
					return true
				}
			}
		}
		return false
	}, 15*time.Second, 250*time.Millisecond, "parent-status carries Accepted=False/IncompatibleFilters")
}

// TestHTTPRouteSourceEnvtest_AttachedWithGatewayApexOverride_EmitsChainToApex
// covers the override branch of chainContentFor: when the parent Gateway
// carries a valid cloudflare.io/gateway-apex annotation, the chain CNAME's
// content is the override hostname verbatim (decoupling the route's per-
// hostname record from the tunnel's own CNAME). Sibling to the default-path
// test above, which covers the no-override / concrete-listener branch.
func TestHTTPRouteSourceEnvtest_AttachedWithGatewayApexOverride_EmitsChainToApex(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupHTTPRouteEnv(t)
	ctx := context.Background()

	zone := &v2alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: f.ns},
		Spec: v2alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, f.c.Create(ctx, zone))

	const apex = "apex.example.com"
	createGatewayForRouteTest(t, f, apex)

	nsRef := gwv1.Namespace(f.ns)
	rt := &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name: "r", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationZoneRef: "example-com",
			},
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

	// Chain CNAME: notes.example.com → apex.example.com (the override wins).
	require.Eventually(t, func() bool {
		var list v2alpha1.CloudflareDNSRecordList
		if err := f.c.List(ctx, &list, client.InNamespace(f.ns)); err != nil {
			return false
		}
		for _, dr := range list.Items {
			if dr.Spec.Type == "CNAME" && dr.Spec.Name == "notes.example.com" &&
				dr.Spec.Content != nil && *dr.Spec.Content == apex {
				return true
			}
		}
		return false
	}, 15*time.Second, 250*time.Millisecond, "chain DNSRecord notes.example.com → %q emitted", apex)
}
