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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
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
		setSpec  func(*v2alpha1.CloudflareDNSRecordSpec)
	}{
		{"A", "A", func(s *v2alpha1.CloudflareDNSRecordSpec) { c := "192.0.2.1"; s.Content = &c }},
		{"AAAA", "AAAA", func(s *v2alpha1.CloudflareDNSRecordSpec) { c := "2001:db8::1"; s.Content = &c }},
		{"CNAME", "CNAME", func(s *v2alpha1.CloudflareDNSRecordSpec) { c := "target.example.com"; s.Content = &c }},
		{"TXT", "TXT", func(s *v2alpha1.CloudflareDNSRecordSpec) { c := "v=spf1 -all"; s.Content = &c }},
		{"NS", "NS", func(s *v2alpha1.CloudflareDNSRecordSpec) { c := "ns1.example.org"; s.Content = &c }},
		{"MX", "MX", func(s *v2alpha1.CloudflareDNSRecordSpec) {
			c := "mail.example.com"
			p := 10
			s.Content = &c
			s.Priority = &p
		}},
		{"SRV", "SRV", func(s *v2alpha1.CloudflareDNSRecordSpec) {
			s.SRVData = &v2alpha1.SRVData{Service: "_satisfactory", Proto: "_tcp", Priority: 0, Weight: 10, Port: 7777, Target: "game.example.com"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := zoneTestScheme(t)
			rec := &v2alpha1.CloudflareDNSRecord{
				ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "default"},
				Spec:       v2alpha1.CloudflareDNSRecordSpec{Name: "app.example.com", Type: tc.ty, ZoneID: "z1"},
			}
			tc.setSpec(&rec.Spec)
			t.Setenv("CLOUDFLARE_API_TOKEN", "t")
			t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
			c := fake.NewClientBuilder().WithScheme(s).WithObjects(rec).WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).Build()
			m := mock.New()
			r := newDNSReconciler(t, c, s, m)
			// Converge: finalizer-set requeue, then create.
			_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
			require.NoError(t, err)
			_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
			require.NoError(t, err)
			var got v2alpha1.CloudflareDNSRecord
			require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))
			require.NotEmpty(t, got.Status.RecordID)
			require.Equal(t, v2alpha1.PhaseReady, got.Status.Phase)
		})
	}
}

func TestDNS_DriftDetected_TriggersUpdate(t *testing.T) {
	s := zoneTestScheme(t)
	content := "192.0.2.1"
	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "default", Finalizers: []string{conventions.FinalizerName}},
		Spec:       v2alpha1.CloudflareDNSRecordSpec{Name: "app.example.com", Type: "A", Content: &content, ZoneID: "z1"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rec).WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).Build()
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
	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))
	require.Equal(t, "192.0.2.1", got.Status.CurrentContent, "reconciler corrects drift")
}

func TestDNS_AdoptBareTakeover(t *testing.T) {
	// adopt: true + an existing matching (name, type) record on CF but NO TXT
	// companion — TXT-verified adoption refuses the takeover (design §2 Q2).
	// The old bare-takeover behavior is superseded by TXT-verified adoption.
	s := zoneTestScheme(t)
	content := "192.0.2.50"
	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "default"},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Name: "app.example.com", Type: "A", Content: &content, ZoneID: "z1", Adopt: true,
		},
	}
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rec).WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).Build()
	m := mock.New()
	// Pre-existing A record with no TXT companion — adoption must be refused.
	_, _ = m.DNS.CreateRecord(context.Background(), "z1", cloudflare.DNSRecordParams{Name: "app.example.com", Type: "A", Content: "1.1.1.1", TTL: 1})
	createBefore := m.Calls("DNS.CreateRecord")
	r := newDNSReconciler(t, c, s, m)
	// Finalizer pass.
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)
	// Adopt attempt — must refuse.
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)
	require.Equal(t, createBefore, m.Calls("DNS.CreateRecord"), "must not backfill a TXT for a pre-existing record")
	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))
	require.Empty(t, got.Status.RecordID, "adoption must be refused — no TXT companion")
	cond := findReadyCondition(got.Status.Conditions)
	require.NotNil(t, cond)
	require.Equal(t, metav1.ConditionFalse, cond.Status)
	require.Equal(t, conventions.ReasonAdoptRefusedNoTXT, cond.Reason)
}

func TestDNS_AdoptNoMatch_CreatesNew(t *testing.T) {
	// adopt: true but no matching record on CF — fall through to Create.
	s := zoneTestScheme(t)
	content := "192.0.2.50"
	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "default"},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Name: "app.example.com", Type: "A", Content: &content, ZoneID: "z1", Adopt: true,
		},
	}
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rec).WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).Build()
	m := mock.New()
	r := newDNSReconciler(t, c, s, m)
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)
	var got v2alpha1.CloudflareDNSRecord
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
	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "default"},
		Spec:       v2alpha1.CloudflareDNSRecordSpec{Name: "apex.example.com", Type: "A", DynamicIP: true, ZoneID: "z1"},
	}
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rec).WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).Build()
	m := mock.New()
	r := newDNSReconciler(t, c, s, m, srv.URL)
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)
	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))
	require.Equal(t, "198.51.100.7", got.Status.CurrentContent)
}

