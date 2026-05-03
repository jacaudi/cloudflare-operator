package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/shared"
	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	cfclient "github.com/jacaudi/cloudflare-operator/internal/cloudflare"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
	if err := appsv1.AddToScheme(s); err != nil {
		t.Fatalf("failed to add appsv1 to scheme: %v", err)
	}
	if err := policyv1.AddToScheme(s); err != nil {
		t.Fatalf("failed to add policyv1 to scheme: %v", err)
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

// TestReconcile_SecretRefCrossNamespace is a regression test for issue #70.
// When the CloudflareDNSRecord lives in one namespace ("apps") but the
// credentials Secret lives in another ("zones") — typical when an HTTPRoute or
// Service source emits a DNSRecord referencing a Secret co-located with the
// zone — the controller must honor Spec.SecretRef.Namespace and resolve the
// Secret in that namespace. Without the fix, GetAPIToken would look in the
// CR's own namespace and fail.
func TestReconcile_SecretRefCrossNamespace(t *testing.T) {
	s := testScheme(t)
	dnsRecord := newTestDNSRecord("test-rec", "apps")
	dnsRecord.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	// Point SecretRef at the Secret in "zones", not "apps".
	dnsRecord.Spec.SecretRef = cloudflarev1alpha1.SecretReference{
		Name:      "cf-secret",
		Namespace: "zones",
	}
	secret := newTestSecret("zones")
	mock := newMockDNSClient()

	r := buildReconciler(s, mock, dnsRecord, secret)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "apps"},
	})
	if err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}

	// Reconcile should reach the create path (5m requeue), not fall through
	// to a 30s SecretNotFound requeue.
	if result.RequeueAfter != 5*time.Minute {
		t.Errorf("expected RequeueAfter=5m (success), got %v", result.RequeueAfter)
	}

	// Verify Ready condition is True (Secret was resolved + record created).
	var updated cloudflarev1alpha1.CloudflareDNSRecord
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-rec", Namespace: "apps"}, &updated); err != nil {
		t.Fatalf("failed to get updated record: %v", err)
	}
	for _, c := range updated.Status.Conditions {
		if c.Type == cloudflarev1alpha1.ConditionTypeReady {
			if c.Reason == cloudflarev1alpha1.ReasonSecretNotFound {
				t.Errorf("expected Secret to be resolved across namespaces, got SecretNotFound: %s", c.Message)
			}
		}
	}
	if !mock.createCalled {
		t.Error("expected CreateRecord to be called (Secret resolved cross-namespace)")
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

// buildReconcilerWithRegistry is like buildReconciler but wires in a RegistryConfig.
// It accepts any cfclient.DNSClient (e.g. *mockDNSClient or *capturingMockDNSClient).
func buildReconcilerWithRegistry(s *runtime.Scheme, dnsClient cfclient.DNSClient, reg RegistryConfig, objs ...client.Object) *CloudflareDNSRecordReconciler {
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
	r := &CloudflareDNSRecordReconciler{
		Client:        fakeClient,
		Scheme:        s,
		Recorder:      record.NewFakeRecorder(10),
		ClientFactory: cfclient.NewClientFactory(fakeClient),
		Registry:      reg,
		DNSClientFn: func(_ string) cfclient.DNSClient {
			return dnsClient
		},
	}
	return r
}

// newTestDNSRecordWithLabels adds source labels so writeRegistryTXT can build a payload.
func newTestDNSRecordWithLabels(name, namespace string) *cloudflarev1alpha1.CloudflareDNSRecord {
	rec := newTestDNSRecord(name, namespace)
	rec.Labels = map[string]string{
		LabelSourceKind:      "httproute",
		LabelSourceNamespace: "default",
		LabelSourceName:      "my-route",
	}
	return rec
}

// TestCloudflareDNSRecord_EmptyRegistryConfig_NoRegressions verifies that a
// zero-value RegistryConfig (TxtOwnerID == "") leaves existing reconcile
// behaviour completely unchanged.
func TestCloudflareDNSRecord_EmptyRegistryConfig_NoRegressions(t *testing.T) {
	s := testScheme(t)
	dnsRecord := newTestDNSRecord("test-rec", "default")
	dnsRecord.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	secret := newTestSecret("default")
	mock := newMockDNSClient()

	r := buildReconcilerWithRegistry(s, mock, RegistryConfig{}, dnsRecord, secret)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 5*time.Minute {
		t.Errorf("expected RequeueAfter=5m, got %v", result.RequeueAfter)
	}
	if !mock.createCalled {
		t.Error("expected CreateRecord to be called in no-registry path")
	}
	// No companion TXT should have been written.
	var updated cloudflarev1alpha1.CloudflareDNSRecord
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-rec", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated record: %v", err)
	}
	if updated.Status.RecordID == "" {
		t.Error("expected RecordID to be set in status")
	}
}

