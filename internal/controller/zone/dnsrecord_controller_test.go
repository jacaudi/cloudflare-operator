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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
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
	"github.com/jacaudi/cloudflare-operator/internal/ipresolver"
)

// newDNSReconciler builds the reconciler with a mock-backed DNSClient and a
// default IPResolver. When providers are passed, the resolver uses them
// instead of the production defaults — required for the DynamicIP test which
// points at an httptest server.
func newDNSReconciler(_ *testing.T, c client.Client, scheme *runtime.Scheme, m *mock.Mock, providers ...string) *CloudflareDNSRecordReconciler {
	var resOpts []ipresolver.Option
	if len(providers) > 0 {
		resOpts = append(resOpts, ipresolver.WithProviders(providers))
	}
	return &CloudflareDNSRecordReconciler{
		Client:      c,
		Scheme:      scheme,
		DNSClientFn: func(_ cloudflare.Credentials) (cloudflare.DNSClient, error) { return m.DNS, nil },
		IPResolver:  ipresolver.NewResolver(resOpts...),
	}
}

func TestDNS_CreateAllTypes(t *testing.T) {
	cases := []struct {
		name, ty string
		setSpec  func(*v1alpha1.CloudflareDNSRecordSpec)
	}{
		{"A", "A", func(s *v1alpha1.CloudflareDNSRecordSpec) { c := "192.0.2.1"; s.Content = &c }},
		{"AAAA", "AAAA", func(s *v1alpha1.CloudflareDNSRecordSpec) { c := "2001:db8::1"; s.Content = &c }},
		{"CNAME", "CNAME", func(s *v1alpha1.CloudflareDNSRecordSpec) { c := "target.example.com"; s.Content = &c }},
		{"TXT", "TXT", func(s *v1alpha1.CloudflareDNSRecordSpec) { c := "v=spf1 -all"; s.Content = &c }},
		{"NS", "NS", func(s *v1alpha1.CloudflareDNSRecordSpec) { c := "ns1.example.org"; s.Content = &c }},
		{"MX", "MX", func(s *v1alpha1.CloudflareDNSRecordSpec) {
			c := "mail.example.com"
			p := 10
			s.Content = &c
			s.Priority = &p
		}},
		{"SRV", "SRV", func(s *v1alpha1.CloudflareDNSRecordSpec) {
			s.SRVData = &v1alpha1.SRVData{Service: "_satisfactory", Proto: "_tcp", Priority: 0, Weight: 10, Port: 7777, Target: "game.example.com"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := zoneTestScheme(t)
			rec := &v1alpha1.CloudflareDNSRecord{
				ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "default"},
				Spec:       v1alpha1.CloudflareDNSRecordSpec{Name: "app.example.com", Type: tc.ty, ZoneID: "z1"},
			}
			tc.setSpec(&rec.Spec)
			t.Setenv("CLOUDFLARE_API_TOKEN", "t")
			t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
			c := fake.NewClientBuilder().WithScheme(s).WithObjects(rec).WithStatusSubresource(&v1alpha1.CloudflareDNSRecord{}).Build()
			m := mock.New()
			r := newDNSReconciler(t, c, s, m)
			// Converge: finalizer-set requeue, then create.
			_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
			require.NoError(t, err)
			_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
			require.NoError(t, err)
			var got v1alpha1.CloudflareDNSRecord
			require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))
			require.NotEmpty(t, got.Status.RecordID)
			require.Equal(t, v1alpha1.PhaseReady, got.Status.Phase)
		})
	}
}

func TestDNS_DriftDetected_TriggersUpdate(t *testing.T) {
	s := zoneTestScheme(t)
	content := "192.0.2.1"
	rec := &v1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "default", Finalizers: []string{conventions.FinalizerName}},
		Spec:       v1alpha1.CloudflareDNSRecordSpec{Name: "app.example.com", Type: "A", Content: &content, ZoneID: "z1"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rec).WithStatusSubresource(&v1alpha1.CloudflareDNSRecord{}).Build()
	m := mock.New()
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	// Seed the mock with a record whose content has drifted from the spec.
	existing, _ := m.DNS.CreateRecord(context.Background(), "z1", cloudflare.DNSRecordParams{Name: "app.example.com", Type: "A", Content: "192.0.2.2", TTL: 1})
	rec.Status.RecordID = existing.ID
	rec.Status.CurrentContent = "192.0.2.2"
	require.NoError(t, c.Status().Update(context.Background(), rec))
	r := newDNSReconciler(t, c, s, m)
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)
	var got v1alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))
	require.Equal(t, "192.0.2.1", got.Status.CurrentContent, "reconciler corrects drift")
}