// TestDNS_NoDrift_NoUpdate locks in the contract that when an observed
// record exactly matches the spec, the reconciler does NOT call
// UpdateRecord. We assert this by injecting a countingErr on UpdateRecord:
// any invocation increments the counter (and would surface as a non-nil
// reconcile error).
func TestDNS_NoDrift_NoUpdate(t *testing.T) {
	s := zoneTestScheme(t)
	content := "192.0.2.1"
	proxied := false
	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "default", Finalizers: []string{conventions.FinalizerName}},
		Spec:       v2alpha1.CloudflareDNSRecordSpec{Name: "app.example.com", Type: "A", Content: &content, ZoneID: "z1", TTL: 1, Proxied: &proxied},
	}
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rec).WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).Build()
	m := mock.New()
	// Seed the mock with a record that exactly matches spec (name, type,
	// content, TTL, proxied).
	existing, _ := m.DNS.CreateRecord(context.Background(), "z1", cloudflare.DNSRecordParams{Name: "app.example.com", Type: "A", Content: "192.0.2.1", TTL: 1, Proxied: &proxied})
	rec.Status.RecordID = existing.ID
	rec.Status.CurrentContent = "192.0.2.1"
	require.NoError(t, c.Status().Update(context.Background(), rec))

	// Inject an error into UpdateRecord. If the reconciler tries to update,
	// the error fires and increments the counter.
	calls := 0
	m.InjectError("DNS.UpdateRecord", &countingErr{calls: &calls})
	r := newDNSReconciler(t, c, s, m)
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err, "reconcile with no drift should not call UpdateRecord")
	require.Zero(t, calls, "UpdateRecord must not be called when observed matches spec")
}

// TestReconcile_TxtRegistryKeyUnavailable_Halts verifies that when the
// CloudflareOperator singleton references a TXT-registry key Secret that does
// not exist, the reconciler halts with a TxtRegistryKeyUnavailable Ready=False
// condition and does NOT return an error (graceful halt + requeue).
func TestReconcile_TxtRegistryKeyUnavailable_Halts(t *testing.T) {
	s := zoneTestScheme(t)

	// CloudflareOperator/cluster with TxtRegistryKeySecretRef → a Secret that
	// does NOT exist.
	op := &v2alpha1.CloudflareOperator{
		ObjectMeta: metav1.ObjectMeta{Name: v2alpha1.CloudflareOperatorSingletonName},
		Spec: v2alpha1.CloudflareOperatorSpec{
			Cloudflare: v2alpha1.CloudflareCredentialRef{
				TokenSecretRef: v2alpha1.SecretReference{Name: "cred", Key: "token"},
				AccountID:      "acct-1",
				TxtRegistryKeySecretRef: &v2alpha1.SecretReference{
					Name: "missing-key",
					Key:  "key",
				},
			},
			Controllers: v2alpha1.ControllersSpec{},
		},
	}

	content := "192.0.2.1"
	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "rec",
			Namespace:  "default",
			Finalizers: []string{conventions.FinalizerName},
		},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Name:    "app.example.com",
			Type:    "A",
			Content: &content,
			ZoneID:  "z1",
		},
	}

	// Use env-based credentials so flow passes LoadCredentialsHierarchical and
	// reaches the codec build.
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(op, rec).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()
	m := mock.New()
	r := newDNSReconciler(t, c, s, m)

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"},
	})
	// Graceful halt: no error, requeue after delay.
	require.NoError(t, err)
	require.Greater(t, result.RequeueAfter.Nanoseconds(), int64(0), "expect non-zero RequeueAfter")

	// Ready condition reason must be TxtRegistryKeyUnavailable.
	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))
	var found *metav1.Condition
	for i := range got.Status.Conditions {
		if got.Status.Conditions[i].Type == conventions.ConditionTypeReady {
			found = &got.Status.Conditions[i]
			break
		}
	}
	require.NotNil(t, found, "Ready condition must be set")
	require.Equal(t, metav1.ConditionFalse, found.Status)
	require.Equal(t, conventions.ReasonTxtRegistryKeyUnavailable, found.Reason)
}

func TestDNS_Delete_RemovesUpstream(t *testing.T) {
	now := metav1.Now()
	s := zoneTestScheme(t)
	content := "192.0.2.1"
	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name: "rec", Namespace: "default",
			Finalizers:        []string{conventions.FinalizerName},
			DeletionTimestamp: &now,
		},
		Spec: v2alpha1.CloudflareDNSRecordSpec{Name: "app.example.com", Type: "A", Content: &content, ZoneID: "z1"},
	}
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rec).WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).Build()
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

// --- Observe mode tests (Task 11) ---

// observeModeRec builds a minimal CloudflareDNSRecord in Observe mode with
// the finalizer already set and the given ZoneID pre-resolved.
func observeModeRec(name, ns, zoneID string, extra ...func(*v2alpha1.CloudflareDNSRecord)) *v2alpha1.CloudflareDNSRecord {
	content := "1.1.1.1"
	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  ns,
			Finalizers: []string{conventions.FinalizerName},
		},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Name:   "test.example.com",
			Type:   "A",
			ZoneID: zoneID,
			Mode:   v2alpha1.RecordModeObserve,
			// Content set so admission CEL is satisfied in fake client;
			// observe mode does NOT read this to mutate CF.
			Content: &content,
		},
	}
	for _, fn := range extra {
		fn(rec)
	}
	return rec
}

// findReadyCondition returns the Ready condition or nil.
func findReadyCondition(conds []metav1.Condition) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == conventions.ConditionTypeReady {
			return &conds[i]
		}
	}
	return nil
}

// seedARecord creates an A record in the mock DNS store and returns its ID.
func seedARecord(t *testing.T, m *mock.Mock, zoneID, name, content string) string {
	t.Helper()
	r, err := m.DNS.CreateRecord(context.Background(), zoneID, cloudflare.DNSRecordParams{
		Name: name, Type: "A", Content: content, TTL: 1,
	})
	require.NoError(t, err)
	return r.ID
}

// seedPlaintextTXT creates a plaintext TXT companion record encoding the given
// namespace/name identity (and optional content hash encH) and returns its
// Cloudflare record ID.
func seedPlaintextTXT(t *testing.T, m *mock.Mock, zoneID, recordName, encNS, encName, encH string) string {
	t.Helper()
	codec := cloudflare.NewPlaintextCodec()
	content, err := codec.Encode(cloudflare.RegistryPayload{
		V:  1,
		K:  "CloudflareDNSRecord",
		NS: encNS,
		N:  encName,
		H:  encH,
	})
	require.NoError(t, err)
	txtName := cloudflare.AffixName("cf-txt", recordName)
	r, err := m.DNS.CreateRecord(context.Background(), zoneID, cloudflare.DNSRecordParams{
		Name: txtName, Type: "TXT", Content: content, TTL: 1,
	})
	require.NoError(t, err)
	return r.ID
}

