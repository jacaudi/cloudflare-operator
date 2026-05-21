/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package tunnel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	cloudflare "github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	mockcf "github.com/jacaudi/cloudflare-operator/internal/cloudflare/mock"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	reconcilelib "github.com/jacaudi/cloudflare-operator/internal/reconcile"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

func tunnelScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, appsv1.AddToScheme(s))
	require.NoError(t, v2alpha1.AddToScheme(s))
	return s
}

func setEnvCreds(t *testing.T) {
	t.Helper()
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
}

func newTunnelReconciler(t *testing.T, c client.Client, s *runtime.Scheme, m *mockcf.Mock, cache *tunnelsynth.Cache) *CloudflareTunnelReconciler {
	t.Helper()
	return &CloudflareTunnelReconciler{
		Client: c,
		Scheme: s,
		TunnelClientFn: func(_ cloudflare.Credentials) (cloudflare.TunnelClient, error) {
			return m.Tunnel, nil
		},
		Cache:        cache,
		DefaultImage: testDefaultImage,
	}
}

func TestTunnelReconciler_CreatesTunnelAndDataplane(t *testing.T) {
	setEnvCreds(t)
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns"},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name: "ns",
			Connector: v2alpha1.ConnectorSpec{
				Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30,
			},
		},
	}
	s := tunnelScheme(t)
	base := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(tn).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).
		Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	m := mockcf.New()
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())

	// First pass adds finalizer.
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"},
	})
	require.NoError(t, err)
	// Second pass: create tunnel + dataplane.
	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"},
	})
	require.NoError(t, err)

	var got v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "tnl", Namespace: "ns"}, &got))
	require.NotEmpty(t, got.Status.TunnelID, "TunnelID must be set after create")
	require.Contains(t, got.Status.TunnelCNAME, ".cfargotunnel.com")
	require.Equal(t, got.Generation, got.Status.ObservedGeneration)
	require.NotNil(t, got.Status.LastSyncedAt)
	require.Contains(t, got.Finalizers, conventions.FinalizerName)

	var dep appsv1.Deployment
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "cloudflared-tnl", Namespace: "ns"}, &dep))

	var sec corev1.Secret
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "cloudflared-token-tnl", Namespace: "ns"}, &sec))
	require.NotEmpty(t, sec.Data["token"])

	var svc corev1.Service
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "cloudflared-tnl-metrics", Namespace: "ns"}, &svc))
}

func TestTunnelReconciler_DriftSkipsPutWhenObservedMatches(t *testing.T) {
	setEnvCreds(t)
	s := tunnelScheme(t)
	m := mockcf.New()

	// Seed mock state: create the tunnel and PUT the catch-all config once.
	created, err := m.Tunnel.CreateTunnel(context.Background(), "acct-1",
		cloudflare.CreateTunnelParams{Name: "ns"})
	require.NoError(t, err)
	_, err = m.Tunnel.PutConfiguration(context.Background(), "acct-1", created.ID,
		cloudflare.TunnelConfig{Ingress: []cloudflare.IngressEntry{{Service: "http_status:404"}}})
	require.NoError(t, err)

	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName}},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name:      "ns",
			Connector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
		Status: v2alpha1.CloudflareTunnelStatus{
			TunnelID:        created.ID,
			ObservedIngress: []v2alpha1.IngressEntrySnapshot{{Service: "http_status:404"}},
		},
	}
	base := fake.NewClientBuilder().
		WithScheme(s).WithObjects(tn).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"},
	})
	require.NoError(t, err)

	// Config version should remain at 1 (no second PUT).
	cfg, gerr := m.Tunnel.GetConfiguration(context.Background(), "acct-1", created.ID)
	require.NoError(t, gerr)
	require.Equal(t, 1, cfg.Version, "drift-skip must not bump version")
}

func TestTunnelReconciler_FinalizerDrainSequence(t *testing.T) {
	setEnvCreds(t)
	s := tunnelScheme(t)
	m := mockcf.New()
	created, err := m.Tunnel.CreateTunnel(context.Background(), "acct-1",
		cloudflare.CreateTunnelParams{Name: "ns"})
	require.NoError(t, err)

	now := metav1.Now()
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tnl", Namespace: "ns",
			Finalizers:        []string{conventions.FinalizerName},
			DeletionTimestamp: &now,
		},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name:      "ns",
			Connector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
		Status: v2alpha1.CloudflareTunnelStatus{TunnelID: created.ID},
	}
	base := fake.NewClientBuilder().
		WithScheme(s).WithObjects(tn).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())

	// Drain: with no Deployment present, the reconciler should proceed to
	// DeleteConnections, DeleteTunnel, and finalizer removal in a single pass.
	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"},
	})
	require.NoError(t, err)

	// Tunnel removed from mock.
	_, gerr := m.Tunnel.GetTunnel(context.Background(), "acct-1", created.ID)
	require.Error(t, gerr)
	require.True(t, errors.Is(gerr, cloudflare.ErrTunnelNotFound),
		"expected ErrTunnelNotFound, got %v", gerr)

	// CR removed (finalizer dropped → fake client GC).
	var got v2alpha1.CloudflareTunnel
	gerr = c.Get(context.Background(), types.NamespacedName{Name: "tnl", Namespace: "ns"}, &got)
	require.True(t, apierrors.IsNotFound(gerr), "tunnel CR must be GC'd after drain; got %v", gerr)
}

func TestTunnelReconciler_FinalizerDrain_TolerantOf404(t *testing.T) {
	// Cloudflare 404s (already-deleted state) must collapse to nil via
	// reconcile.WrapDeleteErr; otherwise the finalizer hangs forever. This
	// asserts the dual-sentinel symmetry on DeleteConnections + DeleteTunnel
	// (Phase-2 review-pattern #14).
	setEnvCreds(t)
	s := tunnelScheme(t)
	m := mockcf.New()
	now := metav1.Now()
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tnl", Namespace: "ns",
			Finalizers:        []string{conventions.FinalizerName},
			DeletionTimestamp: &now,
		},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name:      "ns",
			Connector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
		// TunnelID points at a tunnel that does NOT exist in the mock.
		Status: v2alpha1.CloudflareTunnelStatus{TunnelID: "ghost-tunnel-id"},
	}
	base := fake.NewClientBuilder().
		WithScheme(s).WithObjects(tn).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"},
	})
	require.NoError(t, err, "WrapDeleteErr must swallow 404s on delete-path")

	var got v2alpha1.CloudflareTunnel
	gerr := c.Get(context.Background(), types.NamespacedName{Name: "tnl", Namespace: "ns"}, &got)
	require.True(t, apierrors.IsNotFound(gerr), "finalizer must be removable even on already-gone tunnel")
}

