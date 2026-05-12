package reconcile

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestApply_UsesServerSideApplyWithFieldManager(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	type captured struct {
		called     bool
		patchType  types.PatchType
		fieldOwner string
		forceOwner bool
		gotObjName string
	}
	var got captured

	base := fake.NewClientBuilder().WithScheme(scheme).Build()
	c := interceptor.NewClient(base, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			got.called = true
			got.patchType = patch.Type()
			got.gotObjName = obj.GetName()
			po := &client.PatchOptions{}
			po.ApplyOptions(opts)
			if po.FieldManager != "" {
				got.fieldOwner = po.FieldManager
			}
			if po.Force != nil && *po.Force {
				got.forceOwner = true
			}
			return nil
		},
	})

	cm := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "default"},
		Data:       map[string]string{"k": "v"},
	}
	require.NoError(t, Apply(context.Background(), c, cm))

	require.True(t, got.called, "Apply must invoke Patch")
	require.Equal(t, types.ApplyPatchType, got.patchType, "Apply must use SSA patch type")
	require.Equal(t, FieldManager, got.fieldOwner, "Apply must use the FieldManager constant")
	require.True(t, got.forceOwner, "Apply must force ownership")
	require.Equal(t, "x", got.gotObjName, "Apply must pass through the object")
}

func TestApply_PropagatesPatchError(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	sentinel := errBoom

	base := fake.NewClientBuilder().WithScheme(scheme).Build()
	c := interceptor.NewClient(base, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			return sentinel
		},
	})

	cm := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "default"},
	}
	err := Apply(context.Background(), c, cm)
	require.ErrorIs(t, err, sentinel)
}

var errBoom = boomError("boom")

type boomError string

func (e boomError) Error() string { return string(e) }
