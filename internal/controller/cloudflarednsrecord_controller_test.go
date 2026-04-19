package controller

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	cfclient "github.com/jacaudi/cloudflare-operator/internal/cloudflare"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const testDNSContent = "1.2.3.4"

// mockDNSClient implements cfclient.DNSClient for testing.
type mockDNSClient struct {
	records      map[string]*cfclient.DNSRecord
	nextID       int
	createCalled bool
	updateCalled bool
	deleteCalled bool
	lastZoneID   string
	createErr    error
	updateErr    error
	deleteErr    error
	listErr      error
	getErr       error
	listOverride func(zoneID, name, recordType string) ([]cfclient.DNSRecord, error)
}

func newMockDNSClient() *mockDNSClient {
	return &mockDNSClient{records: make(map[string]*cfclient.DNSRecord)}
}

func (m *mockDNSClient) GetRecord(_ context.Context, _, recordID string) (*cfclient.DNSRecord, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	r, ok := m.records[recordID]
	if !ok {
		return nil, fmt.Errorf("record not found")
	}
	return r, nil
}

func (m *mockDNSClient) ListRecordsByNameAndType(_ context.Context, zoneID, name, recordType string) ([]cfclient.DNSRecord, error) {
	if m.listOverride != nil {
		return m.listOverride(zoneID, name, recordType)
	}
	if m.listErr != nil {
		return nil, m.listErr
	}
	var results []cfclient.DNSRecord
	for _, r := range m.records {
		if r.Name == name && r.Type == recordType {
			results = append(results, *r)
		}
	}
	return results, nil
}

func (m *mockDNSClient) CreateRecord(_ context.Context, zoneID string, params cfclient.DNSRecordParams) (*cfclient.DNSRecord, error) {
	m.createCalled = true
	m.lastZoneID = zoneID
	if m.createErr != nil {
		return nil, m.createErr
	}
	m.nextID++
	id := fmt.Sprintf("rec-%d", m.nextID)
	r := &cfclient.DNSRecord{
		ID:      id,
		Name:    params.Name,
		Type:    params.Type,
		Content: params.Content,
	}
	if params.Proxied != nil {
		r.Proxied = *params.Proxied
	}
	if params.TTL > 0 {
		r.TTL = params.TTL
	}
	m.records[id] = r
	return r, nil
}

func (m *mockDNSClient) UpdateRecord(_ context.Context, _, recordID string, params cfclient.DNSRecordParams) (*cfclient.DNSRecord, error) {
	m.updateCalled = true
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	r, ok := m.records[recordID]
	if !ok {
		return nil, fmt.Errorf("record not found")
	}
	r.Content = params.Content
	if params.Proxied != nil {
		r.Proxied = *params.Proxied
	}
	if params.TTL > 0 {
		r.TTL = params.TTL
	}
	return r, nil
}

func (m *mockDNSClient) DeleteRecord(_ context.Context, zoneID, recordID string) error {
	m.deleteCalled = true
	m.lastZoneID = zoneID
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.records, recordID)
	return nil
}

// Helper to create a base CloudflareDNSRecord for tests.
func newTestDNSRecord(name, namespace string) *cloudflarev1alpha1.CloudflareDNSRecord {
	content := testDNSContent
	proxied := false
	return &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Generation: 1,
		},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			ZoneID:  "zone-abc",
			Name:    "test.example.com",
			Type:    "A",
			Content: &content,
			TTL:     1,
			Proxied: &proxied,
			SecretRef: cloudflarev1alpha1.SecretReference{
				Name: "cf-secret",
			},
			Interval: &metav1.Duration{Duration: 5 * time.Minute},
		},
	}
}

// Helper to create the Cloudflare API token + Account ID secret.
func newTestSecret(namespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cf-secret",
			Namespace: namespace,
		},
		Data: map[string][]byte{
			"apiToken":  []byte("test-token"),
			"accountID": []byte("acct-123"),
		},
	}
}

