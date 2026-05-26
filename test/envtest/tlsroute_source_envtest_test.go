/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package envtest_test

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
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	mockcf "github.com/jacaudi/cloudflare-operator/internal/cloudflare/mock"
	"github.com/jacaudi/cloudflare-operator/internal/controller/tunnel"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

// tlsRouteEnvFixture wires TLSRouteSourceReconciler + GatewaySourceReconciler
// + CloudflareTunnelReconciler inline against a shared tunnelsynth.Cache.
// Mirrors httpRouteEnvFixture (see httproute_source_envtest_test.go): the
// Gateway source is needed so the parent Gateway's tunnel CR auto-creates
// (TLSRoute source never auto-creates tunnels per design §4.3 + §4.2), and
// the tunnel reconciler populates Status.TunnelCNAME so the TLSRoute emission
// can advance past its deferred-emission guard.
type tlsRouteEnvFixture struct {
	c    client.Client
	mock *mockcf.Mock
	ns   string
}

func setupTLSRouteEnv(t *testing.T) *tlsRouteEnvFixture {
	t.Helper()

	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	sch := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(sch))
	utilruntime.Must(v2alpha1.AddToScheme(sch))
	utilruntime.Must(gwv1.Install(sch))
	utilruntime.Must(gwv1a2.Install(sch))

	mgr, err := ctrl.NewManager(sharedConfig, ctrl.Options{
		Scheme:  sch,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	require.NoError(t, err)

	m := mockcf.New()
	cache := tunnelsynth.NewCache()

	// CloudflareTunnel reconciler — drives the dataplane side so the parent
	// Gateway's tunnel CR reaches Status.TunnelCNAME populated.
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

	// GatewaySource — auto-creates the parent Gateway's tunnel CR.
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

	// TLSRouteSource — the unit under test. Mirrors setup.go's wiring:
	// watches TLSRoute (primary) + Gateway (parent change). The production
	// setup also wires CloudflareTunnel → TLSRoutes for deferred-emission
	// retrigger; the envtest skips that watch because the fixture waits for
	// Status.TunnelCNAME populated BEFORE creating the TLSRoute, so the
	// deferred-emission path is never taken. Keeps LOC budget honest without
	// losing coverage of the TLSRoute source's own state machine.
	rtR := &tunnel.TLSRouteSourceReconciler{
		Client:   mgr.GetClient(),
		Scheme:   sch,
		Cache:    cache,
		Recorder: mgr.GetEventRecorderFor("cloudflare-operator-tlsroute-source-test"),
	}
	require.NoError(t, ctrl.NewControllerManagedBy(mgr).
		Named("tlsroutesource-"+sanitizeTestName(t.Name())).
		For(&gwv1a2.TLSRoute{}).
		Owns(&v2alpha1.CloudflareDNSRecord{}).
		Watches(&gwv1.Gateway{},
			handler.EnqueueRequestsFromMapFunc(gatewayToTLSRoutesTestMapFunc(mgr))).
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

	return &tlsRouteEnvFixture{c: mgr.GetClient(), mock: m, ns: ns}
}

// gatewayToTLSRoutesTestMapFunc mirrors setup.go::gatewayToTLSRoutes — list
// every TLSRoute, enqueue those whose parentRefs include the changed Gateway.
// Identical structure to gatewayToHTTPRoutesTestMapFunc but for v1alpha2.
func gatewayToTLSRoutesTestMapFunc(mgr ctrl.Manager) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		gw, ok := obj.(*gwv1.Gateway)
		if !ok {
			return nil
		}
		var routes gwv1a2.TLSRouteList
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

// createTLSGatewayForRouteTest stands up a backing Service + tunnel-targeted
// Gateway with a TLS listener in the fixture namespace, then waits for the
// auto-created tunnel CR to populate Status.TunnelCNAME — so the TLSRoute
// attached afterwards sees a ready tunnel and can emit on the first reconcile.
//
// Gateway-service discovery is annotation-only (cloudflare.io/gateway-service)
// per the design lock — no label fallback. The plan literal's label-based
// fallback example predates the design lock; see the tlsroute reconciler
// docstring (lines 67–68) for the authoritative contract.
//
// gatewayApex sets the cloudflare.io/gateway-apex annotation when non-empty,
// flipping chainContentFor (apex.go) onto its override branch so emitted
// chain CNAMEs anchor at the supplied apex instead of the tunnel's own CNAME.
// Pass "" to exercise the default concrete-listener-no-override behavior.
func createTLSGatewayForRouteTest(t *testing.T, f *tlsRouteEnvFixture, gatewayApex string) {
	t.Helper()
	ctx := context.Background()

	gwSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-svc", Namespace: f.ns},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: 443}},
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
			// GatewayClassName is a required string but envtest's apiserver
			// only enforces type/length; the source reconciler ignores it.
			GatewayClassName: "any-class",
			Listeners: []gwv1.Listener{{
				Name: "tls", Hostname: &h, Port: 443, Protocol: gwv1.TLSProtocolType,
				TLS: tlsPassthroughConfig(),
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

// TestTLSRouteSourceEnvtest_AttachedEmitsTCPIngressAndClientSideClientRequired
// covers design §4.3 + §12 acceptance: a TLSRoute (v1alpha2) attached via
// parentRefs to a tunnel-targeted Gateway emits a tcp:// ingress entry into
// the cloudflared configuration AND surfaces the always-stamped
// ClientSideClientRequired status condition on the tunnel-targeted parent
// (TLSRoute hostnames terminate outside the browser model — reachable only
// via `cloudflared access tcp` or WARP).
func TestTLSRouteSourceEnvtest_AttachedEmitsTCPIngressAndClientSideClientRequired(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupTLSRouteEnv(t)
	ctx := context.Background()

	// CloudflareZone for example.com — the emitted DNSRecord uses spec.zoneRef
	// per design §14 (tunnel-emitted CRs never set spec.zoneID directly).
	// Without it the DNSRecord CRD admission rejects with 422.
	zone := &v2alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: f.ns},
		Spec: v2alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, f.c.Create(ctx, zone))

	createTLSGatewayForRouteTest(t, f, "")

	// Capture the parent tunnel's CNAME for the chain-content assertion below.
	// For Gateways without an explicit cloudflare.io/gateway-apex annotation,
	// chainContentFor (apex.go) returns tn.Status.TunnelCNAME directly — the
	// route's per-hostname CNAME hops straight to the tunnel, skipping any
	// implicit "listener hostname is the apex" interpretation. The
	// gateway-apex override path is covered by a sibling test.
	var parentTunnel v2alpha1.CloudflareTunnel
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: f.ns + "-edge"}, &parentTunnel))
	require.NotEmpty(t, parentTunnel.Status.TunnelCNAME,
		"createTLSGatewayForRouteTest should leave Status.TunnelCNAME populated")
	expectedChainContent := parentTunnel.Status.TunnelCNAME

	// TLSRoute attached to the tunnel-targeted Gateway in the same namespace.
	// Cross-namespace ParentRef is exercised by the unit tests; here we keep
	// the route co-located so the test reads cleanly.
	nsRef := gwv1.Namespace(f.ns)
	rt := &gwv1a2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name: "r", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationZoneRef: "example-com",
			},
		},
		Spec: gwv1a2.TLSRouteSpec{
			Hostnames: []gwv1.Hostname{"tls.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: &nsRef}},
			},
			// TLSRoute CRD admission requires spec.rules to be present. The
			// translator ignores rule bodies (TLSRoute hostname matching is
			// SNI-only — there are no per-rule filters cloudflared can
			// enforce), so an empty rule satisfies the structural requirement.
			Rules: []gwv1a2.TLSRouteRule{{}},
		},
	}
	require.NoError(t, f.c.Create(ctx, rt))

	// Chain CNAME: tls.example.com → <tunnel CNAME>.
	require.Eventually(t, func() bool {
		var list v2alpha1.CloudflareDNSRecordList
		if err := f.c.List(ctx, &list, client.InNamespace(f.ns)); err != nil {
			return false
		}
		for _, dr := range list.Items {
			if dr.Spec.Type == "CNAME" && dr.Spec.Name == "tls.example.com" &&
				dr.Spec.Content != nil && *dr.Spec.Content == expectedChainContent {
				return true
			}
		}
		return false
	}, 15*time.Second, 250*time.Millisecond, "chain DNSRecord tls.example.com → %q emitted", expectedChainContent)

	// Parent-status: Accepted=True/TunnelAttached AND PartiallyInvalid=True/
	// ClientSideClientRequired (the warning is ALWAYS stamped for TLSRoute).
	require.Eventually(t, func() bool {
		var got gwv1a2.TLSRoute
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: "r"}, &got); err != nil {
			return false
		}
		var sawAccepted, sawClientSide bool
		for _, p := range got.Status.Parents {
			for _, cond := range p.Conditions {
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
		}
		return sawAccepted && sawClientSide
	}, 15*time.Second, 250*time.Millisecond,
		"parent-status carries Accepted=True/TunnelAttached AND PartiallyInvalid=True/ClientSideClientRequired")

	// Nudge the tunnel CR so the tunnel reconciler re-reads the contributions
	// cache and PUTs an updated configuration. The TLSRoute source writes to
	// the cache but does NOT touch the tunnel CR; in production this happens
	// on the 30-min requeue or the next tunnel-CR event. Mirrors the
	// HTTPRoute envtest's nudge pattern.
	var tn v2alpha1.CloudflareTunnel
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: f.ns + "-edge"}, &tn))
	require.NotEmpty(t, tn.Status.TunnelID)
	if tn.Annotations == nil {
		tn.Annotations = map[string]string{}
	}
	tn.Annotations["test.cloudflare.io/nudge"] = "1"
	require.NoError(t, f.c.Update(ctx, &tn))

	// tcp:// ingress entry for tls.example.com — the §12 acceptance bit.
	require.Eventually(t, func() bool {
		cfg, err := f.mock.Tunnel.GetConfiguration(ctx, "acct-1", tn.Status.TunnelID)
		if err != nil {
			return false
		}
		for _, e := range cfg.Config.Ingress {
			if e.Hostname == "tls.example.com" && strings.HasPrefix(e.Service, "tcp://") {
				return true
			}
		}
		return false
	}, 15*time.Second, 250*time.Millisecond,
		"tls.example.com appears in cloudflared ingress with tcp:// service URL")
}