// TestRegistry_SkipForTXTRecordType verifies that records with Spec.Type == "TXT"
// skip the registry check entirely (no recursive companion TXT for TXTs).
func TestRegistry_SkipForTXTRecordType(t *testing.T) {
	s := testScheme(t)
	content := "v=spf1 include:example.com ~all"
	proxied := false
	dnsRecord := &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "spf-rec",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cloudflarev1alpha1.FinalizerName},
			Labels: map[string]string{
				LabelSourceKind:      "httproute",
				LabelSourceNamespace: "default",
				LabelSourceName:      "my-route",
			},
		},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			ZoneID:    "zone-abc",
			Name:      "example.com",
			Type:      testRecordTypeTXT,
			Content:   &content,
			TTL:       1,
			Proxied:   &proxied,
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "cf-secret"},
			Interval:  &metav1.Duration{Duration: 5 * time.Minute},
		},
	}
	secret := newTestSecret("default")
	mock := newMockDNSClient()

	r := buildReconcilerWithRegistry(s, mock, RegistryConfig{TxtOwnerID: "cloudflare-operator"}, dnsRecord, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "spf-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The record itself should be created, but no companion-TXT create should be
	// triggered for the TXT record (it would list for a companion, then skip writing).
	if !mock.createCalled {
		t.Error("expected CreateRecord to be called for the TXT record itself")
	}
}

// TestRegistry_SkipForRegistryTXTAnnotation verifies that a CloudflareDNSRecord
// annotated with cloudflare.io/registry-for (i.e. it IS a companion TXT) skips
// the registry check entirely.
func TestRegistry_SkipForRegistryTXTAnnotation(t *testing.T) {
	s := testScheme(t)
	content := `"heritage=external-dns,external-dns/owner=cloudflare-operator"`
	proxied := false
	dnsRecord := &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "registry-txt-rec",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cloudflarev1alpha1.FinalizerName},
			Annotations: map[string]string{
				AnnotationRegistryFor: "a-example.com",
			},
		},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			ZoneID:    "zone-abc",
			Name:      "a-example.com",
			Type:      testRecordTypeTXT,
			Content:   &content,
			TTL:       1,
			Proxied:   &proxied,
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "cf-secret"},
			Interval:  &metav1.Duration{Duration: 5 * time.Minute},
		},
	}
	secret := newTestSecret("default")
	mock := newMockDNSClient()

	r := buildReconcilerWithRegistry(s, mock, RegistryConfig{TxtOwnerID: "cloudflare-operator"}, dnsRecord, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "registry-txt-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should simply create the TXT record without entering registry logic.
	if !mock.createCalled {
		t.Error("expected CreateRecord to be called for the companion-TXT record itself")
	}
}