// buildReconciler creates a CloudflareDNSRecordReconciler wired to a fake client and mock DNS client.
func buildReconciler(s *runtime.Scheme, mock *mockDNSClient, objs ...client.Object) *CloudflareDNSRecordReconciler {
	// Collect CRD objects for status subresource registration
	var statusObjs []client.Object
	for _, o := range objs {
		switch o.(type) {
		case *cloudflarev1alpha1.CloudflareDNSRecord, *cloudflarev1alpha1.CloudflareZone:
			statusObjs = append(statusObjs, o)
		}
	}

	builder := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(statusObjs...)

	fakeClient := builder.Build()

	return &CloudflareDNSRecordReconciler{
		Client:        fakeClient,
		Scheme:        s,
		Recorder:      record.NewFakeRecorder(10),
		ClientFactory: cfclient.NewClientFactory(fakeClient),
		DNSClientFn: func(_ string) cfclient.DNSClient {
			return mock
		},
	}
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := cloudflarev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("failed to add v1alpha1 to scheme: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("failed to add corev1 to scheme: %v", err)
	}
	return s
}

func TestReconcile_AddsFinalizerOnFirstReconcile(t *testing.T) {
	s := testScheme(t)
	dnsRecord := newTestDNSRecord("test-rec", "default")
	secret := newTestSecret("default")
	mock := newMockDNSClient()

	r := buildReconciler(s, mock, dnsRecord, secret)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should requeue after adding finalizer
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after adding finalizer")
	}

	// Verify finalizer was added
	var updated cloudflarev1alpha1.CloudflareDNSRecord
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-rec", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated record: %v", err)
	}

	found := slices.Contains(updated.Finalizers, cloudflarev1alpha1.FinalizerName)
	if !found {
		t.Errorf("expected finalizer %q to be present, got finalizers: %v", cloudflarev1alpha1.FinalizerName, updated.Finalizers)
	}
}

func TestReconcile_CreatesDNSRecord(t *testing.T) {
	s := testScheme(t)
	dnsRecord := newTestDNSRecord("test-rec", "default")
	// Pre-add the finalizer so we proceed to the create path
	dnsRecord.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	secret := newTestSecret("default")
	mock := newMockDNSClient()

	r := buildReconciler(s, mock, dnsRecord, secret)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should requeue after interval
	if result.RequeueAfter != 5*time.Minute {
		t.Errorf("expected RequeueAfter=5m, got %v", result.RequeueAfter)
	}

	// Verify the mock was called
	if !mock.createCalled {
		t.Error("expected CreateRecord to be called")
	}

	// Verify status was updated
	var updated cloudflarev1alpha1.CloudflareDNSRecord
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-rec", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated record: %v", err)
	}

	if updated.Status.RecordID == "" {
		t.Error("expected RecordID to be set in status")
	}
	if updated.Status.CurrentContent != testDNSContent {
		t.Errorf("expected CurrentContent=1.2.3.4, got %q", updated.Status.CurrentContent)
	}
}

func TestReconcile_AdoptsExistingRecord(t *testing.T) {
	s := testScheme(t)
	dnsRecord := newTestDNSRecord("test-rec", "default")
	dnsRecord.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	// No RecordID in status — should adopt
	secret := newTestSecret("default")

	mock := newMockDNSClient()
	// Pre-populate an existing record in Cloudflare that matches by name+type
	mock.records["existing-123"] = &cfclient.DNSRecord{
		ID:      "existing-123",
		Name:    "test.example.com",
		Type:    "A",
		Content: testDNSContent,
		Proxied: false,
		TTL:     1,
	}

	r := buildReconciler(s, mock, dnsRecord, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should NOT have created a new record
	if mock.createCalled {
		t.Error("expected CreateRecord NOT to be called when adopting")
	}

	// Verify the status has the adopted record ID
	var updated cloudflarev1alpha1.CloudflareDNSRecord
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-rec", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated record: %v", err)
	}

	if updated.Status.RecordID != "existing-123" {
		t.Errorf("expected RecordID=existing-123, got %q", updated.Status.RecordID)
	}
}

