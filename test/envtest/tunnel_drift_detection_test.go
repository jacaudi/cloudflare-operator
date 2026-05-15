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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// TestEnvtest_TunnelDriftDetection pins the Design-E2 out-of-band drift
// detection against a real apiserver. The CloudflareTunnel reconciler's
// applyRemoteConfig asks Cloudflare for the live tunnel config and, when it
// diverges from Status.ObservedIngress, emits a single DriftDetected Warning
// Event. This is detection only — no forced re-push.
//
// Scenario: a tunnel CR settles with a populated Status (TunnelID +
// non-empty ObservedIngress). We then mutate the mock's live config out of
// band (simulating a dashboard edit) and trigger a fresh reconcile. The
// reconciler must record a DriftDetected Event on the tunnel CR, observed
// via the apiserver Events API (the manager's EventRecorder writes Events to
// the real API server in envtest).
func TestEnvtest_TunnelDriftDetection(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}
	f := setupTunnelEnv(t)
	ctx := context.Background()

	tn := makeTunnel("tnl", f.ns)
	require.NoError(t, f.c.Create(ctx, tn))

	// Wait for the first reconcile to populate TunnelID + ObservedIngress —
	// the populated baseline the drift guard requires before it engages.
	require.Eventually(t, func() bool {
		var got v1alpha1.CloudflareTunnel
		if err := f.c.Get(ctx, types.NamespacedName{Name: "tnl", Namespace: f.ns}, &got); err != nil {
			return false
		}
		return got.Status.TunnelID != "" && len(got.Status.ObservedIngress) > 0
	}, 15*time.Second, 250*time.Millisecond, "first reconcile populates TunnelID + ObservedIngress")

	var got v1alpha1.CloudflareTunnel
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Name: "tnl", Namespace: f.ns}, &got))
	tunnelID := got.Status.TunnelID

	// Out-of-band edit: replace the live config with an ingress entry the
	// operator never pushed (simulating a dashboard / external-tool change).
	// snapshotFromConfig(live) will now differ from Status.ObservedIngress.
	_, err := f.mock.Tunnel.PutConfiguration(ctx, "acct-1", tunnelID,
		cloudflare.TunnelConfig{Ingress: []cloudflare.IngressEntry{
			{Hostname: "rogue.example.com", Service: "http://rogue.svc:80"},
			{Service: "http_status:404"},
		}})
	require.NoError(t, err)

	// Trigger a fresh reconcile via an annotation touch (no spec change).
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Name: "tnl", Namespace: f.ns}, &got))
	if got.Annotations == nil {
		got.Annotations = map[string]string{}
	}
	got.Annotations["trigger"] = "drift"
	require.NoError(t, f.c.Update(ctx, &got))

	// The reconcile must record a DriftDetected Warning Event on the tunnel
	// CR. The manager's EventRecorder persists Events to the apiserver, so we
	// observe them by listing core/v1 Events in the test namespace and
	// matching on Reason + InvolvedObject.
	require.Eventually(t, func() bool {
		var events corev1.EventList
		if err := f.c.List(ctx, &events, client.InNamespace(f.ns)); err != nil {
			return false
		}
		for i := range events.Items {
			ev := &events.Items[i]
			if ev.Reason == conventions.ReasonDriftDetected &&
				ev.InvolvedObject.Kind == "CloudflareTunnel" &&
				ev.InvolvedObject.Name == "tnl" &&
				ev.Type == corev1.EventTypeWarning {
				return true
			}
		}
		return false
	}, 20*time.Second, 250*time.Millisecond,
		"out-of-band live-config edit must produce a DriftDetected Warning Event on the tunnel CR")
}
