/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package reconcile_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	reconcilelib "github.com/jacaudi/cloudflare-operator/internal/reconcile"
)

func TestSSATranslatingClient_CreatesWhenMissing(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	base := fake.NewClientBuilder().WithScheme(s).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	cm := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "ns"},
		Data:       map[string]string{"k": "v"},
	}
	require.NoError(t, reconcilelib.Apply(context.Background(), c, cm))

	var got corev1.ConfigMap
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: "cm", Namespace: "ns"}, &got))
	require.Equal(t, "v", got.Data["k"])
}

func TestSSATranslatingClient_UpdatesWhenExists(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	existing := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "ns"},
		Data:       map[string]string{"k": "old"},
	}
	base := fake.NewClientBuilder().WithScheme(s).WithObjects(existing).Build()
	c := reconcilelib.SSATranslatingClient(t, base)

	cm := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "ns"},
		Data:       map[string]string{"k": "new"},
	}
	require.NoError(t, reconcilelib.Apply(context.Background(), c, cm))

	var got corev1.ConfigMap
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: "cm", Namespace: "ns"}, &got))
	require.Equal(t, "new", got.Data["k"])
}