// TestReconcile_ObserveMode_RecordExists_PopulatesStatus verifies that
// Spec.Mode=Observe lists the existing A record and TXT companion, populates
// Status fields, sets Reason=Observing, and does NOT invoke any mutating CF
// calls (Create/Update/Delete).
func TestReconcile_ObserveMode_RecordExists_PopulatesStatus(t *testing.T) {
	s := zoneTestScheme(t)
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	rec := observeModeRec("r", "ns", "z1")
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(rec).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()
	m := mock.New()

	// Pre-seed A record and matching plaintext TXT companion with a content hash.
	seedARecord(t, m, "z1", "test.example.com", "1.1.1.1")
	seedPlaintextTXT(t, m, "z1", "test.example.com", "ns", "r", "sha256:deadbeef")

	// Snapshot mutating-call counters BEFORE reconcile.
	createBefore := m.Calls("DNS.CreateRecord")
	updateBefore := m.Calls("DNS.UpdateRecord")
	deleteBefore := m.Calls("DNS.DeleteRecord")

	r := newDNSReconciler(t, c, s, m)
	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "r", Namespace: "ns"},
	})
	require.NoError(t, err)
	require.Greater(t, result.RequeueAfter.Nanoseconds(), int64(0), "expect non-zero RequeueAfter in observe mode")

	// Zero mutating calls.
	require.Equal(t, createBefore, m.Calls("DNS.CreateRecord"), "observe must not call CreateRecord")
	require.Equal(t, updateBefore, m.Calls("DNS.UpdateRecord"), "observe must not call UpdateRecord")
	require.Equal(t, deleteBefore, m.Calls("DNS.DeleteRecord"), "observe must not call DeleteRecord")

	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "r", Namespace: "ns"}, &got))

	require.Equal(t, "1.1.1.1", got.Status.CurrentContent, "Status.CurrentContent must be populated from CF")
	require.NotEmpty(t, got.Status.RecordID, "Status.RecordID must be set")
	require.NotNil(t, got.Status.ObservedTXT, "Status.ObservedTXT must be populated when TXT companion exists")
	require.Equal(t, "ns", got.Status.ObservedTXT.Namespace)
	require.Equal(t, "r", got.Status.ObservedTXT.Name)
	require.Equal(t, "plaintext", got.Status.ObservedTXT.Codec)
	require.Equal(t, "sha256:deadbeef", got.Status.ObservedTXT.ContentHash, "ContentHash must round-trip from TXT payload H field")

	cond := findReadyCondition(got.Status.Conditions)
	require.NotNil(t, cond, "Ready condition must be set")
	require.Equal(t, metav1.ConditionTrue, cond.Status)
	require.Equal(t, conventions.ReasonObserving, cond.Reason)
}

// TestReconcile_ObserveMode_AdoptHasNoEffect verifies that Adopt=true is a
// no-op in Observe mode: a foreign TXT companion still yields Reason=Observing
// (NOT AdoptRefusedForeign) and ObservedTXT reflects the foreign owner.
func TestReconcile_ObserveMode_AdoptHasNoEffect(t *testing.T) {
	s := zoneTestScheme(t)
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	rec := observeModeRec("r", "ns", "z1", func(r *v2alpha1.CloudflareDNSRecord) {
		r.Spec.Adopt = true
	})
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(rec).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()
	m := mock.New()

	// Pre-seed A record and a FOREIGN TXT companion (different ns/name).
	seedARecord(t, m, "z1", "test.example.com", "1.1.1.1")
	seedPlaintextTXT(t, m, "z1", "test.example.com", "other-ns", "other-r", "")

	createBefore := m.Calls("DNS.CreateRecord")
	updateBefore := m.Calls("DNS.UpdateRecord")
	deleteBefore := m.Calls("DNS.DeleteRecord")

	r := newDNSReconciler(t, c, s, m)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "r", Namespace: "ns"},
	})
	require.NoError(t, err)

	// Zero mutating calls — Adopt is a no-op in observe.
	require.Equal(t, createBefore, m.Calls("DNS.CreateRecord"), "observe must not call CreateRecord")
	require.Equal(t, updateBefore, m.Calls("DNS.UpdateRecord"), "observe must not call UpdateRecord")
	require.Equal(t, deleteBefore, m.Calls("DNS.DeleteRecord"), "observe must not call DeleteRecord")

	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "r", Namespace: "ns"}, &got))

	cond := findReadyCondition(got.Status.Conditions)
	require.NotNil(t, cond)
	require.Equal(t, conventions.ReasonObserving, cond.Reason, "Adopt is no-op in observe; reason must be Observing not AdoptRefusedForeign")

	require.NotNil(t, got.Status.ObservedTXT, "TXT companion must still be decoded")
	require.Equal(t, "other-ns", got.Status.ObservedTXT.Namespace)
	require.Equal(t, "other-r", got.Status.ObservedTXT.Name)
}

