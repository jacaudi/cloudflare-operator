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

// gatewayEnvFixture wires the GatewaySourceReconciler + CloudflareTunnel
// reconciler inline, sharing one tunnelsynth.Cache. Mirrors serviceEnvFixture:
// the source defers DNSRecord emission until the tunnel's Status.TunnelCNAME
// populates; a small inline MapFunc retriggers Gateway reconciles when the
// tunnel's status updates.
type gatewayEnvFixture struct {
	c    client.Client
	mock *mockcf.Mock
	ns   string
}

// setupGatewayEnv builds a per-test manager backed by the package-shared
// envtest config. nsName="" picks a short unique namespace.
func setupGatewayEnv(t *testing.T, nsName string) *gatewayEnvFixture {
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

	m := mockcf.New()
	cache := tunnelsynth.NewCache()

	// CloudflareTunnel reconciler — populates Status.TunnelCNAME so the
	// Gateway source's DNSRecord emission can advance.
	tunnelR := &tunnel.CloudflareTunnelReconciler{
		Client:   mgr.GetClient(),
		Scheme:   sch,
		Recorder: mgr.GetEventRecorderFor("cloudflare-operator-tunnel-gw-test"),
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

	// GatewaySource reconciler. The inline Watch on CloudflareTunnel retriggers
	// attached Gateways when the tunnel's Status.TunnelCNAME flips from empty →
	// populated (deferred-emission retrigger). Mirrors setup.go::tunnelToGateways.
	gwR := &tunnel.GatewaySourceReconciler{
		Client:   mgr.GetClient(),
		Scheme:   sch,
		Cache:    cache,
		Recorder: mgr.GetEventRecorderFor("cloudflare-operator-gw-source-test"),
		DefaultConnector: v2alpha1.ConnectorSpec{
			Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30,
		},
	}
	require.NoError(t, ctrl.NewControllerManagedBy(mgr).
		Named("gatewaysource-"+sanitizeTestName(t.Name())).
		For(&gwv1.Gateway{}).
		Owns(&v2alpha1.CloudflareDNSRecord{}).
		Watches(&v2alpha1.CloudflareTunnel{},
			handler.EnqueueRequestsFromMapFunc(tunnelToGatewaysTestMapFunc(mgr))).
		Complete(gwR))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	startManager(t, ctx, mgr)

	syncCtx, syncCancel := context.WithTimeout(ctx, 30*time.Second)
	defer syncCancel()
	require.True(t, mgr.GetCache().WaitForCacheSync(syncCtx), "manager cache failed to sync")

	if nsName == "" {
		nsName = shortUniqueNamespace(t)
	}
	require.NoError(t, mgr.GetClient().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: nsName},
	}))

	return &gatewayEnvFixture{c: mgr.GetClient(), mock: m, ns: nsName}
}

// tunnelToGatewaysTestMapFunc mirrors setup.go::tunnelToGateways: enqueues
// every annotated Gateway in the tunnel's namespace on a tunnel event.
// Re-implemented inline so this envtest doesn't depend on setup.go's
// SetupTunnelControllers (which wires every source reconciler).
func tunnelToGatewaysTestMapFunc(mgr ctrl.Manager) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		tn, ok := obj.(*v2alpha1.CloudflareTunnel)
		if !ok {
			return nil
		}
		var gws gwv1.GatewayList
		if err := mgr.GetClient().List(ctx, &gws, client.InNamespace(tn.Namespace)); err != nil {
			return nil
		}
		out := make([]reconcile.Request, 0, len(gws.Items))
		for _, gw := range gws.Items {
			if gw.Annotations[conventions.AnnotationTunnel] == "true" {
				out = append(out, reconcile.Request{
					NamespacedName: types.NamespacedName{Namespace: gw.Namespace, Name: gw.Name},
				})
			}
		}
		return out
	}
}

// TestGatewaySourceEnvtest_ListenerProducesApexCNAME covers design §12.4: a
// Gateway annotated for tunnel with a listener hostname auto-creates a
// CloudflareTunnel CR (derived name cf-<ns>-<tunnel-name>) and emits a
// CloudflareDNSRecord CNAME mapping the listener hostname to the tunnel CNAME.
// Exercises the full deferred-emission flow on the Gateway side: source
// caches contrib → tunnel reconciler creates Cloudflare-side tunnel + populates
// Status.TunnelCNAME → inline tunnelToGateways watch retriggers source →
// DNSRecord emitted.
func TestGatewaySourceEnvtest_ListenerProducesApexCNAME(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupGatewayEnv(t, "")
	ctx := context.Background()

	// CloudflareZone CR for example.com — the emitted CloudflareDNSRecord uses
	// spec.zoneRef (per design §14: tunnel-emitted CRs never set spec.zoneID
	// directly). Without zoneRef the DNSRecord CRD admission rejects with 422.
	zone := &v2alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: f.ns},
		Spec: v2alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, f.c.Create(ctx, zone))

	// Backing Service the Gateway annotation points at — cloudflared connects
	// to this Service's first port. The reconciler refuses synthesis if the
	// referenced Service is missing.
	gwSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-svc", Namespace: f.ns},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	require.NoError(t, f.c.Create(ctx, gwSvc))

	h := gwv1.Hostname("ext.example.com")
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gw", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:         "true",
				conventions.AnnotationTunnelName:     "edge",
				conventions.AnnotationGatewayService: f.ns + "/gw-svc",
				conventions.AnnotationZoneRef:        "example-com",
			},
		},
		Spec: gwv1.GatewaySpec{
			// GatewayClassName is a required string field; envtest's apiserver
			// only validates type/length, not that a matching GatewayClass
			// exists. The source reconciler ignores this field entirely.
			GatewayClassName: "any-class",
			Listeners: []gwv1.Listener{{
				Name: "h", Hostname: &h, Port: 80, Protocol: gwv1.HTTPProtocolType,
			}},
		},
	}
	require.NoError(t, f.c.Create(ctx, gw))

	expectedTunnel := f.ns + "-edge"

	// Auto-created CloudflareTunnel CR with the derived name.
	require.Eventually(t, func() bool {
		var tn v2alpha1.CloudflareTunnel
		return f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: expectedTunnel}, &tn) == nil
	}, 15*time.Second, 250*time.Millisecond, "CloudflareTunnel %q created", expectedTunnel)

	// Wait for tunnel status to populate so the deferred emission can advance.
	require.Eventually(t, func() bool {
		var tn v2alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: expectedTunnel}, &tn); err != nil {
			return false
		}
		return tn.Status.TunnelCNAME != ""
	}, 15*time.Second, 250*time.Millisecond, "tunnel Status.TunnelCNAME populated")

	// Dog-fooded CloudflareDNSRecord emitted once Status.TunnelCNAME populates.
	// CNAME ext.example.com → <tunnel-CNAME> (the apex-of-the-chain hop; see
	// design §4.2 and gateway_source_controller.go::emitDNSRecord).
	require.Eventually(t, func() bool {
		var list v2alpha1.CloudflareDNSRecordList
		if err := f.c.List(ctx, &list, client.InNamespace(f.ns)); err != nil {
			return false
		}
		for _, r := range list.Items {
			if r.Spec.Type == "CNAME" && r.Spec.Name == "ext.example.com" &&
				r.Spec.Content != nil && *r.Spec.Content != "" {
				return true
			}
		}
		return false
	}, 15*time.Second, 250*time.Millisecond, "CloudflareDNSRecord for ext.example.com emitted with non-empty content")
}