// TestTLSRouteSourceEnvtest_AttachedWithGatewayApexOverride_EmitsChainToApex
// covers the override branch of chainContentFor for TLSRoute: when the parent
// Gateway carries a valid cloudflare.io/gateway-apex annotation, the chain
// CNAME's content is the override hostname verbatim. Sibling to the default-
// path test above, which covers the no-override / concrete-listener branch.
func TestTLSRouteSourceEnvtest_AttachedWithGatewayApexOverride_EmitsChainToApex(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupTLSRouteEnv(t)
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
	createTLSGatewayForRouteTest(t, f, apex)

	nsRef := gwv1.Namespace(f.ns)
	rt := &gwv1a2.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name: "r", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationZoneRef: "example-com",
			},
		},
		Spec: gwv1a2.TLSRouteSpec{
			Hostnames: []gwv1.Hostname{"tls.example.com"},
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: "gw", Namespace: &nsRef}},
			},
			Rules: []gwv1a2.TLSRouteRule{{}},
		},
	}
	require.NoError(t, f.c.Create(ctx, rt))

	// Chain CNAME: tls.example.com → apex.example.com (the override wins).
	require.Eventually(t, func() bool {
		var list v2alpha1.CloudflareDNSRecordList
		if err := f.c.List(ctx, &list, client.InNamespace(f.ns)); err != nil {
			return false
		}
		for _, dr := range list.Items {
			if dr.Spec.Type == "CNAME" && dr.Spec.Name == "tls.example.com" &&
				dr.Spec.Content != nil && *dr.Spec.Content == apex {
				return true
			}
		}
		return false
	}, 15*time.Second, 250*time.Millisecond, "chain DNSRecord tls.example.com → %q emitted", apex)
}