// TestRegistry_CreateWritesCompanionTXT verifies that when a new record is
// created and registry is enabled, a companion TXT is also written.
func TestRegistry_CreateWritesCompanionTXT(t *testing.T) {
	s := testScheme(t)
	dnsRecord := newTestDNSRecordWithLabels("test-rec", "default")
	dnsRecord.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	secret := newTestSecret("default")

	var createCalls []cfclient.DNSRecordParams
	mock := newMockDNSClient()
	mock.listOverride = func(zoneID, name, recordType string) ([]cfclient.DNSRecord, error) {
		return nil, nil
	}

	// We want to capture all creates
	capturer := &capturingMockDNSClient{mockDNSClient: mock}

	r := buildReconcilerWithRegistry(s, capturer, RegistryConfig{TxtOwnerID: "cloudflare-operator"}, dnsRecord, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	createCalls = capturer.createParams
	// Should have two creates: the main record and the companion TXT.
	if len(createCalls) < 2 {
		t.Fatalf("expected at least 2 CreateRecord calls (record + companion TXT), got %d", len(createCalls))
	}

	// Verify one of the creates is a TXT
	var foundTXT bool
	for _, p := range createCalls {
		if p.Type == testRecordTypeTXT {
			foundTXT = true
			// The TXT content should contain heritage=external-dns
			if p.Content == "" {
				t.Error("companion TXT content must not be empty")
			}
		}
	}
	if !foundTXT {
		t.Error("expected a TXT companion record to be created")
	}
}

// TestRegistry_ReconcileDoesNotRewriteTXT verifies that when our owner TXT
// already exists and the record matches, no extra writes occur for the TXT.
func TestRegistry_ReconcileDoesNotRewriteTXT(t *testing.T) {
	s := testScheme(t)
	dnsRecord := newTestDNSRecordWithLabels("test-rec", "default")
	dnsRecord.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	dnsRecord.Status.RecordID = "rec-existing"
	secret := newTestSecret("default")

	const ownerID = "cloudflare-operator"
	txtContent := `"heritage=external-dns,external-dns/owner=cloudflare-operator,external-dns/resource=httproute/default/my-route"`

	mock := newMockDNSClient()
	mock.records["rec-existing"] = &cfclient.DNSRecord{
		ID:      "rec-existing",
		Name:    "test.example.com",
		Type:    "A",
		Content: testDNSContent,
		TTL:     1,
	}
	// Return our TXT when queried for companion TXT name
	mock.listOverride = func(zoneID, name, recordType string) ([]cfclient.DNSRecord, error) {
		if recordType == testRecordTypeTXT {
			return []cfclient.DNSRecord{{
				ID:      "txt-existing",
				Name:    name,
				Type:    testRecordTypeTXT,
				Content: txtContent,
			}}, nil
		}
		return nil, nil
	}

	capturer := &capturingMockDNSClient{mockDNSClient: mock}
	r := buildReconcilerWithRegistry(s, capturer, RegistryConfig{TxtOwnerID: ownerID}, dnsRecord, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No creates should have occurred; maybe an update for TXT if it changed,
	// but never a create for the companion TXT since it already exists.
	for _, p := range capturer.createParams {
		if p.Type == testRecordTypeTXT {
			t.Errorf("did not expect a TXT create when companion TXT already owned by us: %+v", p)
		}
	}
}

// TestRegistry_RefuseForeignOwner verifies that when the companion TXT belongs
// to another owner, reconcile sets a failure condition and requeues with 5m.
func TestRegistry_RefuseForeignOwner(t *testing.T) {
	s := testScheme(t)
	dnsRecord := newTestDNSRecordWithLabels("test-rec", "default")
	dnsRecord.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	secret := newTestSecret("default")

	foreignTXT := `"heritage=external-dns,external-dns/owner=external-dns"`

	mock := newMockDNSClient()
	mock.listOverride = func(zoneID, name, recordType string) ([]cfclient.DNSRecord, error) {
		if recordType == testRecordTypeTXT {
			return []cfclient.DNSRecord{{
				ID:      "txt-foreign",
				Name:    name,
				Type:    testRecordTypeTXT,
				Content: foreignTXT,
			}}, nil
		}
		// Return an existing A record
		return []cfclient.DNSRecord{{
			ID:      "rec-foreign",
			Name:    "test.example.com",
			Type:    "A",
			Content: "9.9.9.9",
		}}, nil
	}

	r := buildReconcilerWithRegistry(s, mock, RegistryConfig{TxtOwnerID: "cloudflare-operator"}, dnsRecord, secret)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error (should be handled gracefully): %v", err)
	}
	// Should requeue after 5 minutes
	if result.RequeueAfter != 5*time.Minute {
		t.Errorf("expected RequeueAfter=5m for foreign owner, got %v", result.RequeueAfter)
	}
	// Should NOT have created or updated
	if mock.createCalled {
		t.Error("must NOT create record when foreign TXT owner")
	}
	if mock.updateCalled {
		t.Error("must NOT update record when foreign TXT owner")
	}
	// Status condition should reflect the conflict
	var updated cloudflarev1alpha1.CloudflareDNSRecord
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-rec", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated record: %v", err)
	}
	foundConflict := false
	for _, c := range updated.Status.Conditions {
		if c.Reason == cloudflarev1alpha1.ReasonRecordOwnershipConflict {
			foundConflict = true
		}
	}
	if !foundConflict {
		t.Errorf("expected RecordOwnershipConflict condition, got: %+v", updated.Status.Conditions)
	}
}

// TestRegistry_RefuseOrphan verifies that when an existing record has no TXT
// and adopt is not opted in, reconcile sets a TxtRegistryGap failure.
func TestRegistry_RefuseOrphan(t *testing.T) {
	s := testScheme(t)
	dnsRecord := newTestDNSRecordWithLabels("test-rec", "default")
	dnsRecord.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	secret := newTestSecret("default")

	mock := newMockDNSClient()
	mock.listOverride = func(zoneID, name, recordType string) ([]cfclient.DNSRecord, error) {
		if recordType == testRecordTypeTXT {
			return nil, nil // no companion TXT
		}
		return []cfclient.DNSRecord{{
			ID:      "rec-orphan",
			Name:    "test.example.com",
			Type:    "A",
			Content: "9.9.9.9",
		}}, nil
	}

	r := buildReconcilerWithRegistry(s, mock, RegistryConfig{TxtOwnerID: "cloudflare-operator"}, dnsRecord, secret)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error (should be handled gracefully): %v", err)
	}
	if result.RequeueAfter != 5*time.Minute {
		t.Errorf("expected RequeueAfter=5m for orphan, got %v", result.RequeueAfter)
	}
	if mock.createCalled {
		t.Error("must NOT create record for orphan without adopt opt-in")
	}
	var updated cloudflarev1alpha1.CloudflareDNSRecord
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-rec", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated record: %v", err)
	}
	foundGap := false
	for _, c := range updated.Status.Conditions {
		if c.Reason == cloudflarev1alpha1.ReasonTxtRegistryGap {
			foundGap = true
		}
	}
	if !foundGap {
		t.Errorf("expected TxtRegistryGap condition, got: %+v", updated.Status.Conditions)
	}
}