func TestTunnelReconciler_StatusConditionsWrittenByOuterFunction(t *testing.T) {
	// Helpers mutate in-memory only; the OUTER Reconcile is the sole persist
	// point (review pattern #12). We verify by re-fetching after Reconcile
	// (review pattern #11) and asserting on the freshly-loaded Status.
	setEnvCreds(t)
	s := tunnelScheme(t)
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns"},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name:      "ns",
			Connector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(tn).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	m := mockcf.New()
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())

	// Two passes: first adds finalizer; second performs the main flow that
	// writes the Ready condition.
	for i := 0; i < 2; i++ {
		_, err := r.Reconcile(context.Background(), ctrl.Request{
			NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"},
		})
		require.NoError(t, err)
	}

	var got v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "tnl", Namespace: "ns"}, &got))

	// With the fake client never reporting DeploymentAvailable=True,
	// Ready=False/ConnectorDeploying but RemoteConfigApplied=True is set by
	// rollup (the Deployment-Available gate is checked before the
	// connector-count gate).
	var sawReady, sawRemoteCfg bool
	for _, cond := range got.Status.Conditions {
		if cond.Type == conventions.ConditionTypeReady {
			sawReady = true
		}
		if cond.Type == conventions.ConditionTypeRemoteConfigApplied {
			sawRemoteCfg = true
		}
	}
	require.True(t, sawReady, "outer Reconcile must persist Ready condition via Status().Update")
	require.True(t, sawRemoteCfg, "rollup must persist RemoteConfigApplied condition")
	require.NotEmpty(t, got.Status.Phase, "Phase must be derived and persisted")
}

func TestTunnelReconciler_NotReadyWhenDeploymentNotAvailable(t *testing.T) {
	// Design §8 step 9: Ready=True requires the cloudflared Deployment to be
	// Available, in addition to a healthy connector count. The fake client
	// never simulates the deployment controller, so Status.Conditions stays
	// empty and isDeploymentAvailable returns false — even with healthy
	// connectors seeded into the mock, the rollup must report
	// Ready=False/ConnectorDeploying.
	//
	// The auto-created annotation marks this as an operator-managed tunnel so
	// it enters the cascade-GC branch rather than the orphaned-unmanaged
	// branch. Without it, a tunnel with no annotation, no source labels, and
	// empty AttachedSources would be classified as user-authored and hit the
	// OrphanedUnmanaged observability path instead of the deployment-availability
	// assertion this test is verifying.
	setEnvCreds(t)
	s := tunnelScheme(t)
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "tnl",
			Namespace:   "ns",
			Annotations: map[string]string{conventions.AnnotationAutoCreated: "true"},
		},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name:      "ns",
			Connector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(tn).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	m := mockcf.New()
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())

	// First pass: finalizer.
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"},
	})
	require.NoError(t, err)

	// Seed connectors into the mock so ConnectionsHealthy > 0 after pass 2.
	// Without the Deployment-Available gate, the rollup would now report
	// Ready=True; the gate must override that and force ReasonConnectorDeploying.
	var afterPass1 v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "tnl", Namespace: "ns"}, &afterPass1))

	// Second pass: creates tunnel; status.TunnelID becomes set during this
	// reconcile, so seed connectors AFTER the first reconcile creates the
	// tunnel. Run a 2nd reconcile to make sure tunnel is created.
	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"},
	})
	require.NoError(t, err)

	var afterPass2 v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "tnl", Namespace: "ns"}, &afterPass2))
	require.NotEmpty(t, afterPass2.Status.TunnelID, "tunnel must be created by pass 2")

	// Seed connectors against the now-known tunnel ID.
	m.Tunnel.SeedConnections(afterPass2.Status.TunnelID, []cloudflare.TunnelConnection{
		{ID: "c1", ColoName: "DEN"},
		{ID: "c2", ColoName: "DEN"},
	})

	// Third pass: ConnectionsHealthy>0 but the Deployment still has no
	// DeploymentAvailable condition (fake client never runs the deployment
	// controller), so isDeploymentAvailable returns false.
	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"},
	})
	require.NoError(t, err)

	var got v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "tnl", Namespace: "ns"}, &got))
	require.Equal(t, int32(2), got.Status.ConnectionsHealthy,
		"mock must have reported the seeded connectors")

	var ready *metav1.Condition
	for i := range got.Status.Conditions {
		if got.Status.Conditions[i].Type == conventions.ConditionTypeReady {
			ready = &got.Status.Conditions[i]
		}
	}
	require.NotNil(t, ready, "Ready condition must be set")
	require.Equal(t, metav1.ConditionFalse, ready.Status,
		"Ready must be False when Deployment is not Available, even with healthy connectors")
	require.Equal(t, conventions.ReasonConnectorDeploying, ready.Reason,
		"Reason must be ConnectorDeploying when the Deployment-Available gate fails")
}

func TestTunnelReconciler_CredentialsHaltUpdatesStatus(t *testing.T) {
	// No env creds and no per-CR override → LoadCredentialsHierarchical halts
	// with CredentialsUnavailable. The reconciler must persist that reason
	// before returning the halt result.
	t.Setenv("CLOUDFLARE_API_TOKEN", "")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	s := tunnelScheme(t)
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName}},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name:      "ns",
			Connector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(tn).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	m := mockcf.New()
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"},
	})
	require.NoError(t, err)

	var got v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "tnl", Namespace: "ns"}, &got))
	var sawCredsUnavailable bool
	for _, cond := range got.Status.Conditions {
		if cond.Type == conventions.ConditionTypeReady &&
			cond.Reason == conventions.ReasonCredentialsUnavailable {
			sawCredsUnavailable = true
		}
	}
	require.True(t, sawCredsUnavailable, "credentials halt must surface as Ready=False/CredentialsUnavailable")
}

func TestTunnelReconciler_DuplicateHostname_EmitsEventOnLoser(t *testing.T) {
	// Two Services in the same namespace both claim foo.example.com. The
	// resolver picks the lex-lower source (Name="a") as winner; the loser
	// (Name="b") must receive a DuplicateHostname Warning Event on the
	// source object so users see the conflict on the resource they own.
	setEnvCreds(t)
	s := tunnelScheme(t)
	m := mockcf.New()

	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName}},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name:      "ns",
			Connector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
	}
	winnerSvc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"}}
	loserSvc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns"}}
	base := fake.NewClientBuilder().
		WithScheme(s).WithObjects(tn, winnerSvc, loserSvc).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	cache := tunnelsynth.NewCache()
	tk := tunnelsynth.TunnelKey{Namespace: "ns", Name: "tnl"}
	cache.Set(tk, tunnelsynth.SourceKey{Kind: "Service", Namespace: "ns", Name: "a"},
		[]tunnelsynth.IngressContribution{{Hostname: "foo.example.com", Service: "http://a.ns.svc.cluster.local:80"}})
	cache.Set(tk, tunnelsynth.SourceKey{Kind: "Service", Namespace: "ns", Name: "b"},
		[]tunnelsynth.IngressContribution{{Hostname: "foo.example.com", Service: "http://b.ns.svc.cluster.local:80"}})

	rec := record.NewFakeRecorder(16)
	r := newTunnelReconciler(t, c, s, m, cache)
	r.Recorder = rec

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"},
	})
	require.NoError(t, err)

	// Drain the channel and look for a DuplicateHostname Event referencing
	// the loser. Other events (TunnelCreated) are also expected — we just
	// need to see at least one DuplicateHostname.
	var sawDuplicate bool
	close(rec.Events)
	for ev := range rec.Events {
		if containsAll(ev, conventions.ReasonDuplicateHostname, "foo.example.com") {
			sawDuplicate = true
		}
	}
	require.True(t, sawDuplicate, "lex-loser Service must receive DuplicateHostname Event")
}

