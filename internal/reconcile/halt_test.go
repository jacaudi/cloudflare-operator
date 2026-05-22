/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
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

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

func haltTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, v2alpha1.AddToScheme(s))
	return s
}

func TestHaltDependency_PersistsConditionAndPhase(t *testing.T) {
	s := haltTestScheme(t)
	obj := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "default"},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(obj).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
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

	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))
	require.Len(t, got.Status.Conditions, 1)
	require.Equal(t, conventions.ConditionTypeReady, got.Status.Conditions[0].Type)
	require.Equal(t, metav1.ConditionFalse, got.Status.Conditions[0].Status)
	require.Equal(t, conventions.ReasonDependencyMissing, got.Status.Conditions[0].Reason)
	require.Equal(t, "zone not ready", got.Status.Conditions[0].Message)
	require.Equal(t, v2alpha1.PhaseError, got.Status.Phase)
}

func TestHaltDependency_ZeroRequeueFallsBackToDefault(t *testing.T) {
	s := haltTestScheme(t)
	obj := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "default"},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(obj).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
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

func TestHaltWith_SetsConditionAndWritesStatus(t *testing.T) {
	s := haltTestScheme(t)
	obj := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "default"},
		Status: v2alpha1.CloudflareDNSRecordStatus{
			Conditions: SetReady(nil, metav1.ConditionTrue, conventions.ReasonReady, "all good"),
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(obj).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()

	res, err := HaltWith(
		context.Background(),
		c, obj,
		&obj.Status.Conditions, &obj.Status.Phase,
		conventions.ReasonAdoptRefusedForeign,
		"TXT companion claims a different owner; refusing adoption",
		1*time.Second,
	)
	require.NoError(t, err)
	require.Equal(t, 1*time.Second, res.RequeueAfter)

	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))
	require.Len(t, got.Status.Conditions, 1)
	require.Equal(t, conventions.ConditionTypeReady, got.Status.Conditions[0].Type)
	require.Equal(t, metav1.ConditionFalse, got.Status.Conditions[0].Status)
	require.Equal(t, conventions.ReasonAdoptRefusedForeign, got.Status.Conditions[0].Reason)
	require.Equal(t, "TXT companion claims a different owner; refusing adoption", got.Status.Conditions[0].Message)
	require.Equal(t, v2alpha1.PhaseError, got.Status.Phase)
}

func TestHaltWith_PropagatesUpdateError(t *testing.T) {
	s := haltTestScheme(t)
	// Object is NOT registered in the fake client — Status().Update will fail with not-found.
	obj := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "missing", Namespace: "default"},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()

	_, err := HaltWith(
		context.Background(),
		c, obj,
		&obj.Status.Conditions, &obj.Status.Phase,
		conventions.ReasonDegraded,
		"something failed",
		DefaultRequeueAfter,
	)
	require.Error(t, err)
}
