/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package reconcile

import (
	"context"
	"fmt"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// SSATranslatingClient wraps a controller-runtime fake client so that
// server-side-apply Patch calls are translated into Create-or-Update. The
// fake client does not natively support SSA; without this wrapper, unit
// tests that exercise reconcilers using Apply() would fail with a
// "patch type ApplyPatchType not supported" error.
//
// Real SSA semantics (e.g. field-manager ownership conflicts) are NOT
// emulated — those are covered by the envtest suite under test/envtest/.
// This helper exists solely to make reconciler unit tests exercise the same
// Apply call path without spinning up a real apiserver.
//
// Usage:
//
//	base := fake.NewClientBuilder().WithScheme(s).Build()
//	c    := reconcile.SSATranslatingClient(t, base)
//	// pass c to the reconciler under test.
//
// The test parameter is used for t.Helper() so failures point at the caller.
func SSATranslatingClient(t *testing.T, base client.WithWatch) client.WithWatch {
	t.Helper()
	return interceptor.NewClient(base, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if patch.Type() != types.ApplyPatchType {
				return c.Patch(ctx, obj, patch, opts...)
			}
			key := client.ObjectKeyFromObject(obj)
			existing, ok := obj.DeepCopyObject().(client.Object)
			if !ok {
				return fmt.Errorf("SSATranslatingClient: DeepCopyObject did not produce client.Object")
			}
			err := c.Get(ctx, key, existing)
			if apierrors.IsNotFound(err) {
				return c.Create(ctx, obj)
			}
			if err != nil {
				return err
			}
			obj.SetResourceVersion(existing.GetResourceVersion())
			return c.Update(ctx, obj)
		},
	})
}