// TestReconcile_ObserveMode_DeletionDropsFinalizerImmediately verifies that
// when a record in Observe mode has a deletion timestamp set, the reconciler
// removes the finalizer without making any CF calls. The test is non-vacuous:
// Status.RecordID is set to a real mock record ID so that the non-short-circuit
// path WOULD have called dc.DeleteRecord (Status.RecordID != ""), but the
// observe-mode short-circuit in reconcileDelete skips it entirely.
func TestReconcile_ObserveMode_DeletionDropsFinalizerImmediately(t *testing.T) {
	now := metav1.Now()
	s := zoneTestScheme(t)
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	// Build the CR with DeletionTimestamp + finalizer. The finalizer must be
	// present so the fake client retains the object after DeletionTimestamp is
	// set.
	rec := observeModeRec("r", "ns", "z1", func(r *v2alpha1.CloudflareDNSRecord) {
		r.DeletionTimestamp = &now
	})

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(rec).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()
	m := mock.New()

	// Seed a real A record in the mock and capture its ID. This ID will be
	// written into Status.RecordID so that a non-short-circuited reconcileDelete
	// (i.e. without the observe-mode guard) WOULD call dc.DeleteRecord.
	aID := seedARecord(t, m, "z1", "test.example.com", "1.1.1.1")

	// Persist Status.RecordID via the status subresource. Because the fake
	// client uses WithStatusSubresource, setting fields on the struct before
	// WithObjects does NOT persist them — Status().Update is required.
	rec.Status.RecordID = aID
	require.NoError(t, c.Status().Update(context.Background(), rec))

	// Verify the Status round-tripped correctly before reconciling.
	var check v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "r", Namespace: "ns"}, &check))
	require.Equal(t, aID, check.Status.RecordID, "test setup: Status.RecordID must be persisted before Reconcile")

	// Snapshot the delete-call counter AFTER seeding.
	deleteBefore := m.Calls("DNS.DeleteRecord")

	r := newDNSReconciler(t, c, s, m)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "r", Namespace: "ns"},
	})
	require.NoError(t, err)

	// Load-bearing assertion: the observe short-circuit suppressed a CF delete
	// that the non-short-circuit path (Status.RecordID != "") WOULD have made.
	// If reconcileDelete's observe guard were removed, DeleteRecord would be
	// called and this assertion would fail.
	require.Equal(t, deleteBefore, m.Calls("DNS.DeleteRecord"), "observe delete must not call CF DeleteRecord")

	// Finalizer must be removed. Use a strict check: if the object is gone
	// (fake client GC after finalizer cleared), NotFound is acceptable; if it
	// still exists, Finalizers must be empty.
	var got v2alpha1.CloudflareDNSRecord
	err = c.Get(context.Background(), types.NamespacedName{Name: "r", Namespace: "ns"}, &got)
	if err != nil {
		require.True(t, apierrors.IsNotFound(err), "unexpected get error: %v", err)
	} else {
		require.Empty(t, got.Finalizers, "finalizer must be cleared after observe deletion")
	}
}

// TestReconcile_ObserveMode_RecordAbsent_NoOp verifies that when no matching
// record exists in Cloudflare, Observe mode still returns Reason=Observing with
// empty status fields and makes no mutating calls.
func TestReconcile_ObserveMode_RecordAbsent_NoOp(t *testing.T) {
	s := zoneTestScheme(t)
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	rec := observeModeRec("r", "ns", "z1")
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(rec).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()
	m := mock.New()
	// No records seeded — CF is empty.

	createBefore := m.Calls("DNS.CreateRecord")
	updateBefore := m.Calls("DNS.UpdateRecord")
	deleteBefore := m.Calls("DNS.DeleteRecord")

	r := newDNSReconciler(t, c, s, m)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "r", Namespace: "ns"},
	})
	require.NoError(t, err)

	require.Equal(t, createBefore, m.Calls("DNS.CreateRecord"), "observe must not create")
	require.Equal(t, updateBefore, m.Calls("DNS.UpdateRecord"), "observe must not update")
	require.Equal(t, deleteBefore, m.Calls("DNS.DeleteRecord"), "observe must not delete")

	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "r", Namespace: "ns"}, &got))

	require.Empty(t, got.Status.RecordID, "RecordID must be empty when no CF record found")
	require.Empty(t, got.Status.CurrentContent, "CurrentContent must be empty when no CF record found")
	require.Nil(t, got.Status.ObservedTXT, "ObservedTXT must be nil when no TXT companion found")

	cond := findReadyCondition(got.Status.Conditions)
	require.NotNil(t, cond)
	require.Equal(t, conventions.ReasonObserving, cond.Reason)
}

// TestReconcile_ObserveMode_NoOperatorSingleton_PlaintextOK verifies that
// when the CloudflareOperator singleton is absent (no TXT key configured),
// Observe mode still populates Status.ObservedTXT via plaintext decoding.
func TestReconcile_ObserveMode_NoOperatorSingleton_PlaintextOK(t *testing.T) {
	s := zoneTestScheme(t)
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	// No CloudflareOperator object in client — singleton absent.
	rec := observeModeRec("r", "ns", "z1")
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(rec).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()
	m := mock.New()

	seedARecord(t, m, "z1", "test.example.com", "2.2.2.2")
	seedPlaintextTXT(t, m, "z1", "test.example.com", "ns", "r", "")

	r := newDNSReconciler(t, c, s, m)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "r", Namespace: "ns"},
	})
	require.NoError(t, err)

	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "r", Namespace: "ns"}, &got))

	require.NotNil(t, got.Status.ObservedTXT, "plaintext TXT must decode even without singleton")
	require.Equal(t, "ns", got.Status.ObservedTXT.Namespace)
	require.Equal(t, "r", got.Status.ObservedTXT.Name)
	require.Equal(t, "plaintext", got.Status.ObservedTXT.Codec)

	cond := findReadyCondition(got.Status.Conditions)
	require.NotNil(t, cond)
	require.Equal(t, conventions.ReasonObserving, cond.Reason)
}

// --- TXT-verified adoption tests (Task 12) ---

// adoptRec builds a Managed-mode CloudflareDNSRecord with Adopt:true,
// Status.RecordID empty, and the finalizer already set (to skip the finalizer
// requeue and land directly in the Adopt branch).
func adoptRec(name, ns, zoneID string) *v2alpha1.CloudflareDNSRecord {
	content := "1.1.1.1"
	return &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  ns,
			Finalizers: []string{conventions.FinalizerName},
		},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Name:    "test.example.com",
			Type:    "A",
			Content: &content,
			ZoneID:  zoneID,
			Adopt:   true,
			// Mode unset → defaults to Managed.
		},
	}
}

