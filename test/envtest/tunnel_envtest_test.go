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

import (
	"context"
	"errors"
	"strconv"
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
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	mockcf "github.com/jacaudi/cloudflare-operator/internal/cloudflare/mock"
	"github.com/jacaudi/cloudflare-operator/internal/controller/tunnel"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

// tunnelEnvFixture is the per-test fixture returned by setupTunnelEnv. It
// wires only the CloudflareTunnelReconciler (no source reconcilers) so the
// envtest API server doesn't need gateway-api CRDs installed — the three
// acceptance scenarios here (create, drift-skip, finalizer-drain) all
// exercise the tunnel reconciler in isolation. Source reconcilers get their
// own envtests in T16–T18.
type tunnelEnvFixture struct {
	c    client.Client
	mock *mockcf.Mock
	// ns is a unique per-test namespace so the package-shared envtest API
	// server doesn't fight over CR / Deployment / Secret names across tests.
	ns string
}

// setupTunnelEnv builds a per-test manager backed by the package-shared
// envtest config (suite_test.go). It wires the CloudflareTunnelReconciler
// inline so the same mock instance is reachable from the test for assertions
// on Cloudflare-side state (GetConfiguration / GetTunnel / SeedConnections).
func setupTunnelEnv(t *testing.T) *tunnelEnvFixture {
	t.Helper()

	// reconcile.LoadCredentialsHierarchical falls back to env-var creds when
	// the CR has no override — the tunnel reconciler calls it on each
	// Reconcile. AccountID flows through to the mock as "acct-1".
	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	sch := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(sch))
	utilruntime.Must(v2alpha1.AddToScheme(sch))

	mgr, err := ctrl.NewManager(sharedConfig, ctrl.Options{
		Scheme:  sch,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	require.NoError(t, err)

	m := mockcf.New()

	tunnelR := &tunnel.CloudflareTunnelReconciler{
		Client:   mgr.GetClient(),
		Scheme:   sch,
		Recorder: mgr.GetEventRecorderFor("cloudflare-operator-tunnel-test"),
		TunnelClientFn: func(_ cloudflare.Credentials) (cloudflare.TunnelClient, error) {
			return m.Tunnel, nil
		},
		Cache:        tunnelsynth.NewCache(),
		DefaultImage: tunnel.DefaultCloudflaredImage,
	}
	// Unique controller name per test — the controller-runtime metrics
	// registry is global to the process and rejects duplicate names. t.Name()
	// gives each test (and sub-test) its own slot.
	require.NoError(t, ctrl.NewControllerManagedBy(mgr).
		Named("cloudflaretunnel-"+t.Name()).
		For(&v2alpha1.CloudflareTunnel{}).
		Complete(tunnelR))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = mgr.Start(ctx) }()

	syncCtx, syncCancel := context.WithTimeout(ctx, 30*time.Second)
	defer syncCancel()
	require.True(t, mgr.GetCache().WaitForCacheSync(syncCtx), "manager cache failed to sync")

	// Build a unique namespace for this test. Lower-cased, DNS-1123-safe.
	ns := uniqueNamespace(t)
	require.NoError(t, mgr.GetClient().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}))

	return &tunnelEnvFixture{c: mgr.GetClient(), mock: m, ns: ns}
}

// uniqueNamespace returns a deterministic, DNS-1123-compliant namespace name
// derived from the test name + a short timestamp. Repeated runs of the
// package against the same envtest binary collide on namespace names
// otherwise.
func uniqueNamespace(t *testing.T) string {
	t.Helper()
	// Lowercase + replace '/' (sub-test separator) + '_' (Go-style names)
	// with '-' so the namespace is DNS-1123 valid.
	name := strings.ToLower(t.Name())
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	suffix := strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)
	out := name + "-" + suffix
	// DNS-1123 caps at 63 chars; trim from the head if needed (keep the
	// timestamp tail so collisions across runs stay unlikely).
	if len(out) > 63 {
		out = out[len(out)-63:]
	}
	return out
}

