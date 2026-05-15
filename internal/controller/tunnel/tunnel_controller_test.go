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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
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
	require.NoError(t, v1alpha1.AddToScheme(s))
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
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns"},
		Spec: v1alpha1.CloudflareTunnelSpec{
			Name: "cf-ns",
			Connector: v1alpha1.ConnectorSpec{
				Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30,
			},
		},
	}
	s := tunnelScheme(t)
	base := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(tn).
		WithStatusSubresource(&v1alpha1.CloudflareTunnel{}).
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

	var got v1alpha1.CloudflareTunnel
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
		cloudflare.CreateTunnelParams{Name: "cf-ns"})
	require.NoError(t, err)
	_, err = m.Tunnel.PutConfiguration(context.Background(), "acct-1", created.ID,
		cloudflare.TunnelConfig{Ingress: []cloudflare.IngressEntry{{Service: "http_status:404"}}})
	require.NoError(t, err)

	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName}},
		Spec: v1alpha1.CloudflareTunnelSpec{
			Name:      "cf-ns",
			Connector: v1alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
		Status: v1alpha1.CloudflareTunnelStatus{
			TunnelID:        created.ID,
			ObservedIngress: []v1alpha1.IngressEntrySnapshot{{Service: "http_status:404"}},
		},
	}
	base := fake.NewClientBuilder().
		WithScheme(s).WithObjects(tn).
		WithStatusSubresource(&v1alpha1.CloudflareTunnel{}).Build()
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
		cloudflare.CreateTunnelParams{Name: "cf-ns"})
	require.NoError(t, err)

	now := metav1.Now()
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tnl", Namespace: "ns",
			Finalizers:        []string{conventions.FinalizerName},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.CloudflareTunnelSpec{
			Name:      "cf-ns",
			Connector: v1alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
		Status: v1alpha1.CloudflareTunnelStatus{TunnelID: created.ID},
	}
	base := fake.NewClientBuilder().
		WithScheme(s).WithObjects(tn).
		WithStatusSubresource(&v1alpha1.CloudflareTunnel{}).Build()
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
	var got v1alpha1.CloudflareTunnel
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
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tnl", Namespace: "ns",
			Finalizers:        []string{conventions.FinalizerName},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.CloudflareTunnelSpec{
			Name:      "cf-ns",
			Connector: v1alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
		// TunnelID points at a tunnel that does NOT exist in the mock.
		Status: v1alpha1.CloudflareTunnelStatus{TunnelID: "ghost-tunnel-id"},
	}
	base := fake.NewClientBuilder().
		WithScheme(s).WithObjects(tn).
		WithStatusSubresource(&v1alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"},
	})
	require.NoError(t, err, "WrapDeleteErr must swallow 404s on delete-path")

	var got v1alpha1.CloudflareTunnel
	gerr := c.Get(context.Background(), types.NamespacedName{Name: "tnl", Namespace: "ns"}, &got)
	require.True(t, apierrors.IsNotFound(gerr), "finalizer must be removable even on already-gone tunnel")
}

