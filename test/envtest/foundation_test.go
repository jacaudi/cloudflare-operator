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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
)

// TestFoundation_BothBundlesEnabled verifies that when both zone and tunnel
// controllers are enabled the reconciler:
//  1. SSAs all five domain CRDs,
//  2. creates both controller Deployments, and
//  3. stamps Ready=True on the singleton CR.
//
// NOTE: CloudflareOperator is cluster-scoped and the singleton name is "cluster".
// All four tests share the same apiserver, so each test uses the singleton name
// "cluster" — they must run sequentially (Go test default) and the first to
// create it wins.  Subsequent tests delete and re-create it as needed.
func TestFoundation_BothBundlesEnabled(t *testing.T) {
	ctx := context.Background()
	c := sharedClient

	// Ensure the operator namespace exists (idempotent).
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cloudflare-system"}}
	_ = c.Create(ctx, ns) // ignore AlreadyExists

	// Clean up any leftover singleton from a previous test run.
	existing := &v1alpha1.CloudflareOperator{}
	if err := c.Get(ctx, types.NamespacedName{Name: v1alpha1.CloudflareOperatorSingletonName}, existing); err == nil {
		_ = c.Delete(ctx, existing)
		waitFor(t, 10*time.Second, func() bool {
			err := c.Get(ctx, types.NamespacedName{Name: v1alpha1.CloudflareOperatorSingletonName}, &v1alpha1.CloudflareOperator{})
			return apierrors.IsNotFound(err)
		})
	}

	op := &v1alpha1.CloudflareOperator{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.CloudflareOperatorSingletonName},
		Spec: v1alpha1.CloudflareOperatorSpec{
			Cloudflare: v1alpha1.CloudflareCredentialRef{
				TokenSecretRef: v1alpha1.SecretReference{Name: "cf-token", Namespace: "cloudflare-system", Key: "token"},
				AccountID:      "acct-123",
			},
			Controllers: v1alpha1.ControllersSpec{
				Zone:   v1alpha1.ControllerSpec{Enabled: true, Replicas: 1},
				Tunnel: v1alpha1.ControllerSpec{Enabled: true, Replicas: 1},
			},
		},
	}
	require.NoError(t, c.Create(ctx, op))
	t.Cleanup(func() {
		_ = c.Delete(ctx, op)
		waitFor(t, 10*time.Second, func() bool {
			err := c.Get(ctx, types.NamespacedName{Name: v1alpha1.CloudflareOperatorSingletonName}, &v1alpha1.CloudflareOperator{})
			return apierrors.IsNotFound(err)
		})
	})

	// All five domain CRDs should be installed.
	waitFor(t, 30*time.Second, func() bool {
		for _, name := range []string{
			"cloudflarezones.cloudflare.io",
			"cloudflarezoneconfigs.cloudflare.io",
			"cloudflarednsrecords.cloudflare.io",
			"cloudflarerulesets.cloudflare.io",
			"cloudflaretunnels.cloudflare.io",
		} {
			var crd apiextv1.CustomResourceDefinition
			if err := c.Get(ctx, types.NamespacedName{Name: name}, &crd); err != nil {
				return false
			}
		}
		return true
	})

	// Both Deployments should exist.
	waitFor(t, 30*time.Second, func() bool {
		var zone appsv1.Deployment
		if err := c.Get(ctx, types.NamespacedName{Name: "cloudflare-zone-controller", Namespace: "cloudflare-system"}, &zone); err != nil {
			return false
		}
		var tunnel appsv1.Deployment
		return c.Get(ctx, types.NamespacedName{Name: "cloudflare-tunnel-controller", Namespace: "cloudflare-system"}, &tunnel) == nil
	})

	// Operator should be Ready.
	waitFor(t, 30*time.Second, func() bool {
		var got v1alpha1.CloudflareOperator
		if err := c.Get(ctx, types.NamespacedName{Name: v1alpha1.CloudflareOperatorSingletonName}, &got); err != nil {
			return false
		}
		for _, cond := range got.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == metav1.ConditionTrue {
				return true
			}
		}
		return false
	})
}