// TestRegistry_AdoptOrphan verifies that cloudflare.io/adopt=true on an
// orphaned record causes the TXT to be written and the record to be reconciled.
func TestRegistry_AdoptOrphan(t *testing.T) {
	s := testScheme(t)
	dnsRecord := newTestDNSRecordWithLabels("test-rec", "default")
	dnsRecord.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	dnsRecord.Annotations = map[string]string{
		AnnotationAdopt: AnnotationValueTrue,
	}
	// Keep labels
	dnsRecord.Labels = map[string]string{
		LabelSourceKind:      "httproute",
		LabelSourceNamespace: "default",
		LabelSourceName:      "my-route",
	}
	secret := newTestSecret("default")

	mock := newMockDNSClient()
	mock.listOverride = func(zoneID, name, recordType string) ([]cfclient.DNSRecord, error) {
		if recordType == testRecordTypeTXT {
			return nil, nil // no companion TXT
		}
		return []cfclient.DNSRecord{{
			ID:      "rec-orphan",
			Name:    "test.example.com",
			Type:    "A",
			Content: testDNSContent,
		}}, nil
	}

	capturer := &capturingMockDNSClient{mockDNSClient: mock}
	r := buildReconcilerWithRegistry(s, capturer, RegistryConfig{TxtOwnerID: "cloudflare-operator"}, dnsRecord, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A companion TXT should have been written
	var foundTXTWrite bool
	for _, p := range capturer.createParams {
		if p.Type == testRecordTypeTXT {
			foundTXTWrite = true
		}
	}
	for _, p := range capturer.updateParams {
		if p.Type == testRecordTypeTXT {
			foundTXTWrite = true
		}
	}
	if !foundTXTWrite {
		t.Error("expected companion TXT to be written during adopt-orphan")
	}
}

// TestRegistry_PlaintextDefault verifies that when TxtEncryptAESKey is nil,
// the companion TXT payload is written as plaintext (contains "heritage=external-dns").
func TestRegistry_PlaintextDefault(t *testing.T) {
	s := testScheme(t)
	dnsRecord := newTestDNSRecordWithLabels("test-rec", "default")
	dnsRecord.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	secret := newTestSecret("default")

	capturer := &capturingMockDNSClient{mockDNSClient: newMockDNSClient()}

	r := buildReconcilerWithRegistry(s, capturer, RegistryConfig{
		TxtOwnerID:       "cloudflare-operator",
		TxtEncryptAESKey: nil, // explicit plaintext
	}, dnsRecord, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var txtContent string
	for _, p := range capturer.createParams {
		if p.Type == testRecordTypeTXT {
			txtContent = p.Content
		}
	}
	if txtContent == "" {
		t.Fatal("expected companion TXT to be written")
	}
	// Plaintext must contain the heritage token
	if !strings.Contains(txtContent, "heritage=external-dns") {
		t.Errorf("expected plaintext TXT to contain 'heritage=external-dns', got: %q", txtContent)
	}
}

// TestRegistry_EncryptionSmoke verifies that when TxtEncryptAESKey is set,
// the companion TXT is NOT plaintext (it's base64 ciphertext that doesn't
// contain "heritage=external-dns" literally).
func TestRegistry_EncryptionSmoke(t *testing.T) {
	s := testScheme(t)
	dnsRecord := newTestDNSRecordWithLabels("test-rec", "default")
	dnsRecord.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	secret := newTestSecret("default")

	key := make([]byte, 32) // 32 zero bytes — valid AES-256 key
	capturer := &capturingMockDNSClient{mockDNSClient: newMockDNSClient()}

	r := buildReconcilerWithRegistry(s, capturer, RegistryConfig{
		TxtOwnerID:       "cloudflare-operator",
		TxtEncryptAESKey: key,
	}, dnsRecord, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var txtContent string
	for _, p := range capturer.createParams {
		if p.Type == testRecordTypeTXT {
			txtContent = p.Content
		}
	}
	if txtContent == "" {
		t.Fatal("expected companion TXT to be written")
	}
	// Encrypted content must NOT contain the plaintext heritage token directly
	if strings.Contains(txtContent, "heritage=external-dns") {
		t.Errorf("expected encrypted TXT to NOT contain literal 'heritage=external-dns', got: %q", txtContent)
	}
}

// capturingMockDNSClient wraps mockDNSClient to capture all create/update params.
type capturingMockDNSClient struct {
	*mockDNSClient
	createParams []cfclient.DNSRecordParams
	updateParams []cfclient.DNSRecordParams
}

func (c *capturingMockDNSClient) CreateRecord(ctx context.Context, zoneID string, params cfclient.DNSRecordParams) (*cfclient.DNSRecord, error) {
	c.createParams = append(c.createParams, params)
	return c.mockDNSClient.CreateRecord(ctx, zoneID, params)
}

func (c *capturingMockDNSClient) UpdateRecord(ctx context.Context, zoneID, recordID string, params cfclient.DNSRecordParams) (*cfclient.DNSRecord, error) {
	c.updateParams = append(c.updateParams, params)
	return c.mockDNSClient.UpdateRecord(ctx, zoneID, recordID, params)
}

// TestRegistry_FailedMainCreate_NoOrphanTXT verifies that if the main
// CreateRecord call fails, no companion TXT is written (orphan-TXT prevention).
// Plan §11.5c: companion TXT is written AFTER the main record write succeeds.
func TestRegistry_FailedMainCreate_NoOrphanTXT(t *testing.T) {
	s := testScheme(t)
	dnsRecord := newTestDNSRecordWithLabels("test-rec", "default")
	dnsRecord.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	secret := newTestSecret("default")

	mock := newMockDNSClient()
	// CreateRecord fails for all record types — this simulates a Cloudflare error
	// on the main A-record write. We track calls via capturer to distinguish
	// main-record creates from TXT creates.
	mock.createErr = fmt.Errorf("simulated Cloudflare create error")

	capturer := &capturingMockDNSClient{mockDNSClient: mock}
	r := buildReconcilerWithRegistry(s, capturer, RegistryConfig{TxtOwnerID: "cloudflare-operator"}, dnsRecord, secret)

	// The controller handles the create error gracefully (sets status condition,
	// requeues with 1-minute backoff) and returns nil from Reconcile.
	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error (controller should handle create failure gracefully): %v", err)
	}
	// Should requeue (not zero) because create failed.
	if result.RequeueAfter == 0 {
		t.Error("expected non-zero RequeueAfter when CreateRecord fails")
	}

	// No companion TXT must have been written when main create fails.
	for _, p := range capturer.createParams {
		if p.Type == testRecordTypeTXT {
			t.Errorf("companion TXT must NOT be written when main CreateRecord fails, got: %+v", p)
		}
	}
	for _, p := range capturer.updateParams {
		if p.Type == testRecordTypeTXT {
			t.Errorf("companion TXT must NOT be updated when main CreateRecord fails, got: %+v", p)
		}
	}
}

// TestRegistry_DecryptFailure_RefusedAsForeign verifies that when a TXT
// exists but cannot be decrypted with the configured import keys, the
// reconcile routes to ReasonRecordOwnershipConflict with 5-min requeue.
func TestRegistry_DecryptFailure_RefusedAsForeign(t *testing.T) {
	s := testScheme(t)
	dnsRecord := newTestDNSRecordWithLabels("test-rec", "default")
	dnsRecord.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	secret := newTestSecret("default")

	// A valid-base64 blob that looks encrypted (≥32 bytes, block-aligned) but
	// was NOT encrypted with our key — decryption will fail or produce garbage
	// that fails the heritage sanity check.
	badCiphertext := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

	mock := newMockDNSClient()
	mock.listOverride = func(zoneID, name, recordType string) ([]cfclient.DNSRecord, error) {
		if recordType == testRecordTypeTXT {
			return []cfclient.DNSRecord{{
				ID:      "txt-garbled",
				Name:    name,
				Type:    testRecordTypeTXT,
				Content: badCiphertext,
			}}, nil
		}
		return []cfclient.DNSRecord{{
			ID:      "rec-1",
			Name:    "test.example.com",
			Type:    "A",
			Content: testDNSContent,
		}}, nil
	}

	// Provide a key so decryption is attempted (not skipped as plaintext).
	key := make([]byte, 32) // 32 zero-bytes — valid AES-256 key
	r := buildReconcilerWithRegistry(s, mock, RegistryConfig{
		TxtOwnerID:           "cloudflare-operator",
		TxtImportDecryptKeys: [][]byte{key},
	}, dnsRecord, secret)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error (should be handled gracefully): %v", err)
	}
	if result.RequeueAfter != 5*time.Minute {
		t.Errorf("expected RequeueAfter=5m for decrypt failure, got %v", result.RequeueAfter)
	}

	// Status must reflect RecordOwnershipConflict (decrypt failure → foreign).
	var updated cloudflarev1alpha1.CloudflareDNSRecord
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-rec", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated record: %v", err)
	}
	var foundConflict bool
	for _, c := range updated.Status.Conditions {
		if c.Reason == cloudflarev1alpha1.ReasonRecordOwnershipConflict {
			foundConflict = true
		}
	}
	if !foundConflict {
		t.Errorf("expected RecordOwnershipConflict condition for decrypt failure, got: %+v", updated.Status.Conditions)
	}
}

