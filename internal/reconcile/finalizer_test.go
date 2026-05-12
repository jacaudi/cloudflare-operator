package reconcile

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

func TestEnsureFinalizer_Adds(t *testing.T) {
	obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "x"}}
	changed := EnsureFinalizer(obj, conventions.FinalizerName)
	require.True(t, changed)
	require.Contains(t, obj.GetFinalizers(), conventions.FinalizerName)
}

func TestEnsureFinalizer_NoChangeIfPresent(t *testing.T) {
	obj := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Finalizers: []string{conventions.FinalizerName}},
	}
	changed := EnsureFinalizer(obj, conventions.FinalizerName)
	require.False(t, changed)
}

func TestRemoveFinalizer(t *testing.T) {
	obj := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "x", Finalizers: []string{conventions.FinalizerName, "other"}},
	}
	changed := RemoveFinalizer(obj, conventions.FinalizerName)
	require.True(t, changed)
	require.NotContains(t, obj.GetFinalizers(), conventions.FinalizerName)
	require.Contains(t, obj.GetFinalizers(), "other")
}