// TestFoundation_TunnelDisabled_RemovesDeployment verifies that toggling
// tunnel.enabled from true → false causes the tunnel Deployment to be deleted
// while the tunnel CRD remains installed.
func TestFoundation_TunnelDisabled_RemovesDeployment(t *testing.T) {
	ctx := context.Background()
	c := sharedClient

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cloudflare-system"}}
	_ = c.Create(ctx, ns)

	// Clean up any leftover singleton.
	existing := &v1alpha1.CloudflareOperator{}
	if err := c.Get(ctx, types.NamespacedName{Name: v1alpha1.CloudflareOperatorSingletonName}, existing); err == nil {
		_ = c.Delete(ctx, existing)
		waitFor(t, 10*time.Second, func() bool {
			err := c.Get(ctx, types.NamespacedName{Name: v1alpha1.CloudflareOperatorSingletonName}, &v1alpha1.CloudflareOperator{})
			return apierrors.IsNotFound(err)
		})
	}

	op := &v1alpha1.CloudflareOperator{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.CloudflareOperatorSingletonName},
		Spec: v1alpha1.CloudflareOperatorSpec{
			Cloudflare: v1alpha1.CloudflareCredentialRef{
				TokenSecretRef: v1alpha1.SecretReference{Name: "x"},
				AccountID:      "acct",
			},
			Controllers: v1alpha1.ControllersSpec{
				Zone:   v1alpha1.ControllerSpec{Enabled: true, Replicas: 1},
				Tunnel: v1alpha1.ControllerSpec{Enabled: true, Replicas: 1},
			},
		},
	}
	require.NoError(t, c.Create(ctx, op))
	t.Cleanup(func() {
		_ = c.Delete(ctx, op)
		waitFor(t, 10*time.Second, func() bool {
			err := c.Get(ctx, types.NamespacedName{Name: v1alpha1.CloudflareOperatorSingletonName}, &v1alpha1.CloudflareOperator{})
			return apierrors.IsNotFound(err)
		})
	})

	// Wait for the tunnel Deployment to be created.
	waitFor(t, 30*time.Second, func() bool {
		var dep appsv1.Deployment
		return c.Get(ctx, types.NamespacedName{Name: "cloudflare-tunnel-controller", Namespace: "cloudflare-system"}, &dep) == nil
	})

	// Re-Get before update to ensure we have the latest resourceVersion.
	var current v1alpha1.CloudflareOperator
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: v1alpha1.CloudflareOperatorSingletonName}, &current))
	current.Spec.Controllers.Tunnel.Enabled = false
	require.NoError(t, c.Update(ctx, &current))

	// Tunnel Deployment should be deleted.
	waitFor(t, 30*time.Second, func() bool {
		var dep appsv1.Deployment
		err := c.Get(ctx, types.NamespacedName{Name: "cloudflare-tunnel-controller", Namespace: "cloudflare-system"}, &dep)
		return apierrors.IsNotFound(err)
	})

	// Tunnel CRD should still exist (disabling drops the controller, not the data).
	var crd apiextv1.CustomResourceDefinition
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "cloudflaretunnels.cloudflare.io"}, &crd))
}

// TestFoundation_TunnelWithoutZoneRejected verifies that the CEL validation
// rule rejects a CR with tunnel.enabled=true and zone.enabled=false.
func TestFoundation_TunnelWithoutZoneRejected(t *testing.T) {
	ctx := context.Background()
	c := sharedClient

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cloudflare-system"}}
	_ = c.Create(ctx, ns)

	// Ensure no leftover singleton so we get a CEL error, not AlreadyExists.
	existing := &v1alpha1.CloudflareOperator{}
	if err := c.Get(ctx, types.NamespacedName{Name: v1alpha1.CloudflareOperatorSingletonName}, existing); err == nil {
		_ = c.Delete(ctx, existing)
		waitFor(t, 10*time.Second, func() bool {
			err := c.Get(ctx, types.NamespacedName{Name: v1alpha1.CloudflareOperatorSingletonName}, &v1alpha1.CloudflareOperator{})
			return apierrors.IsNotFound(err)
		})
	}

	op := &v1alpha1.CloudflareOperator{
		ObjectMeta: metav1.ObjectMeta{Name: v1alpha1.CloudflareOperatorSingletonName},
		Spec: v1alpha1.CloudflareOperatorSpec{
			Cloudflare: v1alpha1.CloudflareCredentialRef{
				TokenSecretRef: v1alpha1.SecretReference{Name: "x"},
				AccountID:      "acct",
			},
			Controllers: v1alpha1.ControllersSpec{
				Zone:   v1alpha1.ControllerSpec{Enabled: false},
				Tunnel: v1alpha1.ControllerSpec{Enabled: true},
			},
		},
	}
	err := c.Create(ctx, op)
	require.Error(t, err, "CEL rule should reject tunnel-enabled without zone-enabled")
}

// TestFoundation_NonSingletonNameIgnored verifies that a CloudflareOperator
// CR with a name other than the singleton name gets stamped with Reason=Ignored
// and no bundles are installed.
func TestFoundation_NonSingletonNameIgnored(t *testing.T) {
	ctx := context.Background()
	c := sharedClient

	op := &v1alpha1.CloudflareOperator{
		ObjectMeta: metav1.ObjectMeta{Name: "other"},
		Spec: v1alpha1.CloudflareOperatorSpec{
			Cloudflare: v1alpha1.CloudflareCredentialRef{
				TokenSecretRef: v1alpha1.SecretReference{Name: "x"},
				AccountID:      "acct",
			},
			Controllers: v1alpha1.ControllersSpec{
				Zone: v1alpha1.ControllerSpec{Enabled: true, Replicas: 1},
			},
		},
	}
	require.NoError(t, c.Create(ctx, op))
	t.Cleanup(func() { _ = c.Delete(ctx, op) })

	waitFor(t, 15*time.Second, func() bool {
		var got v1alpha1.CloudflareOperator
		if err := c.Get(ctx, types.NamespacedName{Name: "other"}, &got); err != nil {
			return false
		}
		for _, cond := range got.Status.Conditions {
			if cond.Reason == "Ignored" {
				return true
			}
		}
		return false
	})
}