// TestRegistry_DecodeFailure_Refused verifies that a TXT that passes
// DecryptPayload (plaintext passthrough) but fails DecodeRegistryPayload
// routes to RefuseForeignOwner / ReasonRecordOwnershipConflict.
func TestRegistry_DecodeFailure_Refused(t *testing.T) {
	s := testScheme(t)
	dnsRecord := newTestDNSRecordWithLabels("test-rec", "default")
	dnsRecord.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	secret := newTestSecret("default")

	// Valid quoted string but NOT a heritage payload — DecodeRegistryPayload fails.
	invalidPayload := `"random-bytes-no-heritage"`

	mock := newMockDNSClient()
	mock.listOverride = func(zoneID, name, recordType string) ([]cfclient.DNSRecord, error) {
		if recordType == testRecordTypeTXT {
			return []cfclient.DNSRecord{{
				ID:      "txt-bad",
				Name:    name,
				Type:    testRecordTypeTXT,
				Content: invalidPayload,
			}}, nil
		}
		return []cfclient.DNSRecord{{
			ID:      "rec-1",
			Name:    "test.example.com",
			Type:    "A",
			Content: testDNSContent,
		}}, nil
	}

	r := buildReconcilerWithRegistry(s, mock, RegistryConfig{TxtOwnerID: "cloudflare-operator"}, dnsRecord, secret)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error (should be handled gracefully): %v", err)
	}
	if result.RequeueAfter != 5*time.Minute {
		t.Errorf("expected RequeueAfter=5m for decode failure, got %v", result.RequeueAfter)
	}

	var updated cloudflarev1alpha1.CloudflareDNSRecord
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-rec", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated record: %v", err)
	}
	var foundConflict bool
	for _, c := range updated.Status.Conditions {
		if c.Reason == cloudflarev1alpha1.ReasonRecordOwnershipConflict {
			foundConflict = true
		}
	}
	if !foundConflict {
		t.Errorf("expected RecordOwnershipConflict condition for decode failure, got: %+v", updated.Status.Conditions)
	}
}

