/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	reconcilelib "github.com/jacaudi/cloudflare-operator/internal/reconcile"
)

func TestMetaReconciler_EnsureCreatesEnabledDeletesDisabled(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	// The fake client does not natively support server-side apply, which
	// MetaReconciler.ensure uses via reconcile.Apply; wrap it with the
	// project's SSATranslatingClient (same pattern as controller_test.go).
	base := fake.NewClientBuilder().WithScheme(scheme).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	r := &MetaReconciler{Client: c, Scheme: scheme, Config: Config{
		OperatorNamespace: "cf-sys",
		OperatorImage:     "img:1",
		ZoneEnabled:       true,
		ZoneReplicas:      2,
		TunnelEnabled:     false,
	}}
	require.NoError(t, r.ensure(context.Background()))

	var zone appsv1.Deployment
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "cloudflare-zone-controller", Namespace: "cf-sys"}, &zone))
	require.Equal(t, int32(2), *zone.Spec.Replicas)

	var tun appsv1.Deployment
	err := c.Get(context.Background(),
		types.NamespacedName{Name: "cloudflare-tunnel-controller", Namespace: "cf-sys"}, &tun)
	require.True(t, apierrors.IsNotFound(err), "disabled bundle Deployment must be absent")
}

func TestMetaReconciler_NegativeReplicasClamped(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	base := fake.NewClientBuilder().WithScheme(scheme).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	r := &MetaReconciler{Client: c, Scheme: scheme, Config: Config{
		OperatorNamespace: "cf-sys",
		OperatorImage:     "img:1",
		ZoneEnabled:       true,
		ZoneReplicas:      -1,
		TunnelEnabled:     false,
	}}
	require.NoError(t, r.ensure(context.Background()))

	var zone appsv1.Deployment
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "cloudflare-zone-controller", Namespace: "cf-sys"}, &zone))
	require.Equal(t, int32(1), *zone.Spec.Replicas, "negative replica count must be clamped to 1")
}

func TestMetaReconciler_EnabledToDisabledDeletesDeployment(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, appsv1.AddToScheme(scheme))
	base := fake.NewClientBuilder().WithScheme(scheme).Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	r := &MetaReconciler{Client: c, Scheme: scheme, Config: Config{
		OperatorNamespace: "cf-sys",
		OperatorImage:     "img:1",
		ZoneEnabled:       true,
		ZoneReplicas:      1,
	}}

	// Step 1: enabled -> Deployment exists.
	require.NoError(t, r.ensure(context.Background()))
	var zone appsv1.Deployment
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "cloudflare-zone-controller", Namespace: "cf-sys"}, &zone))

	// Step 2: same reconciler, now disabled -> Deployment deleted (drift-correction).
	r.Config.ZoneEnabled = false
	require.NoError(t, r.ensure(context.Background()))
	err := c.Get(context.Background(),
		types.NamespacedName{Name: "cloudflare-zone-controller", Namespace: "cf-sys"}, &zone)
	require.True(t, apierrors.IsNotFound(err),
		"previously-enabled bundle Deployment must be deleted once disabled")
}
