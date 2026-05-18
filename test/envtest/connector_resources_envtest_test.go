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

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/controller/tunnel"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

// TestEnvtest_AutoCreatedTunnelCarriesConnectorResources is an integration
// regression-lock for Task 7 of the tunnel-crossns-connector feature (F2).
// It calls tunnel.EnsureTunnelCR directly — the same function the
// GatewaySourceReconciler (and all source reconcilers) invoke on the create
// path — with a non-empty defaults.Resources value, then asserts:
//
//  1. The returned CloudflareTunnel carries Spec.Connector.Resources equal to
//     the configured value (CPU request + memory limit).
//  2. The CR is stamped conventions.AnnotationAutoCreated = "true".
//
// Non-vacuity: removing the `Connector: defaults` assignment from
// EnsureTunnelCR (or zeroing defaults.Resources before passing it) would leave
// Spec.Connector.Resources empty, failing both resource assertions below.
// This test therefore catches any regression in the F2 wiring path that
// prevents chart-configured connector resources from reaching auto-created
// CloudflareTunnel CRs.
//
// Approach — direct EnsureTunnelCR (no-manager fallback):
// The existing Gateway-source envtest (setupGatewayEnv) wires its
// GatewaySourceReconciler with a fixed DefaultConnector hard-coded in the
// fixture; there is no per-test Options injection point that would let us
// vary DefaultConnector.Resources without duplicating the whole manager setup.
// Calling EnsureTunnelCR directly is the plan-documented fallback and is
// strictly more targeted: it exercises the exact create-path logic that seeds
// Spec.Connector from defaults, without the overhead of a full manager loop.
func TestEnvtest_AutoCreatedTunnelCarriesConnectorResources(t *testing.T) {
	if sharedConfig == nil {
		t.Skip("envtest not initialized (KUBEBUILDER_ASSETS unset)")
	}

	ctx := context.Background()

	// Unique namespace so this test cannot collide with any sibling test in the
	// shared-process suite (the envtest apiserver is shared across all tests;
	// there is no per-test GC). shortUniqueNamespace is the suite-standard helper.
	ns := shortUniqueNamespace(t)
	require.NoError(t, sharedClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}))
	t.Cleanup(func() { _ = sharedClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}) })

	// Create a Service as the tunnel owner. EnsureTunnelCR calls
	// reconcilelib.SetControllerOwner (controllerutil.SetControllerReference)
	// which requires a non-zero UID — achieved by a real Create + re-Get.
	// Service is in clientgoscheme (registered in sharedScheme) so GVK
	// resolution succeeds without a per-test scheme.
	owner := &corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: "t7-svc", Namespace: ns},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: 443}},
		},
	}
	if err := sharedClient.Create(ctx, owner); err != nil && !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}
	require.NoError(t, sharedClient.Get(ctx, client.ObjectKeyFromObject(owner), owner))
	t.Cleanup(func() { _ = sharedClient.Delete(ctx, owner) })

	// Configure connector resources — the value the chart would inject via
	// --tunnel-connector-resources → tunnel.Options.DefaultConnector.Resources.
	// All other ConnectorSpec fields are set to valid non-zero values to satisfy
	// the CRD's minimum/enum validators (Replicas ≥ 1, Protocol in enum, etc.).
	wantResources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
	}
	defaults := v2alpha1.ConnectorSpec{
		Replicas:           2,
		Protocol:           "auto",
		LogLevel:           "info",
		GracePeriodSeconds: 30,
		Resources:          wantResources,
	}

	// Derive the tunnel name (cf-<ns>-t7-tunnel) via the same helper the
	// production reconciler calls. Using a test-unique tunnel-name annotation
	// ("t7-tunnel") avoids any name collision with other suites.
	derivedName, err := tunnel.DeriveTunnelName(ns, "t7-tunnel")
	require.NoError(t, err)

	// EnsureTunnelCR is the exact production entry-point called by every source
	// reconciler on the auto-create path. It seeds Spec.Connector from defaults
	// on CREATE and stamps AnnotationAutoCreated.
	created, err := tunnel.EnsureTunnelCR(ctx, sharedClient, sharedScheme, owner, "Service", derivedName, defaults)
	require.NoError(t, err)
	require.NotNil(t, created)
	t.Cleanup(func() { _ = sharedClient.Delete(ctx, created) })

	// Re-fetch from the apiserver to confirm what was actually persisted,
	// including any admission-webhook defaulting.
	var tn v2alpha1.CloudflareTunnel
	require.NoError(t, sharedClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: derivedName}, &tn))

	// Core assertions — the F2 regression lock.
	require.Equal(t, "true", tn.Annotations[conventions.AnnotationAutoCreated],
		"auto-created annotation must be stamped on CREATE")
	require.Equal(t,
		wantResources.Requests.Cpu().String(),
		tn.Spec.Connector.Resources.Requests.Cpu().String(),
		"Spec.Connector.Resources.Requests.cpu must equal the configured default")
	require.Equal(t,
		wantResources.Limits.Memory().String(),
		tn.Spec.Connector.Resources.Limits.Memory().String(),
		"Spec.Connector.Resources.Limits.memory must equal the configured default")
}