func TestReconcile_UpdatesDriftedRecord(t *testing.T) {
	s := testScheme(t)
	dnsRecord := newTestDNSRecord("test-rec", "default")
	dnsRecord.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	dnsRecord.Status.RecordID = "rec-drift"
	secret := newTestSecret("default")

	mock := newMockDNSClient()
	// Existing record with different content (drifted)
	mock.records["rec-drift"] = &cfclient.DNSRecord{
		ID:      "rec-drift",
		Name:    "test.example.com",
		Type:    "A",
		Content: "5.6.7.8", // Drifted from desired 1.2.3.4
		Proxied: false,
		TTL:     1,
	}

	r := buildReconciler(s, mock, dnsRecord, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mock.updateCalled {
		t.Error("expected UpdateRecord to be called for drifted content")
	}

	// Verify the content was updated in the mock
	updatedRec := mock.records["rec-drift"]
	if updatedRec.Content != testDNSContent {
		t.Errorf("expected record content to be updated to 1.2.3.4, got %q", updatedRec.Content)
	}

	// Verify status
	var updated cloudflarev1alpha1.CloudflareDNSRecord
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-rec", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated record: %v", err)
	}
	if updated.Status.CurrentContent != testDNSContent {
		t.Errorf("expected CurrentContent=1.2.3.4, got %q", updated.Status.CurrentContent)
	}
}

func TestReconcile_SecretNotFound(t *testing.T) {
	s := testScheme(t)
	dnsRecord := newTestDNSRecord("test-rec", "default")
	dnsRecord.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	// No secret created — should fail to get API token
	mock := newMockDNSClient()

	r := buildReconciler(s, mock, dnsRecord)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error (should be handled gracefully): %v", err)
	}

	// Should requeue after 30s
	if result.RequeueAfter != 30*time.Second {
		t.Errorf("expected RequeueAfter=30s, got %v", result.RequeueAfter)
	}

	// Verify Ready condition is False with SecretNotFound reason
	var updated cloudflarev1alpha1.CloudflareDNSRecord
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-rec", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated record: %v", err)
	}

	foundCondition := false
	for _, c := range updated.Status.Conditions {
		if c.Type == cloudflarev1alpha1.ConditionTypeReady {
			foundCondition = true
			if c.Status != metav1.ConditionFalse {
				t.Errorf("expected Ready condition status=False, got %s", c.Status)
			}
			if c.Reason != cloudflarev1alpha1.ReasonSecretNotFound {
				t.Errorf("expected reason=%s, got %s", cloudflarev1alpha1.ReasonSecretNotFound, c.Reason)
			}
		}
	}
	if !foundCondition {
		t.Error("expected Ready condition to be set")
	}
}

func TestReconcile_DeletesRecord(t *testing.T) {
	s := testScheme(t)
	dnsRecord := newTestDNSRecord("test-rec", "default")
	dnsRecord.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	dnsRecord.Status.RecordID = "rec-delete"
	now := metav1.Now()
	dnsRecord.DeletionTimestamp = &now

	secret := newTestSecret("default")

	mock := newMockDNSClient()
	mock.records["rec-delete"] = &cfclient.DNSRecord{
		ID:      "rec-delete",
		Name:    "test.example.com",
		Type:    "A",
		Content: testDNSContent,
	}

	r := buildReconciler(s, mock, dnsRecord, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify delete was called on the mock
	if !mock.deleteCalled {
		t.Error("expected DeleteRecord to be called")
	}

	// Verify the record was removed from the mock
	if _, exists := mock.records["rec-delete"]; exists {
		t.Error("expected record to be removed from mock after deletion")
	}

	// The fake client with DeletionTimestamp set will garbage-collect the object
	// once the finalizer is removed, so we verify the object is gone (which proves
	// the finalizer was successfully removed).
	var updated cloudflarev1alpha1.CloudflareDNSRecord
	err = r.Get(context.Background(), types.NamespacedName{Name: "test-rec", Namespace: "default"}, &updated)
	if err == nil {
		// Object still exists — verify finalizer was removed
		for _, f := range updated.Finalizers {
			if f == cloudflarev1alpha1.FinalizerName {
				t.Error("expected finalizer to be removed after deletion")
			}
		}
	}
	// If err is not-found, the object was garbage-collected after finalizer removal — that's correct
}

func TestReconcile_SkipsUpToDateRecord(t *testing.T) {
	s := testScheme(t)
	dnsRecord := newTestDNSRecord("test-rec", "default")
	dnsRecord.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	dnsRecord.Status.RecordID = "rec-uptodate"
	secret := newTestSecret("default")

	mock := newMockDNSClient()
	proxied := false
	// Existing record that exactly matches desired state
	mock.records["rec-uptodate"] = &cfclient.DNSRecord{
		ID:      "rec-uptodate",
		Name:    "test.example.com",
		Type:    "A",
		Content: testDNSContent,
		Proxied: proxied,
		TTL:     1,
	}

	r := buildReconciler(s, mock, dnsRecord, secret)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should NOT have called create or update
	if mock.createCalled {
		t.Error("expected CreateRecord NOT to be called for up-to-date record")
	}
	if mock.updateCalled {
		t.Error("expected UpdateRecord NOT to be called for up-to-date record")
	}

	// Should still requeue on interval
	if result.RequeueAfter != 5*time.Minute {
		t.Errorf("expected RequeueAfter=5m, got %v", result.RequeueAfter)
	}

	// Verify status reflects current content
	var updated cloudflarev1alpha1.CloudflareDNSRecord
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-rec", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated record: %v", err)
	}
	if updated.Status.CurrentContent != testDNSContent {
		t.Errorf("expected CurrentContent=1.2.3.4, got %q", updated.Status.CurrentContent)
	}
}

