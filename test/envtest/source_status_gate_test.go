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

// Envtest coverage for simplify finding F: the DeepEqual gate in
// HTTPRouteSourceReconciler.writeParentStatus and
// TLSRouteSourceReconciler.writeParentStatus.
//
// Before the fix: writeParentStatus called r.Status().Update unconditionally
// on every reconcile even when no condition changed. A single Gateway edit
// fans out to N attached routes × 1 apiserver round-trip each.
//
// After the fix: if the to-be-written conditions are semantically equivalent
// to the conditions already present in the live Route's parent entry (i.e.
// same Status/Reason/Message/ObservedGeneration per type, ignoring
// LastTransitionTime), the Update is skipped entirely.
//
// Non-vacuity guarantee: the test forces TWO reconcile passes for each source
// controller via a no-op annotation Patch on the Route. The test counts
// Status().Update calls via a countingStatusClient wrapper around the
// controller-runtime delegating client. After conditions are stable (pass 1),
// the counter is reset, then a second reconcile is triggered (pass 2). The
// assertion is that the counter does NOT increment during pass 2.
//
// RED→GREEN: at RED the counter increments on pass 2 (no gate). At GREEN the
// gate suppresses the update and the counter stays at 0.

import (
	"context"
	"sync/atomic"
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
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	mockcf "github.com/jacaudi/cloudflare-operator/internal/cloudflare/mock"
	"github.com/jacaudi/cloudflare-operator/internal/controller/tunnel"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

// countingStatusWriter implements client.StatusWriter and counts Update calls.
// It wraps the delegate StatusWriter and increments count on each Update call.
type countingStatusWriter struct {
	delegate client.StatusWriter
	count    *atomic.Int64
}

func (c *countingStatusWriter) Create(ctx context.Context, obj client.Object, sub client.Object, opts ...client.SubResourceCreateOption) error {
	return c.delegate.Create(ctx, obj, sub, opts...)
}

func (c *countingStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	c.count.Add(1)
	return c.delegate.Update(ctx, obj, opts...)
}

func (c *countingStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	return c.delegate.Patch(ctx, obj, patch, opts...)
}

// countingClient wraps a controller-runtime client to count Status.Update calls.
// The counter is accessible via statusUpdateCount / resetCount.
type countingClient struct {
	client.Client
	count *atomic.Int64
}

func newCountingClient(base client.Client) *countingClient {
	return &countingClient{Client: base, count: new(atomic.Int64)}
}

// Status returns a StatusWriter whose Update increments the counter.
func (c *countingClient) Status() client.StatusWriter {
	return &countingStatusWriter{
		delegate: c.Client.Status(),
		count:    c.count,
	}
}

// resetCount atomically resets the call counter to 0.
func (c *countingClient) resetCount() { c.count.Store(0) }

// statusUpdateCount returns the current Status.Update call count.
func (c *countingClient) statusUpdateCount() int64 { return c.count.Load() }

// statusGateFixture holds the wiring for both HTTPRoute and TLSRoute gate
// tests. Both share identical fixture topology (Gateway + backing Service +
// Tunnel CR with populated TunnelCNAME) so each test exercises only its
// target route type.
type statusGateFixture struct {
	c           client.Client
	countClient *countingClient
	mock        *mockcf.Mock
	ns          string
}

// setupStatusGateHTTPEnv wires HTTPRouteSourceReconciler + GatewaySourceReconciler
// + CloudflareTunnelReconciler for the HTTPRoute gate test. The tunnel
// reconciler is needed to populate Status.TunnelCNAME so the HTTPRoute source
// can advance past its deferred-emission guard on the first reconcile.
//
// The HTTPRouteSourceReconciler is given a countingClient so its Status.Update
// calls are observable from the test.
func setupStatusGateHTTPEnv(t *testing.T) *statusGateFixture {
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

	// CloudflareTunnel reconciler — populates Status.TunnelCNAME.
	tunnelR := &tunnel.CloudflareTunnelReconciler{
		Client:   mgr.GetClient(),
		Scheme:   sch,
		Recorder: mgr.GetEventRecorderFor("cloudflare-operator-tunnel-gate-http-test"),
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

	gwR := &tunnel.GatewaySourceReconciler{
		Client:   mgr.GetClient(),
		Scheme:   sch,
		Cache:    cache,
		Recorder: mgr.GetEventRecorderFor("cloudflare-operator-gw-source-gate-http-test"),
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

	// HTTPRouteSourceReconciler — the unit under test — gets the countingClient.
	cc := newCountingClient(mgr.GetClient())
	rtR := &tunnel.HTTPRouteSourceReconciler{
		Client:   cc,
		Scheme:   sch,
		Cache:    cache,
		Recorder: mgr.GetEventRecorderFor("cloudflare-operator-httproute-source-gate-test"),
	}
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

	return &statusGateFixture{c: mgr.GetClient(), countClient: cc, mock: m, ns: ns}
}

// setupStatusGateTLSEnv mirrors setupStatusGateHTTPEnv for TLSRoute.
func setupStatusGateTLSEnv(t *testing.T) *statusGateFixture {
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

	// CloudflareTunnel reconciler.
	tunnelR := &tunnel.CloudflareTunnelReconciler{
		Client:   mgr.GetClient(),
		Scheme:   sch,
		Recorder: mgr.GetEventRecorderFor("cloudflare-operator-tunnel-gate-tls-test"),
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

	gwR := &tunnel.GatewaySourceReconciler{
		Client:   mgr.GetClient(),
		Scheme:   sch,
		Cache:    cache,
		Recorder: mgr.GetEventRecorderFor("cloudflare-operator-gw-source-gate-tls-test"),
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

	// TLSRouteSourceReconciler — the unit under test — gets the countingClient.
	cc := newCountingClient(mgr.GetClient())
	rtR := &tunnel.TLSRouteSourceReconciler{
		Client:   cc,
		Scheme:   sch,
		Cache:    cache,
		Recorder: mgr.GetEventRecorderFor("cloudflare-operator-tlsroute-source-gate-test"),
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

	return &statusGateFixture{c: mgr.GetClient(), countClient: cc, mock: m, ns: ns}
}

// createGatewayForGateTest sets up the backing Service + tunnel-targeted
// Gateway and waits for the tunnel CR to reach Status.TunnelCNAME populated.
// Shared by both HTTPRoute and TLSRoute gate tests.
func createGatewayForGateTest(t *testing.T, f *statusGateFixture, listenerProto gwv1.ProtocolType, port int32) {
	t.Helper()
	ctx := context.Background()

	gwSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-svc", Namespace: f.ns},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: port}},
		},
	}
	require.NoError(t, f.c.Create(ctx, gwSvc))

	h := gwv1.Hostname("gate.example.com")
	gw := &gwv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gw", Namespace: f.ns,
			Annotations: map[string]string{
				conventions.AnnotationTunnel:         "true",
				conventions.AnnotationTunnelName:     "edge",
				conventions.AnnotationGatewayService: f.ns + "/gw-svc",
			},
		},
		Spec: gwv1.GatewaySpec{
			GatewayClassName: "any-class",
			Listeners: []gwv1.Listener{{
				Name: "l", Hostname: &h, Port: gwv1.PortNumber(port), Protocol: listenerProto,
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

// TestEnvtest_SourceStatus_Gate_HTTPRoute asserts that after conditions
// stabilise on the first reconcile, a second reconcile pass triggered by a
// no-op annotation patch does NOT call Status().Update (verified via
// countingClient counter).
//
// RED: the counter increments on pass 2 (no gate in writeParentStatus).
// GREEN: the conditionsEquivalent gate suppresses the Update; counter stays 0.
func TestEnvtest_SourceStatus_Gate_HTTPRoute(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupStatusGateHTTPEnv(t)
	ctx := context.Background()

	// CloudflareZone required for DNSRecord admission (zoneRef).
	zone := &v2alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: f.ns},
		Spec: v2alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, f.c.Create(ctx, zone))

	createGatewayForGateTest(t, f, gwv1.HTTPProtocolType, 80)

	// HTTPRoute attached to the tunnel-targeted Gateway.
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

	// Pass 1: wait for conditions to be written. The reconciler must have run
	// at least once and written the Accepted condition on the parent entry.
	require.Eventually(t, func() bool {
		var got gwv1.HTTPRoute
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: "r"}, &got); err != nil {
			return false
		}
		for _, p := range got.Status.Parents {
			for _, cond := range p.Conditions {
				if cond.Type == conventions.ConditionTypeAccepted {
					return true
				}
			}
		}
		return false
	}, 20*time.Second, 250*time.Millisecond, "first reconcile: Accepted condition written on HTTPRoute")

	// Quiesce: give the controller time to settle so in-flight reconciles
	// triggered by the Create event or the Status.Update observation fully
	// complete before we reset the counter.
	time.Sleep(1 * time.Second)

	// Reset the Status.Update counter. Any call after this point is pass-2.
	f.countClient.resetCount()

	// Pass 2: trigger a second reconcile by patching a no-op annotation on the
	// Route. This directly enqueues the HTTPRoute without changing any field
	// that would affect the computed conditions — the gate should suppress the
	// Status.Update.
	var rtLive gwv1.HTTPRoute
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: "r"}, &rtLive))
	patch := client.MergeFrom(rtLive.DeepCopy())
	if rtLive.Annotations == nil {
		rtLive.Annotations = map[string]string{}
	}
	rtLive.Annotations["test.cloudflare.io/reconcile-probe"] = "1"
	require.NoError(t, f.c.Patch(ctx, &rtLive, patch))

	// Wait long enough for the second reconcile to run.
	// The controller processes the enqueued event and calls writeParentStatus.
	// Without the gate: Status.Update fires → counter increments.
	// With the gate:    Status.Update is skipped → counter stays 0.
	time.Sleep(3 * time.Second)

	updatesAfterPass2 := f.countClient.statusUpdateCount()
	require.Equal(t, int64(0), updatesAfterPass2,
		"Status().Update must NOT be called on pass 2 when conditions are unchanged "+
			"(conditionsEquivalent gate should suppress the write); count=%d", updatesAfterPass2)
}