func TestTunnelReconciler_StatusConditionsWrittenByOuterFunction(t *testing.T) {
	// Helpers mutate in-memory only; the OUTER Reconcile is the sole persist
	// point (review pattern #12). We verify by re-fetching after Reconcile
	// (review pattern #11) and asserting on the freshly-loaded Status.
	setEnvCreds(t)
	s := tunnelScheme(t)
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns"},
		Spec: v1alpha1.CloudflareTunnelSpec{
			Name:      "cf-ns",
			Connector: v1alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(tn).
		WithStatusSubresource(&v1alpha1.CloudflareTunnel{}).Build()
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

	var got v1alpha1.CloudflareTunnel
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
	setEnvCreds(t)
	s := tunnelScheme(t)
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns"},
		Spec: v1alpha1.CloudflareTunnelSpec{
			Name:      "cf-ns",
			Connector: v1alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(tn).
		WithStatusSubresource(&v1alpha1.CloudflareTunnel{}).Build()
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
	var afterPass1 v1alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "tnl", Namespace: "ns"}, &afterPass1))

	// Second pass: creates tunnel; status.TunnelID becomes set during this
	// reconcile, so seed connectors AFTER the first reconcile creates the
	// tunnel. Run a 2nd reconcile to make sure tunnel is created.
	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"},
	})
	require.NoError(t, err)

	var afterPass2 v1alpha1.CloudflareTunnel
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

	var got v1alpha1.CloudflareTunnel
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
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName}},
		Spec: v1alpha1.CloudflareTunnelSpec{
			Name:      "cf-ns",
			Connector: v1alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(tn).
		WithStatusSubresource(&v1alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	m := mockcf.New()
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"},
	})
	require.NoError(t, err)

	var got v1alpha1.CloudflareTunnel
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

	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName}},
		Spec: v1alpha1.CloudflareTunnelSpec{
			Name:      "cf-ns",
			Connector: v1alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
	}
	winnerSvc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"}}
	loserSvc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns"}}
	base := fake.NewClientBuilder().
		WithScheme(s).WithObjects(tn, winnerSvc, loserSvc).
		WithStatusSubresource(&v1alpha1.CloudflareTunnel{}).Build()
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
	setEnvCreds(t)
	s := tunnelScheme(t)
	m := mockcf.New()

	created, err := m.Tunnel.CreateTunnel(context.Background(), "acct-1",
		cloudflare.CreateTunnelParams{Name: "cf-ns"})
	require.NoError(t, err)
	// Live config has a real ingress entry someone added via the dashboard.
	_, err = m.Tunnel.PutConfiguration(context.Background(), "acct-1", created.ID,
		cloudflare.TunnelConfig{Ingress: []cloudflare.IngressEntry{
			{Hostname: "rogue.example.com", Service: "http://rogue.ns.svc:80"},
		}})
	require.NoError(t, err)

	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName}},
		Spec: v1alpha1.CloudflareTunnelSpec{
			Name:      "cf-ns",
			Connector: v1alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
		Status: v1alpha1.CloudflareTunnelStatus{
			TunnelID: created.ID,
			// Deliberately equal to the resolved catch-all snapshot (empty
			// cache → http_status:404), so wantSnap == ObservedIngress and
			// the existing early-return fires. This isolates the assertion to
			// pure drift detection: no PUT mutates the mock before we check
			// for the DriftDetected event.
			ObservedIngress: []v1alpha1.IngressEntrySnapshot{{Service: "http_status:404"}},
		},
	}
	base := fake.NewClientBuilder().
		WithScheme(s).WithObjects(tn).
		WithStatusSubresource(&v1alpha1.CloudflareTunnel{}).Build()
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
		cloudflare.CreateTunnelParams{Name: "cf-ns"})
	require.NoError(t, err)
	liveCfg := cloudflare.TunnelConfig{Ingress: []cloudflare.IngressEntry{{Service: "http_status:404"}}}
	_, err = m.Tunnel.PutConfiguration(context.Background(), "acct-1", created.ID, liveCfg)
	require.NoError(t, err)

	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName}},
		Spec: v1alpha1.CloudflareTunnelSpec{
			Name:      "cf-ns",
			Connector: v1alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
		Status: v1alpha1.CloudflareTunnelStatus{
			TunnelID: created.ID,
			// Built via the same projection the production code compares against,
			// so live and observed are byte-equal — no drift.
			ObservedIngress: snapshotFromConfig(liveCfg),
		},
	}
	base := fake.NewClientBuilder().
		WithScheme(s).WithObjects(tn).
		WithStatusSubresource(&v1alpha1.CloudflareTunnel{}).Build()
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
		cloudflare.CreateTunnelParams{Name: "cf-ns"})
	require.NoError(t, err)
	_, err = m.Tunnel.PutConfiguration(context.Background(), "acct-1", created.ID,
		cloudflare.TunnelConfig{Ingress: []cloudflare.IngressEntry{
			{Hostname: "live.example.com", Service: "http://live.ns.svc:80"},
		}})
	require.NoError(t, err)

	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName}},
		Spec: v1alpha1.CloudflareTunnelSpec{
			Name:      "cf-ns",
			Connector: v1alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
		Status: v1alpha1.CloudflareTunnelStatus{
			TunnelID: created.ID,
			// ObservedIngress intentionally nil — no baseline yet.
		},
	}
	base := fake.NewClientBuilder().
		WithScheme(s).WithObjects(tn).
		WithStatusSubresource(&v1alpha1.CloudflareTunnel{}).Build()
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
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName},
			Annotations: map[string]string{conventions.AnnotationAutoCreated: "true"},
		},
		Spec: v1alpha1.CloudflareTunnelSpec{
			Name:      "cf-ns",
			Connector: v1alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
		Status: v1alpha1.CloudflareTunnelStatus{
			AttachedSources: []v1alpha1.AttachedSource{
				{Kind: "Service", Namespace: "ns", Name: "c-svc"},
				{Kind: "Service", Namespace: "ns", Name: "b-svc"},
			},
		},
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(tn, svcB, svcC).
		WithStatusSubresource(&v1alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	rec := record.NewFakeRecorder(10)
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())
	r.Recorder = rec

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"}})
	require.NoError(t, err)

	var got v1alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "tnl", Namespace: "ns"}, &got))
	require.Len(t, got.OwnerReferences, 1)
	require.Equal(t, "b-svc", got.OwnerReferences[0].Name)
}