func TestDNS_AdoptBareTakeover(t *testing.T) {
	// adopt: true + an existing matching (name, type) record on CF — take it
	// over without TXT verification (TXT registry is deferred this phase).
	s := zoneTestScheme(t)
	content := "192.0.2.50"
	rec := &v1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "default"},
		Spec: v1alpha1.CloudflareDNSRecordSpec{
			Name: "app.example.com", Type: "A", Content: &content, ZoneID: "z1", Adopt: true,
		},
	}
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rec).WithStatusSubresource(&v1alpha1.CloudflareDNSRecord{}).Build()
	m := mock.New()
	// Pre-existing record at the same (name, type) — bare adopt should take it.
	existing, _ := m.DNS.CreateRecord(context.Background(), "z1", cloudflare.DNSRecordParams{Name: "app.example.com", Type: "A", Content: "1.1.1.1", TTL: 1})
	r := newDNSReconciler(t, c, s, m)
	// Converge.
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)
	var got v1alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))
	require.Equal(t, existing.ID, got.Status.RecordID, "adopted by (name, type) match — same record ID")
	require.Equal(t, "192.0.2.50", got.Status.CurrentContent, "drift corrected after adopt")
}

func TestDNS_AdoptNoMatch_CreatesNew(t *testing.T) {
	// adopt: true but no matching record on CF — fall through to Create.
	s := zoneTestScheme(t)
	content := "192.0.2.50"
	rec := &v1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "default"},
		Spec: v1alpha1.CloudflareDNSRecordSpec{
			Name: "app.example.com", Type: "A", Content: &content, ZoneID: "z1", Adopt: true,
		},
	}
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rec).WithStatusSubresource(&v1alpha1.CloudflareDNSRecord{}).Build()
	m := mock.New()
	r := newDNSReconciler(t, c, s, m)
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)
	var got v1alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))
	require.NotEmpty(t, got.Status.RecordID, "fell through to create")
	require.Equal(t, "192.0.2.50", got.Status.CurrentContent)
}

func TestDNS_DynamicIP_ResolvesAndWritesA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("198.51.100.7"))
	}))
	defer srv.Close()
	s := zoneTestScheme(t)
	rec := &v1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "default"},
		Spec:       v1alpha1.CloudflareDNSRecordSpec{Name: "apex.example.com", Type: "A", DynamicIP: true, ZoneID: "z1"},
	}
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rec).WithStatusSubresource(&v1alpha1.CloudflareDNSRecord{}).Build()
	m := mock.New()
	r := newDNSReconciler(t, c, s, m, srv.URL)
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)
	var got v1alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))
	require.Equal(t, "198.51.100.7", got.Status.CurrentContent)
}

func TestDNS_Delete_RemovesUpstream(t *testing.T) {
	now := metav1.Now()
	s := zoneTestScheme(t)
	content := "192.0.2.1"
	rec := &v1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name: "rec", Namespace: "default",
			Finalizers:        []string{conventions.FinalizerName},
			DeletionTimestamp: &now,
		},
		Spec: v1alpha1.CloudflareDNSRecordSpec{Name: "app.example.com", Type: "A", Content: &content, ZoneID: "z1"},
	}
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rec).WithStatusSubresource(&v1alpha1.CloudflareDNSRecord{}).Build()
	m := mock.New()
	created, _ := m.DNS.CreateRecord(context.Background(), "z1", cloudflare.DNSRecordParams{Name: "app.example.com", Type: "A", Content: "192.0.2.1"})
	rec.Status.RecordID = created.ID
	require.NoError(t, c.Status().Update(context.Background(), rec))
	r := newDNSReconciler(t, c, s, m)
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)
	_, err = m.DNS.GetRecord(context.Background(), "z1", created.ID)
	require.Error(t, err, "record deleted on CF side")
}