func TestApplyRemoteConfig_EmitsDriftDetectedWhenLiveDiffersFromObserved(t *testing.T) {
	// Out-of-band drift (Design E2): the live Cloudflare config differs from
	// what the operator last observed. applyRemoteConfig must emit a single
	// DriftDetected Warning Event for operator visibility.
	//
	// Post-G (simplify): drift detection only fires when wantSnap differs from
	// ObservedIngress (the gate that skips GetConfiguration in steady state).
	// This test seeds ObservedIngress as a stale prior snap (≠ wantSnap) so
	// the gate allows GetConfiguration through, exercising the drift path.
	setEnvCreds(t)
	s := tunnelScheme(t)
	m := mockcf.New()

	created, err := m.Tunnel.CreateTunnel(context.Background(), "acct-1",
		cloudflare.CreateTunnelParams{Name: "ns"})
	require.NoError(t, err)
	// Live config has a real ingress entry someone added via the dashboard.
	// This differs from what the operator's ObservedIngress says — triggering
	// the DriftDetected event once GetConfiguration runs.
	_, err = m.Tunnel.PutConfiguration(context.Background(), "acct-1", created.ID,
		cloudflare.TunnelConfig{Ingress: []cloudflare.IngressEntry{
			{Hostname: "rogue.example.com", Service: "http://rogue.ns.svc:80"},
		}})
	require.NoError(t, err)

	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName}},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name:      "ns",
			Connector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
		Status: v2alpha1.CloudflareTunnelStatus{
			TunnelID: created.ID,
			// Stale prior observed snap — differs from the catch-all wantSnap
			// that an empty cache resolves to. This makes wantSnap ≠
			// ObservedIngress so the new G gate allows GetConfiguration through,
			// and the rogue live entry produces a DriftDetected event.
			ObservedIngress: []v2alpha1.IngressEntrySnapshot{
				{Hostname: "old.example.com", Service: "http://old.ns.svc:80"},
			},
		},
	}
	base := fake.NewClientBuilder().
		WithScheme(s).WithObjects(tn).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	rec := record.NewFakeRecorder(16)
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())
	r.Recorder = rec

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"},
	})
	require.NoError(t, err)

	var sawDrift bool
	close(rec.Events)
	for ev := range rec.Events {
		if containsAll(ev, conventions.ReasonDriftDetected) {
			sawDrift = true
		}
	}
	require.True(t, sawDrift, "live config differs from observed → DriftDetected Warning Event expected")
}

func TestApplyRemoteConfig_NoDriftWhenLiveMatchesObserved(t *testing.T) {
	// When the live Cloudflare config matches Status.ObservedIngress (built
	// from the same projection that produced the PUT), no DriftDetected
	// Event must fire.
	setEnvCreds(t)
	s := tunnelScheme(t)
	m := mockcf.New()

	created, err := m.Tunnel.CreateTunnel(context.Background(), "acct-1",
		cloudflare.CreateTunnelParams{Name: "ns"})
	require.NoError(t, err)
	liveCfg := cloudflare.TunnelConfig{Ingress: []cloudflare.IngressEntry{{Service: "http_status:404"}}}
	_, err = m.Tunnel.PutConfiguration(context.Background(), "acct-1", created.ID, liveCfg)
	require.NoError(t, err)

	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName}},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name:      "ns",
			Connector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
		Status: v2alpha1.CloudflareTunnelStatus{
			TunnelID: created.ID,
			// Built via the same projection the production code compares against,
			// so live and observed are byte-equal — no drift.
			ObservedIngress: snapshotFromConfig(liveCfg),
		},
	}
	base := fake.NewClientBuilder().
		WithScheme(s).WithObjects(tn).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	rec := record.NewFakeRecorder(16)
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())
	r.Recorder = rec

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"},
	})
	require.NoError(t, err)

	var sawDrift bool
	close(rec.Events)
	for ev := range rec.Events {
		if containsAll(ev, conventions.ReasonDriftDetected) {
			sawDrift = true
		}
	}
	require.False(t, sawDrift, "live config matches observed → no DriftDetected Event")
}

func TestApplyRemoteConfig_NoDriftWhenObservedEmpty(t *testing.T) {
	// First-reconcile guard: with Status.ObservedIngress empty/nil there is
	// no baseline to drift from. Even with a non-empty live config, no
	// DriftDetected Event must fire (otherwise every fresh tunnel would emit
	// a spurious drift on first reconcile).
	setEnvCreds(t)
	s := tunnelScheme(t)
	m := mockcf.New()

	created, err := m.Tunnel.CreateTunnel(context.Background(), "acct-1",
		cloudflare.CreateTunnelParams{Name: "ns"})
	require.NoError(t, err)
	_, err = m.Tunnel.PutConfiguration(context.Background(), "acct-1", created.ID,
		cloudflare.TunnelConfig{Ingress: []cloudflare.IngressEntry{
			{Hostname: "live.example.com", Service: "http://live.ns.svc:80"},
		}})
	require.NoError(t, err)

	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName}},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name:      "ns",
			Connector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
		Status: v2alpha1.CloudflareTunnelStatus{
			TunnelID: created.ID,
			// ObservedIngress intentionally nil — no baseline yet.
		},
	}
	base := fake.NewClientBuilder().
		WithScheme(s).WithObjects(tn).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	rec := record.NewFakeRecorder(16)
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())
	r.Recorder = rec

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"},
	})
	require.NoError(t, err)

	var sawDrift bool
	close(rec.Events)
	for ev := range rec.Events {
		if containsAll(ev, conventions.ReasonDriftDetected) {
			sawDrift = true
		}
	}
	require.False(t, sawDrift, "empty ObservedIngress → first-reconcile guard suppresses DriftDetected")
}

func TestReconcile_OwnerTransferPromotesLexSmallest(t *testing.T) {
	// Design §4.1 step 5: with an empty OwnerReferences list but two live
	// AttachedSources, the owner-transfer block must promote the
	// lex-smallest live source to controller-owner and requeue.
	setEnvCreds(t)
	s := tunnelScheme(t)
	m := mockcf.New()

	svcB := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "b-svc", Namespace: "ns", UID: "uid-b"}}
	svcC := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "c-svc", Namespace: "ns", UID: "uid-c"}}
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName},
			Annotations: map[string]string{conventions.AnnotationAutoCreated: "true"},
		},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name:      "ns",
			Connector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
		Status: v2alpha1.CloudflareTunnelStatus{
			AttachedSources: []v2alpha1.AttachedSource{
				{Kind: "Service", Namespace: "ns", Name: "c-svc"},
				{Kind: "Service", Namespace: "ns", Name: "b-svc"},
			},
		},
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(tn, svcB, svcC).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	rec := record.NewFakeRecorder(10)
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())
	r.Recorder = rec

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"}})
	require.NoError(t, err)

	var got v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "tnl", Namespace: "ns"}, &got))
	require.Len(t, got.OwnerReferences, 1)
	require.Equal(t, "b-svc", got.OwnerReferences[0].Name)
}

func TestReconcile_OrphanStateFirstObservation_StampsLastOrphanedAt(t *testing.T) {
	setEnvCreds(t)
	s := tunnelScheme(t)
	m := mockcf.New()
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName},
			Annotations: map[string]string{conventions.AnnotationAutoCreated: "true"},
		},
		Spec:   v2alpha1.CloudflareTunnelSpec{Name: "ns", Connector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30}},
		Status: v2alpha1.CloudflareTunnelStatus{AttachedSources: nil}, // empty -> orphan
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(tn).WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"}})
	require.NoError(t, err)
	var got v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "tnl", Namespace: "ns"}, &got))
	require.NotNil(t, got.Status.LastOrphanedAt, "first orphan observation must stamp LastOrphanedAt")
	require.Greater(t, res.RequeueAfter, time.Duration(0), "should requeue after grace")
	require.LessOrEqual(t, res.RequeueAfter, pendingDeletionGrace+1*time.Second, "requeue near grace window")
}