// TestReconcile_AdoptWithNoTXT_Refused verifies that Adopt:true with a
// pre-existing A record but NO TXT companion is refused (design §2 Q2 — no
// silent backfill). RecordID must stay empty; no TXT is created.
func TestReconcile_AdoptWithNoTXT_Refused(t *testing.T) {
	s := zoneTestScheme(t)
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	rec := adoptRec("rec", "default", "z1")
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(rec).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()
	m := mock.New()

	// Seed A record only — no TXT companion.
	seedARecord(t, m, "z1", "test.example.com", "1.1.1.1")

	createBefore := m.Calls("DNS.CreateRecord")

	r := newDNSReconciler(t, c, s, m)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"},
	})
	require.NoError(t, err)

	// No TXT must have been created (no silent backfill).
	require.Equal(t, createBefore, m.Calls("DNS.CreateRecord"), "must not create a TXT for a pre-existing untracked record")

	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))

	require.Empty(t, got.Status.RecordID, "RecordID must be empty — adoption refused")

	cond := findReadyCondition(got.Status.Conditions)
	require.NotNil(t, cond, "Ready condition must be set")
	require.Equal(t, metav1.ConditionFalse, cond.Status)
	require.Equal(t, conventions.ReasonAdoptRefusedNoTXT, cond.Reason)
}

// TestReconcile_AdoptWithForeignTXT_Refused verifies that Adopt:true with a
// pre-existing A record and a TXT companion encoding a FOREIGN owner is
// refused. RecordID must stay empty; no TXT is created.
func TestReconcile_AdoptWithForeignTXT_Refused(t *testing.T) {
	s := zoneTestScheme(t)
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	rec := adoptRec("rec", "default", "z1")
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(rec).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()
	m := mock.New()

	// Seed A record + a TXT claiming a DIFFERENT owner (NS "other", N "other-r").
	seedARecord(t, m, "z1", "test.example.com", "1.1.1.1")
	seedPlaintextTXT(t, m, "z1", "test.example.com", "other", "other-r", "")

	createBefore := m.Calls("DNS.CreateRecord")

	r := newDNSReconciler(t, c, s, m)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"},
	})
	require.NoError(t, err)

	// No TXT must have been created.
	require.Equal(t, createBefore, m.Calls("DNS.CreateRecord"), "must not create a TXT when foreign owner detected")

	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))

	require.Empty(t, got.Status.RecordID, "RecordID must be empty — adoption refused")

	cond := findReadyCondition(got.Status.Conditions)
	require.NotNil(t, cond, "Ready condition must be set")
	require.Equal(t, metav1.ConditionFalse, cond.Status)
	require.Equal(t, conventions.ReasonAdoptRefusedForeign, cond.Reason)
}

// TestReconcile_AdoptWithMatchingTXT_Succeeds verifies that Adopt:true with a
// pre-existing A record and a TXT companion encoding OUR (K, NS, N) succeeds.
// Status.RecordID, TxtRecordID, and TxtAffix must all be populated.
func TestReconcile_AdoptWithMatchingTXT_Succeeds(t *testing.T) {
	s := zoneTestScheme(t)
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	rec := adoptRec("rec", "default", "z1")
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(rec).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()
	m := mock.New()

	// Seed A record + a TXT encoding OUR identity (NS "default", N "rec").
	aID := seedARecord(t, m, "z1", "test.example.com", "1.1.1.1")
	txtID := seedPlaintextTXT(t, m, "z1", "test.example.com", "default", "rec", "")

	r := newDNSReconciler(t, c, s, m)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"},
	})
	require.NoError(t, err)

	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))

	require.Equal(t, aID, got.Status.RecordID, "RecordID must be set to the pre-existing A record")
	require.Equal(t, txtID, got.Status.TxtRecordID, "TxtRecordID must be set to the companion TXT record")
	require.Equal(t, "cf-txt", got.Status.TxtAffix, "TxtAffix must be cf-txt")

	// Must reach terminal success state.
	cond := findReadyCondition(got.Status.Conditions)
	require.NotNil(t, cond)
	require.Equal(t, metav1.ConditionTrue, cond.Status, "Ready condition must be True after successful adoption")
	require.Equal(t, conventions.ReasonReady, cond.Reason, "Ready reason must be ReasonReady after successful adoption")
}

// --- TXT companion write/refresh tests (Task 13) ---

// sha256HexFor returns the expected sha256 hex string for s, matching the
// production sha256Hex helper: "sha256:" + hex(sha256(s)).
func sha256HexFor(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// TestReconcile_CreateWritesTxtCompanion verifies that a fresh Create of the
// main DNS record is paired with a TXT companion write. Status.TxtRecordID and
// TxtAffix must be set; the TXT payload must decode to our identity + the
// sha256 of the written content.
func TestReconcile_CreateWritesTxtCompanion(t *testing.T) {
	s := zoneTestScheme(t)
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	content := "192.0.2.1"
	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "default"},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Name:    "app.example.com",
			Type:    "A",
			Content: &content,
			ZoneID:  "z1",
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(rec).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()
	m := mock.New()
	r := newDNSReconciler(t, c, s, m)

	// First reconcile sets the finalizer and requeues.
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)
	// Second reconcile: main A record created + TXT companion created.
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)

	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))

	require.NotEmpty(t, got.Status.RecordID, "main record must be created")
	require.NotEmpty(t, got.Status.TxtRecordID, "TXT companion must be created")
	require.Equal(t, "cf-txt", got.Status.TxtAffix, "TxtAffix must be cf-txt")

	// Verify TXT companion exists in the mock and decodes correctly.
	txtName := cloudflare.AffixName("cf-txt", "app.example.com")
	txtRecs, err := m.DNS.ListRecordsByNameAndType(context.Background(), "z1", txtName, "TXT")
	require.NoError(t, err)
	require.Len(t, txtRecs, 1, "exactly one TXT companion must exist")

	codec := cloudflare.NewPlaintextCodec()
	payload, err := codec.Decode(txtRecs[0].Content)
	require.NoError(t, err, "TXT companion must be decodable with plaintext codec")
	require.Equal(t, "CloudflareDNSRecord", payload.K)
	require.Equal(t, "default", payload.NS)
	require.Equal(t, "rec", payload.N)
	require.Equal(t, sha256HexFor("192.0.2.1"), payload.H, "TXT H must be sha256 of the written content")
}

