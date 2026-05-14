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
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

func haltTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

func TestHaltDependency_PersistsConditionAndPhase(t *testing.T) {
	s := haltTestScheme(t)
	obj := &v1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "default"},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(obj).
		WithStatusSubresource(&v1alpha1.CloudflareDNSRecord{}).
		Build()

	res, err := HaltDependency(
		context.Background(),
		c, obj,
		&obj.Status.Conditions, &obj.Status.Phase,
		"zone not ready",
		15*time.Second,
	)
	require.NoError(t, err)
	require.Equal(t, 15*time.Second, res.RequeueAfter)

	var got v1alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))
	require.Len(t, got.Status.Conditions, 1)
	require.Equal(t, conventions.ConditionTypeReady, got.Status.Conditions[0].Type)
	require.Equal(t, metav1.ConditionFalse, got.Status.Conditions[0].Status)
	require.Equal(t, conventions.ReasonDependencyMissing, got.Status.Conditions[0].Reason)
	require.Equal(t, "zone not ready", got.Status.Conditions[0].Message)
	require.Equal(t, v1alpha1.PhaseError, got.Status.Phase)
}

func TestHaltDependency_ZeroRequeueFallsBackToDefault(t *testing.T) {
	s := haltTestScheme(t)
	obj := &v1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "default"},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(obj).
		WithStatusSubresource(&v1alpha1.CloudflareDNSRecord{}).
		Build()

	res, err := HaltDependency(
		context.Background(),
		c, obj,
		&obj.Status.Conditions, &obj.Status.Phase,
		"halt",
		0,
	)
	require.NoError(t, err)
	require.Equal(t, DefaultRequeueAfter, res.RequeueAfter)
}
