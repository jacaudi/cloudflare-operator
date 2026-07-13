/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package envtest_test

// Envtest coverage for simplify finding H: the hash-gate in ensureDataplane.
//
// Before the fix: ensureDataplane called reconcilelib.Apply (a SSA patch
// round-trip) for the cloudflared Deployment AND the metrics Service on every
// tunnel reconcile, regardless of whether those objects had changed.
//
// After the fix: sha256 is computed over the built Deployment + Service; the
// Apply is skipped when the hash matches Status.ObservedDataplaneDeploymentHash
// / Status.ObservedDataplaneServiceHash. The new Status fields are stamped only
// after a successful Apply.
//
// Non-vacuity guarantee: the test wraps the controller's client in a
// dataplaneCountingClient that counts Patch calls targeting Deployment or
// Service GVKs. After conditions are stable (first reconcile done, hashes
// stamped), the counter is reset and a second reconcile is triggered via a
// no-op annotation patch on the tunnel CR. The assertion is that the counter
// does NOT increment during pass 2 — meaning no Apply was issued for the
// Deployment or Service.
//
// Without the gate: Apply fires every reconcile → counter increments.
// With the gate:   hash matches → Apply skipped → counter stays 0.

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
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	mockcf "github.com/jacaudi/cloudflare-operator/internal/cloudflare/mock"
	"github.com/jacaudi/cloudflare-operator/internal/controller/tunnel"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

// dataplaneCountingClient wraps a controller-runtime client and counts Patch
// calls targeting Deployment (apps/v1) or Service (core/v1) GVKs. All other
// calls are passed through unmodified.
type dataplaneCountingClient struct {
	client.Client
	count *atomic.Int64
}

func newDataplaneCountingClient(base client.Client) *dataplaneCountingClient {
	return &dataplaneCountingClient{Client: base, count: new(atomic.Int64)}
}

// Patch intercepts Apply patches (SSA round-trips) for Deployment + Service
// objects and increments the counter before delegating.
func (c *dataplaneCountingClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	// All client.Object values implement runtime.Object (which embeds
	// GetObjectKind), so inspect the GVK directly. ensureDataplane calls
	// reconcilelib.Apply which stamps TypeMeta on the built objects.
	switch obj.GetObjectKind().GroupVersionKind().Kind {
	case "Deployment", "Service":
		c.count.Add(1)
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

// resetCount atomically resets the call counter to 0.
func (c *dataplaneCountingClient) resetCount() { c.count.Store(0) }

// dataplaneApplyCount returns the current Patch-apply count for Deployment/Service.
func (c *dataplaneCountingClient) dataplaneApplyCount() int64 { return c.count.Load() }

// setupDataplaneHashEnv wires a CloudflareTunnelReconciler with a
// dataplaneCountingClient so Deployment + Service Apply calls are observable.
// The reconciler is given a mock CF client (tunnel create/adopt is a no-op
// but returns a deterministic tunnelID).
func setupDataplaneHashEnv(t *testing.T) (client.Client, *dataplaneCountingClient, *mockcf.Mock, string) {
	t.Helper()

	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	sch := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(sch))
	utilruntime.Must(v2alpha1.AddToScheme(sch))

	// Start from an empty cluster: earlier tests' CRs outlive them in the
	// shared apiserver and every manager watches cluster-wide.
	purgeCloudflareCRs(t)

	mgr, err := ctrl.NewManager(sharedConfig, ctrl.Options{
		Scheme:  sch,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	require.NoError(t, err)

	m := mockcf.New()
	cc := newDataplaneCountingClient(mgr.GetClient())

	tunnelR := &tunnel.CloudflareTunnelReconciler{
		Client:   cc,
		Scheme:   sch,
		Recorder: mgr.GetEventRecorderFor("cloudflare-operator-tunnel-dataplane-hash-test"),
		TunnelClientFn: func(_ cloudflare.Credentials) (cloudflare.TunnelClient, error) {
			return m.Tunnel, nil
		},
		Cache:        tunnelsynth.NewCache(),
		DefaultImage: tunnel.DefaultCloudflaredImage,
	}
	require.NoError(t, ctrl.NewControllerManagedBy(mgr).
		Named("cloudflaretunnel-"+sanitizeTestName(t.Name())).
		For(&v2alpha1.CloudflareTunnel{}).
		Complete(tunnelR))

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

	return mgr.GetClient(), cc, m, ns
}

// TestEnvtest_Tunnel_EnsureDataplane_SkipApplyWhenHashMatches asserts that
// after the first reconcile stamps ObservedDataplaneDeploymentHash +
// ObservedDataplaneServiceHash, a second reconcile triggered by a no-op
// annotation patch does NOT issue Apply patches for the Deployment or Service.
//
// RED: without the hash-gate, Apply fires every reconcile → counter increments.
// GREEN: gate suppresses Apply when hash matches → counter stays 0.
func TestEnvtest_Tunnel_EnsureDataplane_SkipApplyWhenHashMatches(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	c, cc, _, ns := setupDataplaneHashEnv(t)
	ctx := context.Background()

	tn := makeTunnel("tnl", ns)
	require.NoError(t, c.Create(ctx, tn))

	// Pass 1: wait for ObservedDataplaneDeploymentHash + ObservedDataplaneServiceHash
	// to be stamped. This confirms the first reconcile ran ensureDataplane
	// and successfully applied + hashed both objects.
	require.Eventually(t, func() bool {
		var got v2alpha1.CloudflareTunnel
		if err := c.Get(ctx, types.NamespacedName{Name: "tnl", Namespace: ns}, &got); err != nil {
			return false
		}
		return got.Status.ObservedDataplaneDeploymentHash != "" &&
			got.Status.ObservedDataplaneServiceHash != ""
	}, 20*time.Second, 250*time.Millisecond,
		"first reconcile must stamp ObservedDataplaneDeploymentHash + ObservedDataplaneServiceHash")

	// Quiesce: give the controller time to settle so in-flight reconciles
	// triggered by the Create event or the Status.Update observation fully
	// complete before we reset the counter.
	time.Sleep(1 * time.Second)

	// Reset the Deployment+Service Patch counter. Any call after this point is pass-2.
	cc.resetCount()

	// Pass 2: trigger a second reconcile by patching a no-op annotation on the
	// tunnel CR. The spec and connector config are unchanged, so the hashes
	// will match and ensureDataplane should skip both Apply calls.
	var tnLive v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "tnl", Namespace: ns}, &tnLive))
	patch := client.MergeFrom(tnLive.DeepCopy())
	if tnLive.Annotations == nil {
		tnLive.Annotations = map[string]string{}
	}
	tnLive.Annotations["test.cloudflare.io/reconcile-probe"] = "1"
	require.NoError(t, c.Patch(ctx, &tnLive, patch))

	// Wait long enough for the second reconcile to run and complete.
	// Without the gate: Apply fires for Deployment + Service → count ≥ 2.
	// With the gate:    Apply is skipped → count == 0.
	time.Sleep(3 * time.Second)

	applyCount := cc.dataplaneApplyCount()
	require.Equal(t, int64(0), applyCount,
		"ensureDataplane must NOT Apply Deployment or Service on pass 2 when hashes match "+
			"(hash-gate should suppress the SSA round-trips); apply-count=%d", applyCount)
}
