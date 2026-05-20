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
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	cloudflare "github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	mockcf "github.com/jacaudi/cloudflare-operator/internal/cloudflare/mock"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	reconcilelib "github.com/jacaudi/cloudflare-operator/internal/reconcile"
	"github.com/jacaudi/cloudflare-operator/internal/tunnelsynth"
)

// ──────────────────────────────────────────────────────────────────────────────
// CloudflareTunnel – force-reconcile
// ──────────────────────────────────────────────────────────────────────────────

// TestReconcile_ForceReconcile_Tunnel_BypassesNoDriftShortCircuit seeds a
// tunnel whose ObservedIngress already matches the desired config (the
// applyRemoteConfig drift-skip fires normally). Sets the annotation without
// an ack. Expects:
//   - PutConfiguration is called despite no config drift (drift-skip bypassed).
//   - status.lastReconcileToken == "tkn-1".
func TestReconcile_ForceReconcile_Tunnel_BypassesNoDriftShortCircuit(t *testing.T) {
	setEnvCreds(t)
	s := tunnelScheme(t)
	m := mockcf.New()

	// Create a real tunnel and seed the config to be already in sync.
	created, err := m.Tunnel.CreateTunnel(context.Background(), "acct-1",
		cloudflare.CreateTunnelParams{Name: "ns"})
	require.NoError(t, err)
	_, err = m.Tunnel.PutConfiguration(context.Background(), "acct-1", created.ID,
		cloudflare.TunnelConfig{Ingress: []cloudflare.IngressEntry{{Service: "http_status:404"}}})
	require.NoError(t, err)

	putCallsBefore := m.Calls("Tunnel.PutConfiguration")

	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "tnl",
			Namespace:   "ns",
			Finalizers:  []string{conventions.FinalizerName},
			Annotations: map[string]string{conventions.AnnotationReconcileAt: "tkn-1"},
		},
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
		WithScheme(s).
		WithObjects(tn).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).
		Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"},
	})
	require.NoError(t, err)

	putCallsAfter := m.Calls("Tunnel.PutConfiguration")
	require.Greater(t, putCallsAfter, putCallsBefore,
		"force-reconcile must bypass drift-skip and call PutConfiguration")

	var got v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "tnl", Namespace: "ns"}, &got))
	require.Equal(t, "tkn-1", got.Status.LastReconcileToken, "ack must be written after force-reconcile")
}

// TestReconcile_ForceReconcile_Tunnel_AlreadyAcked_NoEffect seeds the same
// no-drift scenario but with the ack already matching the annotation token.
// Expects PutConfiguration is NOT called (drift-skip fires) and the ack remains stable.
func TestReconcile_ForceReconcile_Tunnel_AlreadyAcked_NoEffect(t *testing.T) {
	setEnvCreds(t)
	s := tunnelScheme(t)
	m := mockcf.New()

	created, err := m.Tunnel.CreateTunnel(context.Background(), "acct-1",
		cloudflare.CreateTunnelParams{Name: "ns"})
	require.NoError(t, err)
	_, err = m.Tunnel.PutConfiguration(context.Background(), "acct-1", created.ID,
		cloudflare.TunnelConfig{Ingress: []cloudflare.IngressEntry{{Service: "http_status:404"}}})
	require.NoError(t, err)

	putCallsBefore := m.Calls("Tunnel.PutConfiguration")

	tn := &v2alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "tnl",
			Namespace:   "ns",
			Finalizers:  []string{conventions.FinalizerName},
			Annotations: map[string]string{conventions.AnnotationReconcileAt: "tkn-1"},
		},
		Spec: v2alpha1.CloudflareTunnelSpec{
			Name:      "ns",
			Connector: v2alpha1.ConnectorSpec{Replicas: 2, Protocol: "auto", LogLevel: "info", GracePeriodSeconds: 30},
		},
		Status: v2alpha1.CloudflareTunnelStatus{
			TunnelID:           created.ID,
			ObservedIngress:    []v2alpha1.IngressEntrySnapshot{{Service: "http_status:404"}},
			LastReconcileToken: "tkn-1", // already acked
		},
	}

	base := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(tn).
		WithStatusSubresource(&v2alpha1.CloudflareTunnel{}).
		Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	r := newTunnelReconciler(t, c, s, m, tunnelsynth.NewCache())

	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "tnl", Namespace: "ns"},
	})
	require.NoError(t, err)

	putCallsAfter := m.Calls("Tunnel.PutConfiguration")
	require.Equal(t, putCallsBefore, putCallsAfter,
		"already-acked drift-skip must not call PutConfiguration")

	var got v2alpha1.CloudflareTunnel
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "tnl", Namespace: "ns"}, &got))
	require.Equal(t, "tkn-1", got.Status.LastReconcileToken)
}