func TestReconcile_NotFoundReturnsNoError(t *testing.T) {
	s := testScheme(t)
	mock := newMockDNSClient()

	// No CR exists — reconcile should return cleanly
	r := buildReconciler(s, mock)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("expected no error for not-found CR, got: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected empty result for not-found CR, got %+v", result)
	}
}

func TestDNSReconcile_ZoneRefResolvesFromCloudflareZone(t *testing.T) {
	s := testScheme(t)
	mock := newMockDNSClient()

	// Create a CloudflareZone with status.zoneID set
	zone := &cloudflarev1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-zone",
			Namespace: "default",
		},
		Spec: cloudflarev1alpha1.CloudflareZoneSpec{
			Name:      "example.com",
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "cf-secret"},
		},
	}

	// Create a CloudflareDNSRecord using zoneRef (not zoneID)
	content := testDNSContent
	proxied := false
	dnsRecord := &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-rec",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cloudflarev1alpha1.FinalizerName},
		},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			ZoneRef:   &cloudflarev1alpha1.ZoneReference{Name: "my-zone"},
			Name:      "test.example.com",
			Type:      "A",
			Content:   &content,
			TTL:       1,
			Proxied:   &proxied,
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "cf-secret"},
			Interval:  &metav1.Duration{Duration: 5 * time.Minute},
		},
	}

	secret := newTestSecret("default")

	r := buildReconciler(s, mock, zone, dnsRecord, secret)

	// Set the CloudflareZone status after creation (fake client requires Status().Update())
	zone.Status.ZoneID = testResolvedZoneID
	zone.Status.Status = testZoneActive
	if err := r.Status().Update(context.Background(), zone); err != nil {
		t.Fatalf("failed to update zone status: %v", err)
	}

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the mock DNS client's CreateRecord was called with the resolved zone ID
	if !mock.createCalled {
		t.Error("expected CreateRecord to be called after resolving zone ID from CloudflareZone")
	}
	if mock.lastZoneID != testResolvedZoneID {
		t.Errorf("expected zone ID passed to DNS client to be %q, got %q", testResolvedZoneID, mock.lastZoneID)
	}

	// Should requeue after interval
	if result.RequeueAfter != 5*time.Minute {
		t.Errorf("expected RequeueAfter=5m, got %v", result.RequeueAfter)
	}
}