// makeTunnel returns a minimal valid CloudflareTunnel CR with the spec
// defaults the implementation expects (Replicas=2, Protocol=auto, ...).
func makeTunnel(name, namespace string) *v2alpha1.CloudflareTunnel {
	return &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name: name,
			Connector: v2alpha1.ConnectorSpec{
				Replicas:           2,
				Protocol:           "auto",
				LogLevel:           "info",
				GracePeriodSeconds: 30,
			},
		},
	}
}

// TestTunnelEnvtest_CreatePopulatesStatusAndDataplane covers design §12.1:
// create the CR, eventually Status.TunnelID + Status.TunnelCNAME populate and
// the operator-owned dataplane (Deployment, token Secret, metrics Service)
// exists. Validates the create+dataplane half of the happy path through the
// envtest API server (CEL validation included, Phase-2 lesson #3).
func TestTunnelEnvtest_CreatePopulatesStatusAndDataplane(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupTunnelEnv(t)
	ctx := context.Background()

	tn := makeTunnel("tnl", f.ns)
	require.NoError(t, f.c.Create(ctx, tn))

	require.Eventually(t, func() bool {
		var got v2alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Name: "tnl", Namespace: f.ns}, &got); err != nil {
			return false
		}
		return got.Status.TunnelID != "" && got.Status.TunnelCNAME != ""
	}, 10*time.Second, 200*time.Millisecond, "Status.TunnelID + Status.TunnelCNAME populated")

	// Dataplane resources owned by the tunnel.
	require.Eventually(t, func() bool {
		var dep appsv1.Deployment
		return f.c.Get(ctx, types.NamespacedName{Name: "cloudflared-tnl", Namespace: f.ns}, &dep) == nil
	}, 10*time.Second, 200*time.Millisecond, "cloudflared Deployment exists")

	var sec corev1.Secret
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Name: "cloudflared-token-tnl", Namespace: f.ns}, &sec))
	require.NotEmpty(t, sec.Data["token"], "token Secret carries connector token")

	var svc corev1.Service
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Name: "cloudflared-tnl-metrics", Namespace: f.ns}, &svc))
}

// TestTunnelEnvtest_DriftSkipsPut covers the PUT-once invariant: the first
// reconcile PUTs the (empty-contributions + catch-all) ingress config; a
// second reconcile triggered by an annotation touch must NOT bump the
// configuration version, because Status.ObservedIngress already matches the
// computed snapshot. With no source reconcilers running, contributions stay
// empty and the snapshot is stable across reconciles.
func TestTunnelEnvtest_DriftSkipsPut(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupTunnelEnv(t)
	ctx := context.Background()

	tn := makeTunnel("tnl", f.ns)
	require.NoError(t, f.c.Create(ctx, tn))

	// Wait for the first reconcile to populate TunnelID AND ObservedIngress
	// (the latter is what enables drift-skip on the second pass).
	require.Eventually(t, func() bool {
		var got v2alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Name: "tnl", Namespace: f.ns}, &got); err != nil {
			return false
		}
		return got.Status.TunnelID != "" && got.Status.ObservedIngress != nil
	}, 10*time.Second, 200*time.Millisecond, "first reconcile populates TunnelID + ObservedIngress")

	var got v2alpha1.CloudflareTunnel
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Name: "tnl", Namespace: f.ns}, &got))
	tunnelID := got.Status.TunnelID

	// Let the controller settle: wait until ObservedGeneration catches the
	// current generation AND the configuration version stops bumping. The
	// first reconcile pass may PUT once on tunnel-create then PUT again on
	// the status-update watch event before ObservedIngress is persisted —
	// drift-skip only kicks in once both sides agree on the snapshot. We
	// capture the stable version as the baseline.
	var baselineVersion int
	require.Eventually(t, func() bool {
		var x v2alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Name: "tnl", Namespace: f.ns}, &x); err != nil {
			return false
		}
		if x.Status.ObservedGeneration != x.Generation {
			return false
		}
		cfg, err := f.mock.Tunnel.GetConfiguration(ctx, "acct-1", tunnelID)
		if err != nil || cfg.Version == 0 {
			return false
		}
		// Read twice with a short gap; if version is stable, the controller
		// has stopped re-PUTting (drift-skip is in effect).
		time.Sleep(500 * time.Millisecond)
		cfg2, err := f.mock.Tunnel.GetConfiguration(ctx, "acct-1", tunnelID)
		if err != nil {
			return false
		}
		if cfg.Version != cfg2.Version {
			return false
		}
		baselineVersion = cfg2.Version
		return true
	}, 15*time.Second, 250*time.Millisecond, "controller settled on stable PUT version")

	// Touch the CR with an annotation to trigger a fresh reconcile. No spec
	// change → ObservedIngress already matches → applyRemoteConfig returns
	// early before the PUT.
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Name: "tnl", Namespace: f.ns}, &got))
	if got.Annotations == nil {
		got.Annotations = map[string]string{}
	}
	got.Annotations["trigger"] = "1"
	require.NoError(t, f.c.Update(ctx, &got))

	// Give the controller time to observe + reconcile the update. 2s is
	// generous: the watch event lands in <100ms in envtest.
	time.Sleep(2 * time.Second)

	cfg2, err := f.mock.Tunnel.GetConfiguration(ctx, "acct-1", tunnelID)
	require.NoError(t, err)
	require.Equal(t, baselineVersion, cfg2.Version,
		"drift-skip means PUT version stays at %d after no-spec-change reconcile", baselineVersion)
}

