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

package bootstrap

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
	reconcilelib "github.com/jacaudi/cloudflare-operator/internal/reconcile"
)

func newBootstrapScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	require.NoError(t, appsv1.AddToScheme(s))
	require.NoError(t, apiextv1.AddToScheme(s))
	return s
}

func TestReconcile_RejectsNonClusterName(t *testing.T) {
	op := &v1alpha1.CloudflareOperator{ObjectMeta: metav1.ObjectMeta{Name: "other"}}
	s := newBootstrapScheme(t)
	base := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(op).
		WithStatusSubresource(&v1alpha1.CloudflareOperator{}).
		Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	r := &Reconciler{Client: c, Scheme: s, OperatorNamespace: "cf", OperatorImage: "img:1"}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "other"}})
	require.NoError(t, err)

	var got v1alpha1.CloudflareOperator
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "other"}, &got))
	require.Len(t, got.Status.Conditions, 1)
	require.Equal(t, conventions.ReasonIgnored, got.Status.Conditions[0].Reason)
}

func TestReconcile_BothBundlesEnabled_CreatesAll(t *testing.T) {
	op := &v1alpha1.CloudflareOperator{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.CloudflareOperatorSingletonName},
		Spec: v1alpha1.CloudflareOperatorSpec{
			Cloudflare: v1alpha1.CloudflareCredentialRef{
				TokenSecretRef: v1alpha1.SecretReference{Name: "t"},
				AccountID:      "acct",
			},
			Controllers: v1alpha1.ControllersSpec{
				Zone:   v1alpha1.ControllerSpec{Enabled: true, Replicas: 1},
				Tunnel: v1alpha1.ControllerSpec{Enabled: true, Replicas: 1},
			},
		},
	}
	s := newBootstrapScheme(t)
	base := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(op).
		WithStatusSubresource(&v1alpha1.CloudflareOperator{}).
		Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	r := &Reconciler{Client: c, Scheme: s, OperatorNamespace: "cf", OperatorImage: "img:1"}
	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: v1alpha1.CloudflareOperatorSingletonName}})
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, res)

	var zoneDep appsv1.Deployment
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "cloudflare-zone-controller", Namespace: "cf"}, &zoneDep))
	var tunnelDep appsv1.Deployment
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "cloudflare-tunnel-controller", Namespace: "cf"}, &tunnelDep))

	var crds apiextv1.CustomResourceDefinitionList
	require.NoError(t, c.List(context.Background(), &crds))
	require.Len(t, crds.Items, 5)
}

func TestReconcile_TunnelDisabled_DeletesTunnelDeployment(t *testing.T) {
	existing := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "cloudflare-tunnel-controller", Namespace: "cf"},
	}
	op := &v1alpha1.CloudflareOperator{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.CloudflareOperatorSingletonName},
		Spec: v1alpha1.CloudflareOperatorSpec{
			Cloudflare: v1alpha1.CloudflareCredentialRef{
				TokenSecretRef: v1alpha1.SecretReference{Name: "t"},
				AccountID:      "acct",
			},
			Controllers: v1alpha1.ControllersSpec{
				Zone:   v1alpha1.ControllerSpec{Enabled: true, Replicas: 1},
				Tunnel: v1alpha1.ControllerSpec{Enabled: false},
			},
		},
	}
	s := newBootstrapScheme(t)
	base := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(op, existing).
		WithStatusSubresource(&v1alpha1.CloudflareOperator{}).
		Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	r := &Reconciler{Client: c, Scheme: s, OperatorNamespace: "cf", OperatorImage: "img:1"}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: v1alpha1.CloudflareOperatorSingletonName}})
	require.NoError(t, err)

	var dep appsv1.Deployment
	err = c.Get(context.Background(), types.NamespacedName{Name: "cloudflare-tunnel-controller", Namespace: "cf"}, &dep)
	require.True(t, apierrors.IsNotFound(err), "tunnel Deployment should be deleted: %v", err)
}

func TestBundleMembership_MatchesBundleKinds(t *testing.T) {
	for _, b := range AllBundles() {
		gvks := bundleKinds(b)
		files := bundleMembership(b)
		require.Equal(t, len(gvks), len(files),
			"bundle %s: gvks count %d != filenames count %d", b, len(gvks), len(files))
		// Spot-check that derivation matches the expected on-disk pattern.
		for i, gvk := range gvks {
			expected := "crds/" + gvk.Group + "_" + strings.ToLower(gvk.Kind) + "s.yaml"
			require.Equal(t, expected, files[i],
				"bundle %s: expected filename %q for kind %s, got %q",
				b, expected, gvk.Kind, files[i])
		}
	}
}

func TestReconcile_StaleCRSweep_OnDisable(t *testing.T) {
	zone := &v1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "z1", Namespace: "media"},
	}
	op := &v1alpha1.CloudflareOperator{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.CloudflareOperatorSingletonName},
		Spec: v1alpha1.CloudflareOperatorSpec{
			Cloudflare: v1alpha1.CloudflareCredentialRef{
				TokenSecretRef: v1alpha1.SecretReference{Name: "t"},
				AccountID:      "acct",
			},
			Controllers: v1alpha1.ControllersSpec{
				Zone:   v1alpha1.ControllerSpec{Enabled: false}, // already disabled — triggers sweep
				Tunnel: v1alpha1.ControllerSpec{Enabled: false},
			},
		},
	}
	s := newBootstrapScheme(t)
	base := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(op, zone).
		WithStatusSubresource(
			&v1alpha1.CloudflareOperator{},
			&v1alpha1.CloudflareZone{},
		).
		Build()
	c := reconcilelib.SSATranslatingClient(t, base)
	r := &Reconciler{Client: c, Scheme: s, OperatorNamespace: "cf", OperatorImage: "img:1"}
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: v1alpha1.CloudflareOperatorSingletonName}})
	require.NoError(t, err)

	var got v1alpha1.CloudflareZone
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "z1", Namespace: "media"}, &got))
	var sawOffline bool
	for _, cond := range got.Status.Conditions {
		if cond.Reason == conventions.ReasonControllerOffline {
			sawOffline = true
			break
		}
	}
	require.True(t, sawOffline, "expected ControllerOffline condition on stale Zone CR")
}