// TestRegistry_AdoptOrphan_AssertMainRecordWrite verifies that adopt-orphan
// writes the companion TXT AND falls through to create/update the main record.
func TestRegistry_AdoptOrphan_AssertMainRecordWrite(t *testing.T) {
	s := testScheme(t)
	dnsRecord := newTestDNSRecordWithLabels("test-rec", "default")
	dnsRecord.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	dnsRecord.Annotations = map[string]string{AnnotationAdopt: AnnotationValueTrue}
	dnsRecord.Labels = map[string]string{
		LabelSourceKind:      "httproute",
		LabelSourceNamespace: "default",
		LabelSourceName:      "my-route",
	}
	secret := newTestSecret("default")

	// Pre-existing A record with no companion TXT → AdoptOrphan path.
	existingRecord := &cfclient.DNSRecord{
		ID:      "rec-orphan",
		Name:    "test.example.com",
		Type:    "A",
		Content: testDNSContent,
	}
	mock := newMockDNSClient()
	mock.records["rec-orphan"] = existingRecord
	mock.listOverride = func(zoneID, name, recordType string) ([]cfclient.DNSRecord, error) {
		if recordType == testRecordTypeTXT {
			return nil, nil // no companion TXT
		}
		return []cfclient.DNSRecord{*existingRecord}, nil
	}

	capturer := &capturingMockDNSClient{mockDNSClient: mock}
	r := buildReconcilerWithRegistry(s, capturer, RegistryConfig{TxtOwnerID: "cloudflare-operator"}, dnsRecord, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-rec", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Companion TXT must be written during adopt.
	var foundTXTWrite bool
	for _, p := range capturer.createParams {
		if p.Type == testRecordTypeTXT {
			foundTXTWrite = true
		}
	}
	for _, p := range capturer.updateParams {
		if p.Type == testRecordTypeTXT {
			foundTXTWrite = true
		}
	}
	if !foundTXTWrite {
		t.Error("expected companion TXT to be written during adopt-orphan")
	}

	// Main record should also have been processed (not refused).
	// The existing record already has matching content, so no update needed.
	// But the status should reflect the record is managed.
	var updated cloudflarev1alpha1.CloudflareDNSRecord
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-rec", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated record: %v", err)
	}
	if updated.Status.RecordID == "" {
		t.Error("expected RecordID to be set after adopt-orphan reconcile")
	}
}