func TestReconcile_OrphanStateGraceElapsed_SelfDeletes(t *testing.T) {
	setEnvCreds(t)
	s := tunnelScheme(t)
	m := mockcf.New()
	staleStamp := metav1.NewTime(time.Now().Add(-(pendingDeletionGrace + 5*time.Second)))
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName},
			Annotations: map[string]string{conventions.AnnotationAutoCreated: "true"},
		},
		Spec:   v2alpha1.CloudflareTunnelSpec{Name: "ns", Connector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30}},
		Status: v2alpha1.CloudflareTunnelStatus{LastOrphanedAt: &staleStamp},
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(tn).WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	rec := record.NewFakeRecorder(10)
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())
	r.Recorder = rec
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"}})
	require.NoError(t, err)
	var got v2alpha1.CloudflareTunnel
	gerr := c.Get(context.Background(), types.NamespacedName{Name: "tnl", Namespace: "ns"}, &got)
	if gerr == nil {
		require.NotNil(t, got.DeletionTimestamp, "self-delete must have set DeletionTimestamp")
	} else {
		require.True(t, apierrors.IsNotFound(gerr), "CR may already be deleted: %v", gerr)
	}
	foundTerminal := false
drain:
	for {
		select {
		case ev := <-rec.Events:
			if strings.Contains(ev, conventions.ReasonTerminalNoSources) {
				foundTerminal = true
			}
		default:
			break drain
		}
	}
	require.True(t, foundTerminal, "expected TerminalNoSources event before self-delete")
}

// TestReconcile_OrphanSelfDelete_ConflictDefersDelete asserts the CORRECT
// behaviour that Phase B will implement: when r.Status().Update returns a
// Conflict the reconciler must requeue (Requeue:true, nil error) and MUST NOT
// proceed to self-delete the tunnel.
//
// Against the current (buggy) code this test FAILS: the current code discards
// the Conflict with `_ = r.Status().Update(...)` and then calls r.Delete,
// producing ctrl.Result{} and a deleted/DeletionTimestamp-set tunnel instead
// of ctrl.Result{Requeue:true} with the tunnel intact.
func TestReconcile_OrphanSelfDelete_ConflictDefersDelete(t *testing.T) {
	setEnvCreds(t)
	s := tunnelScheme(t)
	m := mockcf.New()

	// Stale orphan: same stamp as TestReconcile_OrphanStateGraceElapsed_SelfDeletes
	// so time.Since(LastOrphanedAt) >= grace is true and the self-delete branch fires.
	staleStamp := metav1.NewTime(time.Now().Add(-(pendingDeletionGrace + 5*time.Second)))
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName},
			Annotations: map[string]string{conventions.AnnotationAutoCreated: "true"},
		},
		Spec:   v2alpha1.CloudflareTunnelSpec{Name: "ns", Connector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30}},
		Status: v2alpha1.CloudflareTunnelStatus{LastOrphanedAt: &staleStamp},
	}

	// Build the client stack mirroring TestReconcile_OrphanStateGraceElapsed_SelfDeletes,
	// then wrap with an interceptor that returns a Conflict on SubResourceUpdate.
	//
	// Layering (innermost → outermost):
	//   base (fake)
	//   → SSATranslatingClient(t, base)   — intercepts Patch/SSA (same as the SelfDeletes test)
	//   → interceptor.NewClient(ssa, ...) — intercepts SubResourceUpdate to inject Conflict
	//
	// When the reconciler calls r.Status().Update(ctx, &tn):
	//   outer.Status() → outer.SubResource("status") → subResourceInterceptor{funcs: our Funcs}
	//   → SubResourceUpdate hook → returns Conflict immediately, no real write.
	//
	// This mirrors the pattern in attach_test.go:440 (Patch interceptor wrapping a fake base)
	// but targets SubResourceUpdate instead of Patch, and wraps SSATranslatingClient as the
	// inner layer so SSA patch semantics are still available for the rest of reconciliation.
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(tn).WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	ssa := reconcilelib.SSATranslatingClient(t, base)
	conflictErr := apierrors.NewConflict(
		schema.GroupResource{Group: "cloudflare.io", Resource: "cloudflaretunnels"},
		"tnl",
		errors.New("conflict"),
	)
	c := interceptor.NewClient(ssa, interceptor.Funcs{
		SubResourceUpdate: func(_ context.Context, _ client.Client, _ string, _ client.Object, _ ...client.SubResourceUpdateOption) error {
			return conflictErr
		},
	})

	rec := record.NewFakeRecorder(10)
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())
	r.Recorder = rec

	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"}})

	// Phase B target: Conflict must cause a requeue, not a delete.
	require.NoError(t, err, "Conflict on Status().Update must not propagate as an error")
	require.True(t, res.Requeue, "Conflict on Status().Update must requeue")

	// The tunnel must still exist and must NOT have been deleted.
	// (Current code deletes it; Phase B must prevent that.)
	var got v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "tnl", Namespace: "ns"}, &got),
		"tunnel must still exist after Conflict — self-delete must not have proceeded")
	require.Nil(t, got.DeletionTimestamp, "DeletionTimestamp must be nil — tunnel was not self-deleted")

	// Test-fidelity guard: the TerminalNoSources event is emitted in the
	// self-delete branch immediately BEFORE the guarded Status().Update.
	// Asserting it was recorded pins this test to the self-delete site —
	// if a future reconcile pass added an earlier Status().Update, the
	// unconditioned interceptor would otherwise silently exercise the wrong
	// site (the event would not have fired yet).
	sawTerminal := false
drain:
	for {
		select {
		case ev := <-rec.Events:
			if strings.Contains(ev, conventions.ReasonTerminalNoSources) {
				sawTerminal = true
			}
		default:
			break drain
		}
	}
	require.True(t, sawTerminal,
		"TerminalNoSources event must precede the guarded Status().Update — confirms the Conflict was injected at the self-delete branch")
}

func TestReconcile_OrphanStateClearedOnSourceReattach(t *testing.T) {
	setEnvCreds(t)
	s := tunnelScheme(t)
	m := mockcf.New()
	stamp := metav1.NewTime(time.Now())
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns", UID: "uid-svc"}}
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName},
			Annotations:     map[string]string{conventions.AnnotationAutoCreated: "true"},
			OwnerReferences: []metav1.OwnerReference{{Kind: "Service", Name: "svc", UID: "uid-svc", APIVersion: "v1", Controller: ptr.To(true)}},
		},
		Spec: v2alpha1.CloudflareTunnelSpec{Name: "ns", Connector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30}},
		Status: v2alpha1.CloudflareTunnelStatus{
			LastOrphanedAt:  &stamp,
			AttachedSources: []v2alpha1.AttachedSource{{Kind: "Service", Namespace: "ns", Name: "svc"}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(tn, svc).WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"}})
	require.NoError(t, err)
	var got v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "tnl", Namespace: "ns"}, &got))
	require.Nil(t, got.Status.LastOrphanedAt, "source reattach must clear LastOrphanedAt")
}