func TestDNSReconcile_ZoneRefNotReady(t *testing.T) {
	s := testScheme(t)
	mock := newMockDNSClient()

	// Create a CloudflareZone with NO status.zoneID (pending zone)
	zone := &cloudflarev1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pending-zone",
			Namespace: "default",
		},
		Spec: cloudflarev1alpha1.CloudflareZoneSpec{
			Name:      "pending.com",
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "cf-secret"},
		},
	}

	// Create a CloudflareDNSRecord using zoneRef pointing to the pending zone
	content := testDNSContent
	proxied := false
	dnsRecord := &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-rec",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cloudflarev1alpha1.FinalizerName},
		},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			ZoneRef:   &cloudflarev1alpha1.ZoneReference{Name: "pending-zone"},
			Name:      "test.example.com",
			Type:      "A",
			Content:   &content,
			TTL:       1,
			Proxied:   &proxied,
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "cf-secret"},
			Interval:  &metav1.Duration{Duration: 5 * time.Minute},
		},
	}

	secret := newTestSecret("default")

	r := buildReconciler(s, mock, zone, dnsRecord, secret)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error (should be handled gracefully): %v", err)
	}

	// Should requeue after 30s
	if result.RequeueAfter != 30*time.Second {
		t.Errorf("expected RequeueAfter=30s, got %v", result.RequeueAfter)
	}

	// Verify Ready condition is False with ZoneRefNotReady reason
	var updated cloudflarev1alpha1.CloudflareDNSRecord
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-rec", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated record: %v", err)
	}

	foundCondition := false
	for _, c := range updated.Status.Conditions {
		if c.Type == cloudflarev1alpha1.ConditionTypeReady {
			foundCondition = true
			if c.Status != metav1.ConditionFalse {
				t.Errorf("expected Ready condition status=False, got %s", c.Status)
			}
			if c.Reason != cloudflarev1alpha1.ReasonZoneRefNotReady {
				t.Errorf("expected reason=%s, got %s", cloudflarev1alpha1.ReasonZoneRefNotReady, c.Reason)
			}
		}
	}
	if !foundCondition {
		t.Error("expected Ready condition to be set")
	}
}

func TestDNSReconcile_ZoneRefDeleteWithResolvedZone(t *testing.T) {
	s := testScheme(t)
	mock := newMockDNSClient()

	// CloudflareZone with status
	zone := &cloudflarev1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-zone",
			Namespace: "default",
		},
		Spec: cloudflarev1alpha1.CloudflareZoneSpec{
			Name:      "example.com",
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "cf-secret"},
		},
	}

	// DNS record marked for deletion with zoneRef
	content := testDNSContent
	proxied := false
	now := metav1.Now()
	dnsRecord := &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-rec",
			Namespace:         "default",
			Generation:        1,
			Finalizers:        []string{cloudflarev1alpha1.FinalizerName},
			DeletionTimestamp: &now,
		},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			ZoneRef:   &cloudflarev1alpha1.ZoneReference{Name: "my-zone"},
			Name:      "test.example.com",
			Type:      "A",
			Content:   &content,
			TTL:       1,
			Proxied:   &proxied,
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "cf-secret"},
			Interval:  &metav1.Duration{Duration: 5 * time.Minute},
		},
		Status: cloudflarev1alpha1.CloudflareDNSRecordStatus{
			RecordID: "existing-record-id",
		},
	}

	secret := newTestSecret("default")
	r := buildReconciler(s, mock, zone, dnsRecord, secret)

	// Set zone status
	zone.Status.ZoneID = testResolvedZoneID
	zone.Status.Status = testZoneActive
	if err := r.Status().Update(context.Background(), zone); err != nil {
		t.Fatalf("failed to update zone status: %v", err)
	}

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mock.deleteCalled {
		t.Error("expected DeleteRecord to be called")
	}
	if mock.lastZoneID != testResolvedZoneID {
		t.Errorf("expected zone ID %q for delete, got %q", testResolvedZoneID, mock.lastZoneID)
	}
}

func TestDNSReconcile_ZoneRefDeleteZoneNotResolvable(t *testing.T) {
	s := testScheme(t)
	mock := newMockDNSClient()

	// DNS record marked for deletion, but the referenced zone doesn't exist
	content := testDNSContent
	proxied := false
	now := metav1.Now()
	dnsRecord := &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-rec",
			Namespace:         "default",
			Generation:        1,
			Finalizers:        []string{cloudflarev1alpha1.FinalizerName},
			DeletionTimestamp: &now,
		},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			ZoneRef:   &cloudflarev1alpha1.ZoneReference{Name: "deleted-zone"},
			Name:      "test.example.com",
			Type:      "A",
			Content:   &content,
			TTL:       1,
			Proxied:   &proxied,
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "cf-secret"},
			Interval:  &metav1.Duration{Duration: 5 * time.Minute},
		},
		Status: cloudflarev1alpha1.CloudflareDNSRecordStatus{
			RecordID: "existing-record-id",
		},
	}

	secret := newTestSecret("default")
	r := buildReconciler(s, mock, dnsRecord, secret)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.RequeueAfter != 30*time.Second {
		t.Errorf("expected RequeueAfter=30s, got %v", result.RequeueAfter)
	}

	if mock.deleteCalled {
		t.Error("DeleteRecord should NOT be called when zone can't be resolved")
	}
}