// TestRegistry_WriteRegistryTXT_UpdatesExistingTXT verifies that when a
// companion TXT already exists at the affixed FQDN, writeRegistryTXT calls
// UpdateRecord (not CreateRecord) on that TXT.
func TestRegistry_WriteRegistryTXT_UpdatesExistingTXT(t *testing.T) {
	s := testScheme(t)
	dnsRecord := newTestDNSRecordWithLabels("test-rec", "default")
	dnsRecord.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	secret := newTestSecret("default")

	const ownerID = "cloudflare-operator"
	// Stale TXT — same owner but different source labels (old source name).
	staleTXTContent := `"heritage=external-dns,external-dns/owner=cloudflare-operator,external-dns/resource=httproute/default/old-route"`

	// The affixed name for "test.example.com" type "A" with default config is "a-test.example.com".
	affixedName := cfclient.AffixName("test.example.com", "A", cfclient.AffixConfig{})

	mock := newMockDNSClient()
	// Pre-seed existing main A record (so registry decision is Reconcile, not Create).
	mock.records["rec-existing"] = &cfclient.DNSRecord{
		ID:      "rec-existing",
		Name:    "test.example.com",
		Type:    "A",
		Content: testDNSContent,
		TTL:     1,
	}
	// Pre-seed existing companion TXT at the affixed name.
	mock.records["txt-stale"] = &cfclient.DNSRecord{
		ID:      "txt-stale",
		Name:    affixedName,
		Type:    testRecordTypeTXT,
		Content: staleTXTContent,
	}
	mock.listOverride = func(zoneID, name, recordType string) ([]cfclient.DNSRecord, error) {
		if recordType == testRecordTypeTXT {
			return []cfclient.DNSRecord{{
				ID:      "txt-stale",
				Name:    name,
				Type:    testRecordTypeTXT,
				Content: staleTXTContent,
			}}, nil
		}
		return []cfclient.DNSRecord{*mock.records["rec-existing"]}, nil
	}

	capturer := &capturingMockDNSClient{mockDNSClient: mock}
	r := buildReconcilerWithRegistry(s, capturer, RegistryConfig{TxtOwnerID: ownerID}, dnsRecord, secret)

	// Call writeRegistryTXT directly — this is the unit under test.
	err := r.writeRegistryTXT(context.Background(), dnsRecord, capturer, "zone-abc")
	if err != nil {
		t.Fatalf("writeRegistryTXT returned error: %v", err)
	}

	// Must have called UpdateRecord, not CreateRecord, on the TXT.
	var txtUpdated bool
	for _, p := range capturer.updateParams {
		if p.Type == testRecordTypeTXT {
			txtUpdated = true
			// The content should now reflect current source labels (my-route).
			if !strings.Contains(p.Content, "my-route") {
				t.Errorf("updated TXT content should reflect current source, got: %q", p.Content)
			}
		}
	}
	if !txtUpdated {
		t.Error("expected UpdateRecord to be called for stale companion TXT")
	}

	// Must NOT have created a new TXT.
	for _, p := range capturer.createParams {
		if p.Type == testRecordTypeTXT {
			t.Errorf("expected no TXT CreateRecord when stale TXT exists (should update), got: %+v", p)
		}
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

// TestCloudflareDNSRecordReconciler_NotFoundRoutesToRemoteGone verifies that a
// 404 from the Cloudflare API routes the reconciler to the ReasonRemoteGone
// condition and an immediate requeue. Note: Status.RecordID is cleared by the
// in-flow ID-recovery path (lines 240-246 in reconcileRecord) when mock.getErr
// is set, NOT by the classifier's ResetRemoteID flag. The classifier's reset
// semantic is unit-tested in error_routing_test.go::TestClassifyCloudflareError.
func TestCloudflareDNSRecordReconciler_NotFoundRoutesToRemoteGone(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := cloudflarev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	dns := &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "rec",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cloudflarev1alpha1.FinalizerName},
		},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			ZoneID:    "zone-1",
			Name:      "x.example.com",
			Type:      cloudflarev1alpha1.DNSRecordTypeA,
			Content:   strPtr("1.2.3.4"),
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "creds"},
		},
		Status: cloudflarev1alpha1.CloudflareDNSRecordStatus{
			RecordID: "rec-stale",
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "default"},
		Data:       map[string][]byte{cfclient.SecretKeyAPIToken: []byte("token")},
	}

	mock := newMockDNSClient()
	mock.getErr = errors.New("transient")
	mock.listErr = &cfgo.Error{StatusCode: http.StatusNotFound}

	r := buildReconciler(scheme, mock, dns, secret)

	res, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(dns),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RequeueAfter != time.Duration(0) {
		t.Errorf("RemoteGone is immediate-requeue: expected RequeueAfter=0, got %v", res.RequeueAfter)
	}

	updated := &cloudflarev1alpha1.CloudflareDNSRecord{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(dns), updated); err != nil {
		t.Fatalf("failed to get updated record: %v", err)
	}
	cond := meta.FindStatusCondition(updated.Status.Conditions, cloudflarev1alpha1.ConditionTypeReady)
	if cond == nil {
		t.Fatal("expected Ready condition to be set")
	}
	if cond.Reason != cloudflarev1alpha1.ReasonRemoteGone {
		t.Errorf("expected reason=%s, got %s", cloudflarev1alpha1.ReasonRemoteGone, cond.Reason)
	}
}

func TestCloudflareDNSRecordReconciler_BadRequestSetsInvalidSpec(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := cloudflarev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	dns := &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "rec",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cloudflarev1alpha1.FinalizerName},
		},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			ZoneID:    "zone-1",
			Name:      "x.example.com",
			Type:      cloudflarev1alpha1.DNSRecordTypeA,
			Content:   strPtr("1.2.3.4"),
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "creds"},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "default"},
		Data:       map[string][]byte{cfclient.SecretKeyAPIToken: []byte("token")},
	}
	mock := newMockDNSClient()
	mock.createErr = &cfgo.Error{StatusCode: http.StatusBadRequest}

	r := buildReconciler(scheme, mock, dns, secret)
	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(dns)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RequeueAfter != time.Hour {
		t.Errorf("expected RequeueAfter=1h, got %v", res.RequeueAfter)
	}

	updated := &cloudflarev1alpha1.CloudflareDNSRecord{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(dns), updated); err != nil {
		t.Fatalf("failed to get updated record: %v", err)
	}
	cond := meta.FindStatusCondition(updated.Status.Conditions, cloudflarev1alpha1.ConditionTypeReady)
	if cond == nil {
		t.Fatal("expected Ready condition to be set")
	}
	if cond.Reason != cloudflarev1alpha1.ReasonInvalidSpec {
		t.Errorf("expected reason=%s, got %s", cloudflarev1alpha1.ReasonInvalidSpec, cond.Reason)
	}
}

