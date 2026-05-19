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

package reconcile

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

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

func TestHasSourceLabels(t *testing.T) {
	all := &unstructured.Unstructured{}
	all.SetLabels(map[string]string{
		conventions.LabelSourceKind:      "Gateway",
		conventions.LabelSourceName:      "gw",
		conventions.LabelSourceNamespace: "net",
	})
	require.True(t, HasSourceLabels(all))

	none := &unstructured.Unstructured{}
	require.False(t, HasSourceLabels(none))

	partial := &unstructured.Unstructured{}
	partial.SetLabels(map[string]string{conventions.LabelSourceKind: "Gateway"})
	require.False(t, HasSourceLabels(partial), "partial source labels must NOT count as managed")
}