// TestReconcile_RequeueIsMinOfPendingAndInterval is a characterisation /
// regression-lock test for the trailing-return requeue logic.  It verifies
// the min(pendingRequeueAfter, interval) contract across the three meaningful
// cases without relying on any specific implementation detail beyond what
// callers can observe (ctrl.Result.RequeueAfter).
//
// The test MUST pass both before and after EDIT 1 — it locks behaviour, not
// an implementation; if it ever goes RED the production semantics changed.
func TestReconcile_RequeueIsMinOfPendingAndInterval(t *testing.T) {
	setEnvCreds(t)

	// Case 1: pending == 0 (steady state, no orphan).
	// A healthy tunnel with Spec.Interval=90s and no orphan state must requeue
	// at exactly the resolved interval (90s).
	t.Run("pending_zero_returns_interval", func(t *testing.T) {
		s := tunnelScheme(t)
		m := mockcf.New()
		smallInterval := metav1.Duration{Duration: 90 * time.Second}
		svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns", UID: "uid-svc"}}
		tn := &v2alpha1.CloudflareTunnel{
			ObjectMeta: metav1.ObjectMeta{
				Name: "tnl", Namespace: "ns",
				Finalizers:      []string{conventions.FinalizerName},
				Annotations:     map[string]string{conventions.AnnotationAutoCreated: "true"},
				OwnerReferences: []metav1.OwnerReference{{Kind: "Service", Name: "svc", UID: "uid-svc", APIVersion: "v1", Controller: ptr.To(true)}},
			},
			Spec: v2alpha1.CloudflareTunnelSpec{
				Name:     "ns",
				Interval: &smallInterval,
				Connector: v2alpha1.ConnectorSpec{
					Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30,
				},
			},
			Status: v2alpha1.CloudflareTunnelStatus{
				// AttachedSources has one entry so the tunnel is NOT orphaned.
				AttachedSources: []v2alpha1.AttachedSource{{Kind: "Service", Namespace: "ns", Name: "svc"}},
			},
		}
		base := fake.NewClientBuilder().WithScheme(s).WithObjects(tn, svc).WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
		c := reconcilelib.SSATranslatingClient(t, base)
		r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"}})
		require.NoError(t, err)
		require.Equal(t, 90*time.Second, res.RequeueAfter, "steady-state requeue must equal Spec.Interval")
	})

	// Case 2: pending > 0 && pending < interval (orphan grace SHORTER than interval).
	// Use Spec.Interval >> pendingDeletionGrace so that the fresh-stamp path
	// sets pendingRequeueAfter = grace < interval; result must equal grace, not interval.
	t.Run("pending_less_than_interval_returns_pending", func(t *testing.T) {
		s := tunnelScheme(t)
		m := mockcf.New()
		// Spec.Interval is much larger than pendingDeletionGrace (60s) so that
		// grace < interval and min(grace, interval) == grace.
		largeInterval := metav1.Duration{Duration: pendingDeletionGrace * 10}
		tn := &v2alpha1.CloudflareTunnel{
			ObjectMeta: metav1.ObjectMeta{
				Name: "tnl", Namespace: "ns",
				Finalizers:  []string{conventions.FinalizerName},
				Annotations: map[string]string{conventions.AnnotationAutoCreated: "true"},
			},
			Spec: v2alpha1.CloudflareTunnelSpec{
				Name:     "ns",
				Interval: &largeInterval,
				Connector: v2alpha1.ConnectorSpec{
					Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30,
				},
			},
			Status: v2alpha1.CloudflareTunnelStatus{
				// No AttachedSources + auto-created → first orphan observation.
				// Reconciler will stamp LastOrphanedAt and set
				// pendingRequeueAfter = pendingDeletionGrace (60s) < largeInterval.
			},
		}
		base := fake.NewClientBuilder().WithScheme(s).WithObjects(tn).WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
		c := reconcilelib.SSATranslatingClient(t, base)
		r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"}})
		require.NoError(t, err)
		require.Greater(t, res.RequeueAfter, time.Duration(0), "grace-window requeue must be positive")
		require.LessOrEqual(t, res.RequeueAfter, pendingDeletionGrace, "requeue must not exceed grace (== min(grace,interval))")
		require.Less(t, res.RequeueAfter, largeInterval.Duration, "requeue must be shorter than the large interval")
	})

	// Case 3: pending > 0 && pending >= interval (orphan grace LONGER than interval).
	// Use a LastOrphanedAt that is recent (within grace) but Spec.Interval <<
	// pendingDeletionGrace so that the remaining-window path yields
	// pendingRequeueAfter ≈ grace > interval; min(pending, interval) == interval.
	t.Run("pending_greater_than_interval_returns_interval", func(t *testing.T) {
		s := tunnelScheme(t)
		m := mockcf.New()
		// Spec.Interval is much smaller than pendingDeletionGrace (60s) so that
		// interval < remaining-grace and min(remaining, interval) == interval.
		tinyInterval := metav1.Duration{Duration: pendingDeletionGrace / 10}
		// Stamp LastOrphanedAt just now so the remaining window ≈ full grace (>> tinyInterval).
		recentStamp := metav1.NewTime(time.Now())
		tn := &v2alpha1.CloudflareTunnel{
			ObjectMeta: metav1.ObjectMeta{
				Name: "tnl", Namespace: "ns",
				Finalizers:  []string{conventions.FinalizerName},
				Annotations: map[string]string{conventions.AnnotationAutoCreated: "true"},
			},
			Spec: v2alpha1.CloudflareTunnelSpec{
				Name:     "ns",
				Interval: &tinyInterval,
				Connector: v2alpha1.ConnectorSpec{
					Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30,
				},
			},
			Status: v2alpha1.CloudflareTunnelStatus{
				// LastOrphanedAt is fresh so we land in the "within grace, requeue
				// remaining window" branch; remaining ≈ 60s >> tinyInterval (6s).
				LastOrphanedAt: &recentStamp,
			},
		}
		base := fake.NewClientBuilder().WithScheme(s).WithObjects(tn).WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
		c := reconcilelib.SSATranslatingClient(t, base)
		r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())
		res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"}})
		require.NoError(t, err)
		// min(remaining≈60s, tinyInterval=6s) should be tinyInterval.
		require.LessOrEqual(t, res.RequeueAfter, tinyInterval.Duration+1*time.Second,
			"requeue must be capped at interval when pending > interval")
		require.Greater(t, res.RequeueAfter, time.Duration(0), "requeue must be positive")
	})
}

