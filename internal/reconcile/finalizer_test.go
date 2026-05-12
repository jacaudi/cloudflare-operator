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