// TestReconcile_UpdateRefreshesTxtHash verifies that when the main record is
// updated (content drift), the TXT companion is refreshed with the new content
// hash. Status.TxtRecordID must remain set.
func TestReconcile_UpdateRefreshesTxtHash(t *testing.T) {
	s := zoneTestScheme(t)
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	newContent := "192.0.2.99"
	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "rec",
			Namespace:  "default",
			Finalizers: []string{conventions.FinalizerName},
		},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Name:    "app.example.com",
			Type:    "A",
			Content: &newContent, // desired new content
			ZoneID:  "z1",
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(rec).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()
	m := mock.New()

	// Pre-seed the A record with OLD content.
	oldContent := "192.0.2.1"
	aID := seedARecord(t, m, "z1", "app.example.com", oldContent)

	// Pre-seed the TXT companion with the hash of the OLD content.
	txtID := seedPlaintextTXT(t, m, "z1", "app.example.com", "default", "rec", sha256HexFor(oldContent))

	// Persist Status.RecordID + TxtRecordID so reconciler starts in update path.
	rec.Status.RecordID = aID
	rec.Status.TxtRecordID = txtID
	rec.Status.TxtAffix = "cf-txt"
	rec.Status.CurrentContent = oldContent
	require.NoError(t, c.Status().Update(context.Background(), rec))

	// Snapshot update call counts before reconcile.
	updatesBefore := m.Calls("DNS.UpdateRecord")

	r := newDNSReconciler(t, c, s, m)
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)

	// At least one UpdateRecord must have been called (main + TXT).
	require.Greater(t, m.Calls("DNS.UpdateRecord"), updatesBefore, "UpdateRecord must be called for drift")

	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))

	require.NotEmpty(t, got.Status.TxtRecordID, "TxtRecordID must remain set after update")
	require.Equal(t, "cf-txt", got.Status.TxtAffix)

	// Verify TXT companion now carries the NEW content hash.
	txtName := cloudflare.AffixName("cf-txt", "app.example.com")
	txtRecs, err := m.DNS.ListRecordsByNameAndType(context.Background(), "z1", txtName, "TXT")
	require.NoError(t, err)
	require.Len(t, txtRecs, 1, "exactly one TXT companion must exist")

	codec := cloudflare.NewPlaintextCodec()
	payload, err := codec.Decode(txtRecs[0].Content)
	require.NoError(t, err, "TXT companion must be decodable")
	require.Equal(t, sha256HexFor(newContent), payload.H, "TXT H must be updated to sha256 of the new content")
}

// TestReconcile_TxtWriteFailsAfterDnsCreate_Partial verifies the partial-failure
// contract: when the main A record create succeeds but the TXT companion create
// fails, the reconcile must NOT return an error, must leave Status.TxtRecordID
// unset, must emit a Warning Event with reason TxtRegistryWriteFailed, and must
// return a non-zero RequeueAfter (so a later reconcile retries the TXT write).
func TestReconcile_TxtWriteFailsAfterDnsCreate_Partial(t *testing.T) {
	s := zoneTestScheme(t)
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	content := "192.0.2.1"
	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "rec",
			Namespace:  "default",
			Finalizers: []string{conventions.FinalizerName},
		},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Name:    "app.example.com",
			Type:    "A",
			Content: &content,
			ZoneID:  "z1",
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(rec).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()
	m := mock.New()

	// Inject an error on the 2nd DNS.CreateRecord call:
	//   call #1 = main A record create (must succeed)
	//   call #2 = TXT companion create (must fail — partial failure)
	m.InjectErrorOnCall("DNS.CreateRecord", 2, errors.New("simulated TXT create failure"))

	// Wire a real fake recorder so we can assert the Warning Event.
	fakeRecorder := record.NewFakeRecorder(10)
	r := newDNSReconciler(t, c, s, m)
	r.Recorder = fakeRecorder

	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})

	// Partial failure: reconcile must NOT return an error.
	require.NoError(t, err, "partial TXT failure must not fail the reconcile")
	// Reconciler must requeue so the TXT write is retried.
	require.Greater(t, result.RequeueAfter.Nanoseconds(), int64(0), "expect non-zero RequeueAfter so TXT write is retried")

	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))

	// Main record was created.
	require.NotEmpty(t, got.Status.RecordID, "main RecordID must be set even on partial TXT failure")
	// TXT companion was NOT persisted (partial failure).
	require.Empty(t, got.Status.TxtRecordID, "TxtRecordID must be empty when TXT companion write failed")

	// A Warning Event with reason TxtRegistryWriteFailed must have been emitted.
	select {
	case event := <-fakeRecorder.Events:
		require.Contains(t, event, conventions.ReasonTxtRegistryWriteFailed,
			"Warning Event must contain TxtRegistryWriteFailed reason")
	default:
		t.Fatal("expected a Warning Event for TxtRegistryWriteFailed but none was emitted")
	}
}

