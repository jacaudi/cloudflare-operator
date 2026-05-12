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

package zone

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare"
	"github.com/jacaudi/cloudflare-operator/internal/cloudflare/mock"
	"github.com/jacaudi/cloudflare-operator/internal/conventions"
)

func zoneTestScheme(t *testing.T) *runtime.Scheme {
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

type zoneTestFixture struct {
	c      client.Client
	mock   *mock.Mock
	reconc *CloudflareZoneReconciler
	scheme *runtime.Scheme
}

func newZoneFixture(t *testing.T, objs ...client.Object) *zoneTestFixture {
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	s := zoneTestScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.CloudflareZone{}).
		Build()
	m := mock.New()
	return &zoneTestFixture{
		c: c, mock: m, scheme: s,
		reconc: &CloudflareZoneReconciler{
			Client:       c,
			Scheme:       s,
			ZoneClientFn: func(_ cloudflare.Credentials) (cloudflare.ZoneClient, error) { return m.Zone, nil },
		},
	}
}

func TestZone_CreateFlow(t *testing.T) {
	z := &v1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default"},
		Spec:       v1alpha1.CloudflareZoneSpec{Name: "example.com", Type: "full", DeletionPolicy: v1alpha1.DeletionPolicyRetain},
	}
	f := newZoneFixture(t, z)
	_, err := f.reconc.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "example", Namespace: "default"}})
	require.NoError(t, err)

	var got v1alpha1.CloudflareZone
	require.NoError(t, f.c.Get(context.Background(), types.NamespacedName{Name: "example", Namespace: "default"}, &got))
	// First Reconcile sets the finalizer and requeues. Re-reconcile to run the
	// create flow.
	require.Contains(t, got.Finalizers, conventions.FinalizerName)
	_, err = f.reconc.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "example", Namespace: "default"}})
	require.NoError(t, err)
	require.NoError(t, f.c.Get(context.Background(), types.NamespacedName{Name: "example", Namespace: "default"}, &got))
	require.NotEmpty(t, got.Status.ZoneID, "ZoneID populated")
	require.Contains(t, []string{"pending", "active"}, got.Status.Status)
	require.Len(t, got.Status.NameServers, 2)
}

func TestZone_ActivationCheckPokesPending(t *testing.T) {
	m := mock.New()
	z := &v1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default", Finalizers: []string{conventions.FinalizerName}},
		Spec:       v1alpha1.CloudflareZoneSpec{Name: "example.com", Type: "full", DeletionPolicy: v1alpha1.DeletionPolicyRetain},
	}
	created, _ := m.Zone.CreateZone(context.Background(), "acct-1", cloudflare.ZoneParams{Name: "example.com"})
	z.Status.ZoneID = created.ID
	z.Status.Status = "pending"
	s := zoneTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(z).WithStatusSubresource(&v1alpha1.CloudflareZone{}).Build()
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	r := &CloudflareZoneReconciler{Client: c, Scheme: s,
		ZoneClientFn: func(_ cloudflare.Credentials) (cloudflare.ZoneClient, error) { return m.Zone, nil },
	}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "example", Namespace: "default"}})
	require.NoError(t, err)

	var got v1alpha1.CloudflareZone
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "example", Namespace: "default"}, &got))
	require.Equal(t, "active", got.Status.Status)
}

func TestZone_DeleteWithRetain_KeepsZone(t *testing.T) {
	m := mock.New()
	created, _ := m.Zone.CreateZone(context.Background(), "acct-1", cloudflare.ZoneParams{Name: "example.com"})
	now := metav1.Now()
	z := &v1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{
			Name: "example", Namespace: "default",
			Finalizers:        []string{conventions.FinalizerName},
			DeletionTimestamp: &now,
		},
		Spec:   v1alpha1.CloudflareZoneSpec{Name: "example.com", Type: "full", DeletionPolicy: v1alpha1.DeletionPolicyRetain},
		Status: v1alpha1.CloudflareZoneStatus{ZoneID: created.ID, Status: "active"},
	}
	s := zoneTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(z).WithStatusSubresource(&v1alpha1.CloudflareZone{}).Build()
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	r := &CloudflareZoneReconciler{Client: c, Scheme: s,
		ZoneClientFn: func(_ cloudflare.Credentials) (cloudflare.ZoneClient, error) { return m.Zone, nil },
	}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "example", Namespace: "default"}})
	require.NoError(t, err)

	got, err := m.Zone.GetZone(context.Background(), created.ID)
	require.NoError(t, err)
	require.Equal(t, "example.com", got.Name)
}

func TestZone_DeleteWithDelete_RemovesZone(t *testing.T) {
	m := mock.New()
	created, _ := m.Zone.CreateZone(context.Background(), "acct-1", cloudflare.ZoneParams{Name: "example.com"})
	now := metav1.Now()
	z := &v1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{
			Name: "example", Namespace: "default",
			Finalizers:        []string{conventions.FinalizerName},
			DeletionTimestamp: &now,
		},
		Spec:   v1alpha1.CloudflareZoneSpec{Name: "example.com", Type: "full", DeletionPolicy: v1alpha1.DeletionPolicyDelete},
		Status: v1alpha1.CloudflareZoneStatus{ZoneID: created.ID, Status: "active"},
	}
	s := zoneTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(z).WithStatusSubresource(&v1alpha1.CloudflareZone{}).Build()
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	r := &CloudflareZoneReconciler{Client: c, Scheme: s,
		ZoneClientFn: func(_ cloudflare.Credentials) (cloudflare.ZoneClient, error) { return m.Zone, nil },
	}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "example", Namespace: "default"}})
	require.NoError(t, err)
	_, err = m.Zone.GetZone(context.Background(), created.ID)
	require.Error(t, err, "zone deleted on CF side")
}