// TestTunnelEnvtest_FinalizerDrainSequence covers design §12.7: the
// finalizer drain runs scale → DeleteConnections → DeleteTunnel → drop
// finalizer, even when a connector is still registered at delete time. In
// envtest there's no kubelet, so the Deployment's status replicas stay at
// zero and the drain immediately advances past steps 1+2. Seeding a
// connection exercises the DeleteConnections call (without it, the mock's
// DeleteTunnel would still succeed but we wouldn't be testing the drain).
func TestTunnelEnvtest_FinalizerDrainSequence(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupTunnelEnv(t)
	ctx := context.Background()

	tn := makeTunnel("tnl", f.ns)
	require.NoError(t, f.c.Create(ctx, tn))

	require.Eventually(t, func() bool {
		var got v2alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Name: "tnl", Namespace: f.ns}, &got); err != nil {
			return false
		}
		return got.Status.TunnelID != ""
	}, 10*time.Second, 200*time.Millisecond, "tunnel created on Cloudflare side")

	var got v2alpha1.CloudflareTunnel
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Name: "tnl", Namespace: f.ns}, &got))
	tunnelID := got.Status.TunnelID

	// Seed an active connection so step 3 (DeleteConnections) has real work
	// to do; the mock's DeleteTunnel would otherwise refuse if we tried to
	// short-circuit step 3.
	f.mock.Tunnel.SeedConnections(tunnelID, []cloudflare.TunnelConnection{
		{ID: "c1", ColoName: "DEN"},
	})

	require.NoError(t, f.c.Delete(ctx, &got))

	// CR finalizer is dropped only after the full drain succeeds.
	require.Eventually(t, func() bool {
		var x v2alpha1.CloudflareTunnel
		err := f.c.Get(ctx, types.NamespacedName{Name: "tnl", Namespace: f.ns}, &x)
		return apierrors.IsNotFound(err)
	}, 30*time.Second, 500*time.Millisecond, "CR removed after finalizer drain")

	// Cloudflare-side tunnel is gone (mock returns the dual-sentinel
	// wrapping cloudflare.ErrTunnelNotFound).
	_, err := f.mock.Tunnel.GetTunnel(ctx, "acct-1", tunnelID)
	require.Error(t, err)
	require.True(t, errors.Is(err, cloudflare.ErrTunnelNotFound),
		"DeleteTunnel removed the tunnel on Cloudflare; got %v", err)
}