// TestReconcile_TxtWrite_RetriesOnNextReconcileNoDrift verifies the retry
// contract for the Important bug fixed in P5-T13: when the main record exists
// (RecordID set) but TxtRecordID is empty (partial TXT failure on create), a
// subsequent reconcile with NO content drift must still write the missing TXT
// companion. Previously the no-drift else-branch skipped writeTXTCompanion
// entirely, leaving the companion permanently missing.
//
// This test is DESIGNED TO FAIL against the pre-fix production code (where the
// no-drift arm does not call writeTXTCompanion) and PASS after the fix.
func TestReconcile_TxtWrite_RetriesOnNextReconcileNoDrift(t *testing.T) {
	s := zoneTestScheme(t)
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	content := "192.0.2.1"
	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "rec",
			Namespace:  "default",
			Finalizers: []string{conventions.FinalizerName},
		},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Name:    "app.example.com",
			Type:    "A",
			Content: &content,
			ZoneID:  "z1",
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(rec).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()
	m := mock.New()

	// First reconcile: inject TXT create failure so RecordID is set but
	// TxtRecordID is left empty (simulating a partial failure).
	m.InjectErrorOnCall("DNS.CreateRecord", 2, errors.New("simulated TXT create failure"))
	fakeRecorder := record.NewFakeRecorder(10)
	r := newDNSReconciler(t, c, s, m)
	r.Recorder = fakeRecorder

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err, "partial TXT failure must not fail the first reconcile")

	var afterFirst v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &afterFirst))
	require.NotEmpty(t, afterFirst.Status.RecordID, "RecordID must be set after first reconcile")
	require.Empty(t, afterFirst.Status.TxtRecordID, "TxtRecordID must be empty after partial TXT failure")

	// Drain the Warning Event from the first reconcile.
	select {
	case <-fakeRecorder.Events:
	default:
	}

	// Second reconcile: NO content drift, NO injected errors. The retry guard
	// (TxtRecordID == "") must cause writeTXTCompanion to run and write the
	// companion.
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err, "second reconcile must not fail")

	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))

	// The retry must have written the TXT companion.
	require.NotEmpty(t, got.Status.TxtRecordID, "TxtRecordID must be set after retry reconcile")
	require.Equal(t, "cf-txt", got.Status.TxtAffix, "TxtAffix must be cf-txt after retry")

	// Verify the TXT companion exists in the mock with correct identity + hash.
	txtName := cloudflare.AffixName("cf-txt", "app.example.com")
	txtRecs, err := m.DNS.ListRecordsByNameAndType(context.Background(), "z1", txtName, "TXT")
	require.NoError(t, err)
	require.Len(t, txtRecs, 1, "exactly one TXT companion must exist after retry")

	codec := cloudflare.NewPlaintextCodec()
	payload, err := codec.Decode(txtRecs[0].Content)
	require.NoError(t, err, "TXT companion must be decodable with plaintext codec")
	require.Equal(t, "CloudflareDNSRecord", payload.K)
	require.Equal(t, "default", payload.NS)
	require.Equal(t, "rec", payload.N)
	require.Equal(t, sha256HexFor(content), payload.H, "TXT H must be sha256 of the record's content")
}

// --- TXT-companion cascade delete tests (Task 14) ---

// TestReconcile_DeleteCascadesTxtCompanion verifies that when a Managed-mode
// record with a DeletionTimestamp is reconciled, the reconciler deletes BOTH
// the main A record AND the TXT companion from Cloudflare. The TXT-gone
// assertion is the load-bearing one: it must fail if the cascade is not wired.
func TestReconcile_DeleteCascadesTxtCompanion(t *testing.T) {
	now := metav1.Now()
	s := zoneTestScheme(t)
	content := "192.0.2.1"
	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "rec",
			Namespace:         "default",
			Finalizers:        []string{conventions.FinalizerName},
			DeletionTimestamp: &now,
		},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Name:    "app.example.com",
			Type:    "A",
			Content: &content,
			ZoneID:  "z1",
			// Mode unset → Managed.
		},
	}
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rec).WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).Build()
	m := mock.New()

	// Pre-seed the main A record and a TXT companion in the mock.
	aID := seedARecord(t, m, "z1", "app.example.com", "192.0.2.1")
	txtID := seedPlaintextTXT(t, m, "z1", "app.example.com", "default", "rec", sha256HexFor("192.0.2.1"))

	// Persist Status.RecordID + Status.TxtRecordID so reconcileDelete can act.
	rec.Status.RecordID = aID
	rec.Status.TxtRecordID = txtID
	require.NoError(t, c.Status().Update(context.Background(), rec))

	// Verify setup round-tripped correctly.
	var check v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &check))
	require.Equal(t, aID, check.Status.RecordID, "test setup: Status.RecordID must be persisted")
	require.Equal(t, txtID, check.Status.TxtRecordID, "test setup: Status.TxtRecordID must be persisted")

	// Snapshot the delete counter before reconcile.
	deleteBefore := m.Calls("DNS.DeleteRecord")

	r := newDNSReconciler(t, c, s, m)
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)

	// Both DeleteRecord calls must have happened (main + TXT).
	require.Equal(t, deleteBefore+2, m.Calls("DNS.DeleteRecord"), "must call DeleteRecord twice: main record + TXT companion")

	// Main record must be gone.
	_, gerr := m.DNS.GetRecord(context.Background(), "z1", aID)
	require.Error(t, gerr, "main A record must be deleted from Cloudflare")

	// Load-bearing: TXT companion must also be gone.
	txtName := cloudflare.AffixName("cf-txt", "app.example.com")
	txtRecs, lerr := m.DNS.ListRecordsByNameAndType(context.Background(), "z1", txtName, "TXT")
	require.NoError(t, lerr)
	require.Empty(t, txtRecs, "TXT companion must be deleted from Cloudflare (cascade delete)")

	// Finalizer must be removed.
	var got v2alpha1.CloudflareDNSRecord
	err = c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got)
	if err != nil {
		require.True(t, apierrors.IsNotFound(err), "unexpected get error: %v", err)
	} else {
		require.Empty(t, got.Finalizers, "finalizer must be cleared after delete")
	}
}

