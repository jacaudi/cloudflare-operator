package reconcile

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

func TestStampSourceLabels(t *testing.T) {
	obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "x"}}
	StampSourceLabels(obj, "CloudflareTunnel", "tunnel-a", "ns-a")
	require.Equal(t, "CloudflareTunnel", obj.Labels[conventions.LabelSourceKind])
	require.Equal(t, "tunnel-a", obj.Labels[conventions.LabelSourceName])
	require.Equal(t, "ns-a", obj.Labels[conventions.LabelSourceNamespace])
}

func TestVerifySourceLabels_AllPresent(t *testing.T) {
	obj := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "x",
			Labels: map[string]string{
				conventions.LabelSourceKind:      "k",
				conventions.LabelSourceName:      "n",
				conventions.LabelSourceNamespace: "ns",
			},
		},
	}
	require.NoError(t, VerifySourceLabels(obj))
}

func TestVerifySourceLabels_NoneIsOK(t *testing.T) {
	// Hand-written CR with no source labels: legal.
	obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "x"}}
	require.NoError(t, VerifySourceLabels(obj))
}

func TestVerifySourceLabels_PartialIsHardFail(t *testing.T) {
	obj := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "x",
			Labels: map[string]string{
				conventions.LabelSourceKind: "k", // name + namespace missing
			},
		},
	}
	err := VerifySourceLabels(obj)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrPartialSourceLabels)
}