func TestReconcile_OrphanStateFirstObservation_StampsLastOrphanedAt(t *testing.T) {
	setEnvCreds(t)
	s := tunnelScheme(t)
	m := mockcf.New()
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName},
			Annotations: map[string]string{conventions.AnnotationAutoCreated: "true"},
		},
		Spec:   v1alpha1.CloudflareTunnelSpec{Name: "cf-ns", Connector: v1alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30}},
		Status: v1alpha1.CloudflareTunnelStatus{AttachedSources: nil}, // empty -> orphan
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(tn).WithStatusSubresource(&v1alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"}})
	require.NoError(t, err)
	var got v1alpha1.CloudflareTunnel
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
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName},
			Annotations: map[string]string{conventions.AnnotationAutoCreated: "true"},
		},
		Spec:   v1alpha1.CloudflareTunnelSpec{Name: "cf-ns", Connector: v1alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30}},
		Status: v1alpha1.CloudflareTunnelStatus{LastOrphanedAt: &staleStamp},
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(tn).WithStatusSubresource(&v1alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	rec := record.NewFakeRecorder(10)
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())
	r.Recorder = rec
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"}})
	require.NoError(t, err)
	var got v1alpha1.CloudflareTunnel
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

func TestReconcile_OrphanStateClearedOnSourceReattach(t *testing.T) {
	setEnvCreds(t)
	s := tunnelScheme(t)
	m := mockcf.New()
	stamp := metav1.NewTime(time.Now())
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns", UID: "uid-svc"}}
	tn := &v1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tnl", Namespace: "ns", Finalizers: []string{conventions.FinalizerName},
			Annotations:     map[string]string{conventions.AnnotationAutoCreated: "true"},
			OwnerReferences: []metav1.OwnerReference{{Kind: "Service", Name: "svc", UID: "uid-svc", APIVersion: "v1", Controller: ptr.To(true)}},
		},
		Spec: v1alpha1.CloudflareTunnelSpec{Name: "cf-ns", Connector: v1alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30}},
		Status: v1alpha1.CloudflareTunnelStatus{
			LastOrphanedAt:  &stamp,
			AttachedSources: []v1alpha1.AttachedSource{{Kind: "Service", Namespace: "ns", Name: "svc"}},
		},
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(tn, svc).WithStatusSubresource(&v1alpha1.CloudflareTunnel{}).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"}})
	require.NoError(t, err)
	var got v1alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "tnl", Namespace: "ns"}, &got))
	require.Nil(t, got.Status.LastOrphanedAt, "source reattach must clear LastOrphanedAt")
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