// TestEnvtest_SourceStatus_Gate_TLSRoute mirrors
// TestEnvtest_SourceStatus_Gate_HTTPRoute for the TLSRoute source controller.
func TestEnvtest_SourceStatus_Gate_TLSRoute(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupStatusGateTLSEnv(t)
	ctx := context.Background()

	// CloudflareZone required for DNSRecord admission.
	zone := &v2alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example-com", Namespace: f.ns},
		Spec: v2alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			Type:           "full",
			DeletionPolicy: "Retain",
		},
	}
	require.NoError(t, f.c.Create(ctx, zone))

	createGatewayForGateTest(t, f, gwv1.TLSProtocolType, 443)

	// TLSRoute attached to the tunnel-targeted Gateway.
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

	// Pass 1: wait for conditions to be written.
	require.Eventually(t, func() bool {
		var got gwv1a2.TLSRoute
		if err := f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: "r"}, &got); err != nil {
			return false
		}
		for _, p := range got.Status.Parents {
			for _, cond := range p.Conditions {
				if cond.Type == conventions.ConditionTypeAccepted {
					return true
				}
			}
		}
		return false
	}, 20*time.Second, 250*time.Millisecond, "first reconcile: Accepted condition written on TLSRoute")

	// Quiesce.
	time.Sleep(1 * time.Second)

	// Reset the Status.Update counter.
	f.countClient.resetCount()

	// Pass 2: trigger a second reconcile via a no-op annotation patch on the Route.
	var rtLive gwv1a2.TLSRoute
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: "r"}, &rtLive))
	patch := client.MergeFrom(rtLive.DeepCopy())
	if rtLive.Annotations == nil {
		rtLive.Annotations = map[string]string{}
	}
	rtLive.Annotations["test.cloudflare.io/reconcile-probe"] = "1"
	require.NoError(t, f.c.Patch(ctx, &rtLive, patch))

	// Allow time for the second reconcile to run.
	time.Sleep(3 * time.Second)

	updatesAfterPass2 := f.countClient.statusUpdateCount()
	require.Equal(t, int64(0), updatesAfterPass2,
		"Status().Update must NOT be called on pass 2 when conditions are unchanged "+
			"(conditionsEquivalent gate should suppress the write); count=%d", updatesAfterPass2)
}