func TestCloudflareDNSRecordReconciler_PlanTier1015(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := cloudflarev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	dns := &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "rec",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cloudflarev1alpha1.FinalizerName},
		},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			ZoneID:    "zone-1",
			Name:      "x.example.com",
			Type:      cloudflarev1alpha1.DNSRecordTypeA,
			Content:   strPtr("1.2.3.4"),
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "creds"},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "default"},
		Data:       map[string][]byte{cfclient.SecretKeyAPIToken: []byte("token")},
	}
	mock := newMockDNSClient()
	mock.createErr = &cfgo.Error{
		StatusCode: http.StatusForbidden,
		Errors:     []shared.ErrorData{{Code: 1015}},
	}

	r := buildReconciler(scheme, mock, dns, secret)
	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(dns)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RequeueAfter != time.Hour {
		t.Errorf("expected RequeueAfter=1h, got %v", res.RequeueAfter)
	}

	updated := &cloudflarev1alpha1.CloudflareDNSRecord{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(dns), updated); err != nil {
		t.Fatalf("failed to get updated record: %v", err)
	}
	cond := meta.FindStatusCondition(updated.Status.Conditions, cloudflarev1alpha1.ConditionTypeReady)
	if cond == nil {
		t.Fatal("expected Ready condition to be set")
	}
	if cond.Reason != cloudflarev1alpha1.ReasonPlanTierRequired {
		t.Errorf("expected reason=%s, got %s", cloudflarev1alpha1.ReasonPlanTierRequired, cond.Reason)
	}
}

// TestCloudflareDNSRecordReconciler_BadRequest_EmitsInvalidSpecEvent asserts that
// a 400 Bad Request from the Cloudflare API emits an "InvalidSpec" recorder event,
// NOT the legacy "SyncFailed" event. A future regression that reverts to hardcoded
// "SyncFailed" for classified errors will be caught here.
func TestCloudflareDNSRecordReconciler_BadRequest_EmitsInvalidSpecEvent(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := cloudflarev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	dns := &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "rec",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cloudflarev1alpha1.FinalizerName},
		},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			ZoneID:    "zone-1",
			Name:      "x.example.com",
			Type:      cloudflarev1alpha1.DNSRecordTypeA,
			Content:   strPtr("1.2.3.4"),
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "creds"},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "default"},
		Data:       map[string][]byte{cfclient.SecretKeyAPIToken: []byte("token")},
	}
	mock := newMockDNSClient()
	mock.createErr = &cfgo.Error{StatusCode: http.StatusBadRequest}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(dns, secret).
		WithStatusSubresource(dns).
		Build()
	fakeRec := record.NewFakeRecorder(10)
	r := &CloudflareDNSRecordReconciler{
		Client:        fakeClient,
		Scheme:        scheme,
		Recorder:      fakeRec,
		ClientFactory: cfclient.NewClientFactory(fakeClient),
		DNSClientFn: func(_ string) cfclient.DNSClient {
			return mock
		},
	}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(dns)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	close(fakeRec.Events)
	var sawInvalidSpec bool
	for ev := range fakeRec.Events {
		if strings.Contains(ev, "InvalidSpec") {
			sawInvalidSpec = true
		}
		if strings.Contains(ev, "SyncFailed") {
			t.Errorf("unexpected SyncFailed event for classified InvalidSpec failure: %q", ev)
		}
	}
	if !sawInvalidSpec {
		t.Error("expected InvalidSpec event from classifier")
	}
}

func TestCloudflareDNSRecordReconciler_DeleteRecordNotFound_RemovesFinalizer(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := cloudflarev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	now := metav1.Now()
	dns := &cloudflarev1alpha1.CloudflareDNSRecord{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "rec",
			Namespace:         "default",
			Generation:        1,
			Finalizers:        []string{cloudflarev1alpha1.FinalizerName},
			DeletionTimestamp: &now,
		},
		Spec: cloudflarev1alpha1.CloudflareDNSRecordSpec{
			ZoneID:    "zone-1",
			Name:      "x.example.com",
			Type:      cloudflarev1alpha1.DNSRecordTypeA,
			Content:   strPtr("1.2.3.4"),
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "creds"},
		},
		Status: cloudflarev1alpha1.CloudflareDNSRecordStatus{
			RecordID: "rec-already-gone",
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "default"},
		Data:       map[string][]byte{cfclient.SecretKeyAPIToken: []byte("token")},
	}
	mock := newMockDNSClient()
	mock.deleteErr = &cfgo.Error{StatusCode: http.StatusNotFound}

	r := buildReconciler(scheme, mock, dns, secret)
	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(dns)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RequeueAfter != time.Duration(0) {
		t.Errorf("delete-404 should be success-equivalent, no requeue loop: got %v", res.RequeueAfter)
	}

	got := &cloudflarev1alpha1.CloudflareDNSRecord{}
	getErr := r.Get(context.Background(), client.ObjectKeyFromObject(dns), got)
	if getErr == nil {
		if len(got.Finalizers) != 0 {
			t.Errorf("finalizer must be removed so deletion completes; got finalizers: %v", got.Finalizers)
		}
	} else if !apierrors.IsNotFound(getErr) {
		t.Fatalf("unexpected error: %v", getErr)
	}
}