// TestReconcile_PreP4Tunnel_StampedThenGCd verifies the stamp-on-detect
// self-heal path and the cascadeGCEligible gate for pre-P4 tunnels.
//
// A tunnel that carries operator source labels (cloudflare.io/source-kind/name/namespace)
// but NO cloudflare.io/auto-created annotation, no OwnerReferences, and an
// empty Status.AttachedSources (the pre-P4 orphan shape) must:
//
//  1. Reconcile #1 (first-orphan tick): the reconciler stamps
//     cloudflare.io/auto-created="true" (self-heal) AND sets
//     Status.LastOrphanedAt (proves cascadeGCEligible admitted it).
//
//  2. Second tick (grace elapsed): driving the same grace-advancement
//     mechanism as TestReconcile_OrphanStateGraceElapsed_SelfDeletes, the
//     reconciler self-deletes the CR (cascade-GC proceeds).
func TestReconcile_PreP4Tunnel_StampedThenGCd(t *testing.T) {
	setEnvCreds(t)
	s := tunnelScheme(t)
	m := mockcf.New()

	// Pre-P4 orphan shape: source labels prove operator authorship, but no
	// auto-created annotation, no ownerRefs, no AttachedSources.
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tnl", Namespace: "ns",
			Finalizers: []string{conventions.FinalizerName},
			Labels: map[string]string{
				"cloudflare.io/source-kind":      "Service",
				"cloudflare.io/source-name":      "my-svc",
				"cloudflare.io/source-namespace": "ns",
			},
			// No AnnotationAutoCreated — this is the pre-P4 shape.
		},
		Spec:   v2alpha1.CloudflareTunnelSpec{Name: "ns", Connector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30}},
		Status: v2alpha1.CloudflareTunnelStatus{AttachedSources: nil}, // orphan: no sources
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(tn).WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	rec := record.NewFakeRecorder(10)
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())
	r.Recorder = rec

	// Reconcile #1: stamp-on-detect fires (HasSourceLabels && !isAutoCreated)
	// AND the cascadeGCEligible gate admits the tunnel into the orphan-state
	// block (sets LastOrphanedAt, requeues after grace).
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"}})
	require.NoError(t, err)

	var afterPass1 v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "tnl", Namespace: "ns"}, &afterPass1))

	// The auto-created annotation must have been stamped (self-heal).
	require.Equal(t, "true", afterPass1.Annotations[conventions.AnnotationAutoCreated],
		"stamp-on-detect must set cloudflare.io/auto-created=true for pre-P4 source-labelled tunnel")

	// LastOrphanedAt must be set (proves the cascadeGCEligible gate admitted it).
	require.NotNil(t, afterPass1.Status.LastOrphanedAt,
		"cascadeGCEligible gate must admit pre-P4 tunnel into orphan-state block; LastOrphanedAt must be set")
	require.Greater(t, res.RequeueAfter, time.Duration(0), "should requeue after grace")

	// Second tick: drive the grace-advancement mechanism exactly as
	// TestReconcile_OrphanStateGraceElapsed_SelfDeletes does — update
	// Status.LastOrphanedAt to a stale time so time.Since >= grace.
	staleStamp := metav1.NewTime(time.Now().Add(-(pendingDeletionGrace + 5*time.Second)))
	afterPass1.Status.LastOrphanedAt = &staleStamp
	require.NoError(t, c.Status().Update(context.Background(), &afterPass1))

	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"}})
	require.NoError(t, err)

	var got v2alpha1.CloudflareTunnel
	gerr := c.Get(context.Background(), types.NamespacedName{Name: "tnl", Namespace: "ns"}, &got)
	if gerr == nil {
		require.NotNil(t, got.DeletionTimestamp, "self-delete must have set DeletionTimestamp")
	} else {
		require.True(t, apierrors.IsNotFound(gerr), "CR may already be deleted: %v", gerr)
	}

	foundTerminal := false
drain:
	for {
		select {
		case ev := <-rec.Events:
			if strings.Contains(ev, conventions.ReasonTerminalNoSources) {
				foundTerminal = true
			}
		default:
			break drain
		}
	}
	require.True(t, foundTerminal, "expected TerminalNoSources event before self-delete")
}

// TestReconcile_PreP4Tunnel_StampPatchFails_StillCascadeGCd is a fault-injection
// regression guard for the best-effort self-heal stamp path in Reconcile.
//
// A pre-P4-shaped tunnel (operator source labels, no cloudflare.io/auto-created
// annotation, no OwnerReferences, empty Status.AttachedSources) must cascade-GC
// even when the metadata MergeFrom Patch that stamps the auto-created annotation
// fails. The stamp is explicitly best-effort: a failed Patch is logged at V(1)
// and the reconciler falls through — cascadeGCEligible still admits the tunnel
// via its source labels, so cascade-GC proceeds regardless.
//
// Non-vacuity: if someone adds a bare `return ... err` after the patch in the
// self-heal block, Reconcile #1 returns a non-nil error and assertion 1 (NoError)
// fails immediately, or the status update for LastOrphanedAt is never persisted
// and assertion 3 (cascade-GC terminal) fails on the second tick.
func TestReconcile_PreP4Tunnel_StampPatchFails_StillCascadeGCd(t *testing.T) {
	setEnvCreds(t)
	s := tunnelScheme(t)
	m := mockcf.New()

	// Pre-P4 orphan shape: source labels prove operator authorship, but no
	// auto-created annotation, no ownerRefs, no AttachedSources.
	// Mirrors TestReconcile_PreP4Tunnel_StampedThenGCd exactly — only the
	// client wrapping differs.
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "tnl",
			Namespace:  "ns",
			Finalizers: []string{conventions.FinalizerName},
			Labels: map[string]string{
				conventions.LabelSourceKind:      "Service",
				conventions.LabelSourceName:      "my-svc",
				conventions.LabelSourceNamespace: "ns",
			},
			// No AnnotationAutoCreated — this is the pre-P4 shape.
		},
		Spec:   v2alpha1.CloudflareTunnelSpec{Name: "ns", Connector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30}},
		Status: v2alpha1.CloudflareTunnelStatus{AttachedSources: nil}, // orphan: no sources
	}

	base := fake.NewClientBuilder().WithScheme(s).WithObjects(tn).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	ssa := reconcilelib.SSATranslatingClient(t, base)

	// Wrap with a Patch interceptor that fails ONLY when the self-heal block
	// stamps cloudflare.io/auto-created on a CloudflareTunnel. The self-heal
	// block calls tn.SetAnnotations(anns) BEFORE invoking r.Client.Patch, so
	// obj.GetAnnotations()[conventions.AnnotationAutoCreated] == "true" is the
	// precise discriminator. All other Patch calls (different types, or tunnels
	// without the annotation) delegate to the wrapped client unchanged.
	stampErr := errors.New("injected: stamp patch failure")
	c := interceptor.NewClient(ssa, interceptor.Funcs{
		Patch: func(ctx context.Context, wc client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if tun, ok := obj.(*v2alpha1.CloudflareTunnel); ok {
				if tun.GetAnnotations()[conventions.AnnotationAutoCreated] == "true" {
					return stampErr
				}
			}
			return wc.Patch(ctx, obj, patch, opts...)
		},
	})

	rec := record.NewFakeRecorder(10)
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())
	r.Recorder = rec

	// Reconcile #1: stamp-on-detect fires but the Patch fails (injected).
	// The reconciler must NOT abort — it logs at V(1) and falls through to the
	// cascadeGCEligible / orphan-state block which stamps LastOrphanedAt.
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"}})
	// Assertion 1: failed stamp Patch must NOT abort reconcile.
	require.NoError(t, err, "a failed auto-created stamp Patch must not abort reconcile (best-effort path)")

	// Assertion 2: the annotation was NOT persisted (Patch failed).
	// Guard for NotFound: if cascade-GC already deleted the CR on this pass
	// (edge case: grace = 0), treat deletion as stronger proof GC proceeded.
	var afterPass1 v2alpha1.CloudflareTunnel
	getErr := c.Get(context.Background(), types.NamespacedName{Name: "tnl", Namespace: "ns"}, &afterPass1)
	if apierrors.IsNotFound(getErr) {
		// Tunnel already GC'd — this is even stronger evidence cascade-GC
		// proceeded. The test is done; assertions 1 and 3 are both satisfied.
		return
	}
	require.NoError(t, getErr)
	require.Empty(t, afterPass1.Annotations[conventions.AnnotationAutoCreated],
		"stamp Patch failed: cloudflare.io/auto-created must NOT be persisted on the CR")

	// Assertion 3: cascade-GC still proceeded despite the failed stamp.
	// The cascadeGCEligible gate admits the tunnel via source labels alone, so
	// LastOrphanedAt must be stamped on the first orphan observation.
	require.NotNil(t, afterPass1.Status.LastOrphanedAt,
		"cascadeGCEligible must admit pre-P4 tunnel via source labels even when stamp Patch fails; LastOrphanedAt must be set")
	require.Greater(t, res.RequeueAfter, time.Duration(0), "should requeue after grace")

	// Second tick: advance the grace stamp to simulate the confirmation window
	// elapsed, then re-reconcile. Mirrors TestReconcile_PreP4Tunnel_StampedThenGCd.
	staleStamp := metav1.NewTime(time.Now().Add(-(pendingDeletionGrace + 5*time.Second)))
	afterPass1.Status.LastOrphanedAt = &staleStamp
	require.NoError(t, c.Status().Update(context.Background(), &afterPass1))

	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"}})
	require.NoError(t, err)

	// Terminal post-condition: the tunnel must be deleted (cascade-GC completed).
	var got v2alpha1.CloudflareTunnel
	gerr := c.Get(context.Background(), types.NamespacedName{Name: "tnl", Namespace: "ns"}, &got)
	if gerr == nil {
		require.NotNil(t, got.DeletionTimestamp, "self-delete must have set DeletionTimestamp")
	} else {
		require.True(t, apierrors.IsNotFound(gerr), "CR may already be deleted: %v", gerr)
	}

	foundTerminal := false