// TestReconcile_DeleteTxtCompanion_NotFoundTolerated verifies that when a
// Managed-mode delete is reconciled and Status.TxtRecordID points to a record
// that no longer exists (already removed externally), the reconcile succeeds
// without error. This pins the best-effort tolerance of deleteTXTCompanion.
func TestReconcile_DeleteTxtCompanion_NotFoundTolerated(t *testing.T) {
	now := metav1.Now()
	s := zoneTestScheme(t)
	content := "192.0.2.1"
	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "rec",
			Namespace:         "default",
			Finalizers:        []string{conventions.FinalizerName},
			DeletionTimestamp: &now,
		},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Name:    "app.example.com",
			Type:    "A",
			Content: &content,
			ZoneID:  "z1",
		},
	}
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rec).WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).Build()
	m := mock.New()

	// Seed only the main A record. The TxtRecordID will reference a non-existent
	// record — simulating external removal of the TXT companion.
	aID := seedARecord(t, m, "z1", "app.example.com", "192.0.2.1")
	const missingTxtID = "txt-already-gone"

	rec.Status.RecordID = aID
	rec.Status.TxtRecordID = missingTxtID
	require.NoError(t, c.Status().Update(context.Background(), rec))

	r := newDNSReconciler(t, c, s, m)
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	// Best-effort tolerance: ErrRecordNotFound on TXT delete must not propagate.
	require.NoError(t, err, "NotFound on TXT companion delete must be tolerated")

	// Main record must still be deleted.
	_, gerr := m.DNS.GetRecord(context.Background(), "z1", aID)
	require.Error(t, gerr, "main A record must be deleted even when TXT companion was already gone")

	// Finalizer must be removed.
	var got v2alpha1.CloudflareDNSRecord
	err = c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got)
	if err != nil {
		require.True(t, apierrors.IsNotFound(err), "unexpected get error: %v", err)
	} else {
		require.Empty(t, got.Finalizers, "finalizer must be cleared after delete")
	}
}

// TestReconcile_AdoptWithUnparseableTXT_Refused verifies that Adopt:true with
// TestReconcile_ManagedToObserveTransition_StopsWriting verifies that switching a
// previously-Managed record to Spec.Mode=Observe stops all mutating Cloudflare
// calls. The test is non-vacuous: after the mode flip the main record and TXT
// companion are still present in the mock (the test checks they are untouched),
// and if the observe-mode early-exit were removed the next reconcile WOULD call
// UpdateRecord (or at minimum re-enter the write path) — so the zero-delta
// assertions would fail.
func TestReconcile_ManagedToObserveTransition_StopsWriting(t *testing.T) {
	s := zoneTestScheme(t)
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	content := "10.0.0.1"
	rec := &v2alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "default"},
		Spec: v2alpha1.CloudflareDNSRecordSpec{
			Name:    "transition.example.com",
			Type:    "A",
			Content: &content,
			ZoneID:  "z1",
			// Mode unset → defaults to Managed.
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(rec).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()
	m := mock.New()
	r := newDNSReconciler(t, c, s, m)

	// First reconcile: sets the finalizer and requeues.
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)
	// Second reconcile: main A record created + TXT companion created.
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)

	// Confirm Managed path populated both IDs.
	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))
	require.NotEmpty(t, got.Status.RecordID, "setup: RecordID must be set after Managed reconcile")
	require.NotEmpty(t, got.Status.TxtRecordID, "setup: TxtRecordID must be set after Managed reconcile")

	// Flip to Observe mode.
	got.Spec.Mode = v2alpha1.RecordModeObserve
	require.NoError(t, c.Update(context.Background(), &got))

	// Snapshot mutating-call counters BEFORE the post-flip reconcile.
	createBefore := m.Calls("DNS.CreateRecord")
	updateBefore := m.Calls("DNS.UpdateRecord")
	deleteBefore := m.Calls("DNS.DeleteRecord")

	// Reconcile in Observe mode: must make zero mutating CF calls.
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"}})
	require.NoError(t, err)

	require.Equal(t, createBefore, m.Calls("DNS.CreateRecord"), "observe must not call CreateRecord after mode flip")
	require.Equal(t, updateBefore, m.Calls("DNS.UpdateRecord"), "observe must not call UpdateRecord after mode flip")
	require.Equal(t, deleteBefore, m.Calls("DNS.DeleteRecord"), "observe must not call DeleteRecord after mode flip")

	// Prior records must still exist in the mock (observe does not delete them).
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))
	require.NotEmpty(t, got.Status.RecordID, "RecordID must survive transition to Observe")
	require.NotEmpty(t, got.Status.TxtRecordID, "TxtRecordID must survive transition to Observe")
}

// a pre-existing A record and a TXT companion whose content is gibberish is
// treated conservatively as AdoptRefusedNoTXT (unrecognized ⇒ refuse).
// RecordID must stay empty; no TXT is created.
func TestReconcile_AdoptWithUnparseableTXT_Refused(t *testing.T) {
	s := zoneTestScheme(t)
	t.Setenv("CLOUDFLARE_API_TOKEN", "t")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct-1")

	rec := adoptRec("rec", "default", "z1")
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(rec).
		WithStatusSubresource(&v2alpha1.CloudflareDNSRecord{}).
		Build()
	m := mock.New()

	// Seed A record + a TXT with unparseable content.
	seedARecord(t, m, "z1", "test.example.com", "1.1.1.1")
	txtName := cloudflare.AffixName("cf-txt", "test.example.com")
	_, err := m.DNS.CreateRecord(context.Background(), "z1", cloudflare.DNSRecordParams{
		Name: txtName, Type: "TXT", Content: "not-a-codec", TTL: 1,
	})
	require.NoError(t, err)

	createBefore := m.Calls("DNS.CreateRecord")

	r := newDNSReconciler(t, c, s, m)
	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "rec", Namespace: "default"},
	})
	require.NoError(t, err)

	// No TXT must have been created.
	require.Equal(t, createBefore, m.Calls("DNS.CreateRecord"), "must not create a TXT when TXT content is unrecognized")

	var got v2alpha1.CloudflareDNSRecord
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "rec", Namespace: "default"}, &got))

	require.Empty(t, got.Status.RecordID, "RecordID must be empty — adoption refused")

	cond := findReadyCondition(got.Status.Conditions)
	require.NotNil(t, cond, "Ready condition must be set")
	require.Equal(t, metav1.ConditionFalse, cond.Status)
	require.Equal(t, conventions.ReasonAdoptRefusedNoTXT, cond.Reason, "unrecognized TXT content must be treated conservatively as NoTXT")
}