drain:
	for {
		select {
		case ev := <-rec.Events:
			if strings.Contains(ev, conventions.ReasonTerminalNoSources) {
				foundTerminal = true
			}
		default:
			break drain
		}
	}
	require.True(t, foundTerminal, "expected TerminalNoSources event before self-delete")
}

// TestReconcile_OrphanedUserAuthored_SurfacedNotDeleted asserts that a tunnel
// with no operator source labels, no cloudflare.io/auto-created annotation, no
// OwnerReferences, and empty Status.AttachedSources (genuinely user-authored,
// orphan-shaped) is NEVER auto-deleted — not even past the grace window — and
// is surfaced via a Warning Event + Ready=False condition with reason
// ReasonOrphanedUnmanaged. The Event is transition-gated so a second reconcile
// does not emit a duplicate.
func TestReconcile_OrphanedUserAuthored_SurfacedNotDeleted(t *testing.T) {
	setEnvCreds(t)
	s := tunnelScheme(t)
	m := mockcf.New()

	// Genuinely user-authored: no annotation, no operator source labels,
	// no OwnerReferences, empty Status.AttachedSources.
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "user-tnl",
			Namespace:  "ns",
			Finalizers: []string{conventions.FinalizerName},
			// No AnnotationAutoCreated, no source labels, no OwnerReferences.
		},
		Spec:   v2alpha1.CloudflareTunnelSpec{Name: "user", Connector: v2alpha1.ConnectorSpec{Replicas: 1, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30}},
		Status: v2alpha1.CloudflareTunnelStatus{AttachedSources: nil},
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(tn).WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	rec := record.NewFakeRecorder(16)
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())
	r.Recorder = rec

	// Reconcile #1: must NOT delete, must NOT stamp auto-created, must NOT set
	// LastOrphanedAt, must emit ReasonOrphanedUnmanaged Event + condition.
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "user-tnl", Namespace: "ns"}})
	require.NoError(t, err)

	var afterPass1 v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "user-tnl", Namespace: "ns"}, &afterPass1))

	// CR must still exist and must NOT have been deleted or marked for deletion.
	require.Nil(t, afterPass1.DeletionTimestamp, "user-authored tunnel must NOT be deleted")

	// auto-created annotation must NOT be stamped.
	require.Empty(t, afterPass1.Annotations[conventions.AnnotationAutoCreated],
		"user-authored tunnel must NOT receive auto-created annotation")

	// LastOrphanedAt must NOT be set (tunnel never entered the cascade-GC path).
	require.Nil(t, afterPass1.Status.LastOrphanedAt,
		"user-authored tunnel must NOT have LastOrphanedAt set")

	// Ready condition must be Ready=False with reason OrphanedUnmanaged.
	var readyCond *metav1.Condition
	for i := range afterPass1.Status.Conditions {
		if afterPass1.Status.Conditions[i].Type == conventions.ConditionTypeReady {
			readyCond = &afterPass1.Status.Conditions[i]
		}
	}
	require.NotNil(t, readyCond, "Ready condition must be present after pass 1")
	require.Equal(t, metav1.ConditionFalse, readyCond.Status, "Ready must be False for orphaned-unmanaged tunnel")
	require.Equal(t, conventions.ReasonOrphanedUnmanaged, readyCond.Reason,
		"Ready condition reason must be OrphanedUnmanaged")

	// At least one OrphanedUnmanaged Warning Event must have been emitted.
	pass1Events := drainEvents(rec)
	sawOrphanedUnmanaged := false
	for _, ev := range pass1Events {
		if containsAll(ev, conventions.ReasonOrphanedUnmanaged) {
			sawOrphanedUnmanaged = true
		}
	}
	require.True(t, sawOrphanedUnmanaged, "Warning Event with reason OrphanedUnmanaged must be emitted on pass 1")

	// Reconcile #2: transition-gate must suppress a duplicate OrphanedUnmanaged
	// Event (condition is already OrphanedUnmanaged so no new Event).
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "user-tnl", Namespace: "ns"}})
	require.NoError(t, err)

	var afterPass2 v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "user-tnl", Namespace: "ns"}, &afterPass2))

	// Still not deleted after pass 2.
	require.Nil(t, afterPass2.DeletionTimestamp, "user-authored tunnel must NOT be deleted after pass 2")

	// No duplicate OrphanedUnmanaged Event on pass 2.
	pass2Events := drainEvents(rec)
	for _, ev := range pass2Events {
		require.False(t, containsAll(ev, conventions.ReasonOrphanedUnmanaged),
			"duplicate OrphanedUnmanaged Event must NOT be emitted on pass 2 (transition-gated)")
	}
}

func TestApplyRemoteConfig_SkipGetWhenWantMatchesObserved(t *testing.T) {
	// G (simplify): when wantSnap == Status.ObservedIngress (steady state),
	// applyRemoteConfig must NOT call GetConfiguration — the drift-check call
	// is only meaningful when the operator's own desired state moved.
	setEnvCreds(t)
	s := tunnelScheme(t)
	m := mockcf.New()

	created, err := m.Tunnel.CreateTunnel(context.Background(), "acct-1",
		cloudflare.CreateTunnelParams{Name: "ns"})
	require.NoError(t, err)

	// ObservedIngress matches the snapshot an empty cache produces (catch-all
	// only) so that wantSnap == ObservedIngress on reconcile.
	matchedSnap := []v2alpha1.IngressEntrySnapshot{{Service: "http_status:404"}}
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName}},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name:      "ns",
			Connector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
		Status: v2alpha1.CloudflareTunnelStatus{
			TunnelID:        created.ID,
			ObservedIngress: matchedSnap,
		},
	}
	base := fake.NewClientBuilder().
		WithScheme(s).WithObjects(tn).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"},
	})
	require.NoError(t, err)

	require.Equal(t, 0, m.Calls("Tunnel.GetConfiguration"),
		"wantSnap == ObservedIngress → GetConfiguration must be skipped (steady state)")
}

func TestApplyRemoteConfig_CallsGetWhenWantDiffersFromObserved(t *testing.T) {
	// G (simplify) non-vacuity: when wantSnap != Status.ObservedIngress, the
	// GetConfiguration drift-check must still fire (preserves existing
	// drift-detection behavior when the operator's own desired state changes).
	setEnvCreds(t)
	s := tunnelScheme(t)
	m := mockcf.New()

	created, err := m.Tunnel.CreateTunnel(context.Background(), "acct-1",
		cloudflare.CreateTunnelParams{Name: "ns"})
	require.NoError(t, err)

	// ObservedIngress is a stale prior entry — it does NOT match the catch-all
	// that an empty cache will resolve to, so wantSnap != ObservedIngress and
	// the gate must allow GetConfiguration through.
	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName}},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name:      "ns",
			Connector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
		Status: v2alpha1.CloudflareTunnelStatus{
			TunnelID: created.ID,
			// Stale prior snap — differs from the catch-all wantSnap.
			ObservedIngress: []v2alpha1.IngressEntrySnapshot{
				{Hostname: "old.example.com", Service: "http://old.ns.svc:80"},
			},
		},
	}
	base := fake.NewClientBuilder().
		WithScheme(s).WithObjects(tn).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"},
	})
	require.NoError(t, err)

	require.GreaterOrEqual(t, m.Calls("Tunnel.GetConfiguration"), 1,
		"wantSnap != ObservedIngress → GetConfiguration must be called (drift-detection path)")
}

func TestSnapshotFromConfig_ProjectsOriginRequest(t *testing.T) {
	osn := "origin.example.com"
	ntvTrue := true
	ntvFalse := false

	cases := []struct {
		name string
		in   *cloudflare.IngressOriginRequest
		want *v2alpha1.IngressSnapshotOriginRequest
	}{
		{"nil → nil", nil, nil},
		{"empty → nil", &cloudflare.IngressOriginRequest{}, nil},
		{"osn only → osn", &cloudflare.IngressOriginRequest{OriginServerName: &osn}, &v2alpha1.IngressSnapshotOriginRequest{OriginServerName: &osn}},
		{"ntv=true → ntv=true", &cloudflare.IngressOriginRequest{NoTLSVerify: &ntvTrue}, &v2alpha1.IngressSnapshotOriginRequest{NoTLSVerify: &ntvTrue}},
		{"ntv=false (unset-vs-false ambiguity) → nil", &cloudflare.IngressOriginRequest{NoTLSVerify: &ntvFalse}, nil},
		{"both → both", &cloudflare.IngressOriginRequest{OriginServerName: &osn, NoTLSVerify: &ntvTrue}, &v2alpha1.IngressSnapshotOriginRequest{OriginServerName: &osn, NoTLSVerify: &ntvTrue}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := cloudflare.TunnelConfig{Ingress: []cloudflare.IngressEntry{
				{Hostname: "h.example.com", Service: "https://svc:443", OriginRequest: c.in},
			}}
			got := snapshotFromConfig(cfg)
			require.Len(t, got, 1)
			if c.want == nil {
				require.Nil(t, got[0].OriginRequest)
				return
			}
			require.NotNil(t, got[0].OriginRequest)
			if c.want.OriginServerName == nil {
				require.Nil(t, got[0].OriginRequest.OriginServerName)
			} else {
				require.NotNil(t, got[0].OriginRequest.OriginServerName)
				require.Equal(t, *c.want.OriginServerName, *got[0].OriginRequest.OriginServerName)
			}
			if c.want.NoTLSVerify == nil {
				require.Nil(t, got[0].OriginRequest.NoTLSVerify)
			} else {
				require.NotNil(t, got[0].OriginRequest.NoTLSVerify)
				require.Equal(t, *c.want.NoTLSVerify, *got[0].OriginRequest.NoTLSVerify)
			}
		})
	}
}

// containsAll returns true when s contains every substring.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// drainEvents drains all currently buffered events from a FakeRecorder into a
// slice without blocking.
func drainEvents(rec *record.FakeRecorder) []string {
	var out []string
	for {
		select {
		case ev := <-rec.Events:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func TestEmitOriginRequestWipedEvents(t *testing.T) {
	osn := "origin.example.com"
	live := &cloudflare.TunnelConfiguration{Config: cloudflare.TunnelConfig{Ingress: []cloudflare.IngressEntry{
		{Hostname: "kept.example.com", Service: "https://a:443", OriginRequest: &cloudflare.IngressOriginRequest{OriginServerName: &osn}},
		{Hostname: "wiped.example.com", Service: "https://b:443", OriginRequest: &cloudflare.IngressOriginRequest{OriginServerName: &osn}},
		{Service: "http_status:404"},
	}}}
	wantOSN := "origin.example.com"
	cfg := cloudflare.TunnelConfig{Ingress: []cloudflare.IngressEntry{
		{Hostname: "kept.example.com", Service: "https://a:443", OriginRequest: &cloudflare.IngressOriginRequest{OriginServerName: &wantOSN}},
		{Hostname: "wiped.example.com", Service: "https://b:443"},
		{Service: "http_status:404"},
	}}
	rec := record.NewFakeRecorder(8)
	tn := &v2alpha1.CloudflareTunnel{ObjectMeta: metav1.ObjectMeta{Name: "tn", Namespace: "ns"}}
	emitOriginRequestWipedEvents(rec, tn, live, cfg)
	var got []string
	for {
		select {
		case e := <-rec.Events:
			got = append(got, e)
		default:
			require.Len(t, got, 1, "expected exactly one wipe event; got: %v", got)
			require.Contains(t, got[0], "wiped.example.com")
			require.NotContains(t, got[0], "kept.example.com")
			require.Contains(t, got[0], conventions.ReasonOriginRequestWiped)
			return
		}
	}
}

func TestEmitOriginRequestWipedEvents_NilLiveIsNoop(t *testing.T) {
	rec := record.NewFakeRecorder(4)
	tn := &v2alpha1.CloudflareTunnel{ObjectMeta: metav1.ObjectMeta{Name: "tn", Namespace: "ns"}}
	emitOriginRequestWipedEvents(rec, tn, nil, cloudflare.TunnelConfig{})
	select {
	case e := <-rec.Events:
		t.Fatalf("expected no events on nil live; got %s", e)
	default:
	}
}

func TestEmitOriginRequestWipedEvents_NilRecorderIsNoop(t *testing.T) {
	live := &cloudflare.TunnelConfiguration{Config: cloudflare.TunnelConfig{Ingress: []cloudflare.IngressEntry{
		{Hostname: "x.example.com", OriginRequest: &cloudflare.IngressOriginRequest{}},
	}}}
	cfg := cloudflare.TunnelConfig{}
	// Should not panic.
	emitOriginRequestWipedEvents(nil, nil, live, cfg)
}
