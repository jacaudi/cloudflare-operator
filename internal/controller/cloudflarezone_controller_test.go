package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	cfclient "github.com/jacaudi/cloudflare-operator/internal/cloudflare"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// mockZoneLifecycleClient implements cfclient.ZoneLifecycleClient for testing.
type mockZoneLifecycleClient struct {
	zones            map[string]*cfclient.Zone
	nextID           int
	createCalled     bool
	deleteCalled     bool
	editCalled       bool
	activationCalled bool
	createErr        error
	deleteErr        error
	editErr          error
	getErr           error
	listErr          error
}

func newMockZoneLifecycleClient() *mockZoneLifecycleClient {
	return &mockZoneLifecycleClient{zones: make(map[string]*cfclient.Zone)}
}

func (m *mockZoneLifecycleClient) CreateZone(_ context.Context, _ string, params cfclient.ZoneLifecycleParams) (*cfclient.Zone, error) {
	m.createCalled = true
	if m.createErr != nil {
		return nil, m.createErr
	}
	m.nextID++
	id := fmt.Sprintf("zone-%d", m.nextID)
	z := &cfclient.Zone{
		ID:          id,
		Name:        params.Name,
		Status:      "pending",
		Type:        params.Type,
		NameServers: []string{"ns1.cloudflare.com", "ns2.cloudflare.com"},
	}
	m.zones[id] = z
	return z, nil
}

func (m *mockZoneLifecycleClient) GetZone(_ context.Context, zoneID string) (*cfclient.Zone, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	z, ok := m.zones[zoneID]
	if !ok {
		return nil, fmt.Errorf("zone not found")
	}
	return z, nil
}

func (m *mockZoneLifecycleClient) ListZonesByName(_ context.Context, _, name string) ([]cfclient.Zone, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	var results []cfclient.Zone
	for _, z := range m.zones {
		if z.Name == name {
			results = append(results, *z)
		}
	}
	return results, nil
}

func (m *mockZoneLifecycleClient) EditZone(_ context.Context, zoneID string, params cfclient.ZoneLifecycleEditParams) (*cfclient.Zone, error) {
	m.editCalled = true
	if m.editErr != nil {
		return nil, m.editErr
	}
	z, ok := m.zones[zoneID]
	if !ok {
		return nil, fmt.Errorf("zone not found")
	}
	if params.Paused != nil {
		z.Paused = *params.Paused
	}
	return z, nil
}

func (m *mockZoneLifecycleClient) DeleteZone(_ context.Context, zoneID string) error {
	m.deleteCalled = true
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.zones, zoneID)
	return nil
}

func (m *mockZoneLifecycleClient) TriggerActivationCheck(_ context.Context, _ string) error {
	m.activationCalled = true
	return nil
}

func newTestCloudflareZone(name, namespace string) *cloudflarev1alpha1.CloudflareZone {
	return &cloudflarev1alpha1.CloudflareZone{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Generation: 1,
		},
		Spec: cloudflarev1alpha1.CloudflareZoneSpec{
			Name:           "example.com",
			AccountID:      "acct-123",
			Type:           "full",
			DeletionPolicy: "Retain",
			SecretRef: cloudflarev1alpha1.SecretReference{
				Name: "cf-secret",
			},
			Interval: &metav1.Duration{Duration: 30 * time.Minute},
		},
	}
}

func buildZoneReconciler(mock *mockZoneLifecycleClient, objs ...client.Object) *CloudflareZoneReconciler {
	s := testScheme(&testing.T{})

	var statusObjs []client.Object
	for _, o := range objs {
		if _, ok := o.(*cloudflarev1alpha1.CloudflareZone); ok {
			statusObjs = append(statusObjs, o)
		}
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(statusObjs...).
		Build()

	return &CloudflareZoneReconciler{
		Client:        fakeClient,
		Scheme:        s,
		Recorder:      record.NewFakeRecorder(10),
		ClientFactory: cfclient.NewClientFactory(fakeClient),
		ZoneLifecycleClientFn: func(_ string) cfclient.ZoneLifecycleClient {
			return mock
		},
	}
}

func TestZoneReconcile_AddsFinalizerOnFirstReconcile(t *testing.T) {
	zone := newTestCloudflareZone("test-zone", "default")
	secret := newTestSecret("default")
	mock := newMockZoneLifecycleClient()

	r := buildZoneReconciler(mock, zone, secret)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Requeue {
		t.Error("expected Requeue=true after adding finalizer")
	}

	var updated cloudflarev1alpha1.CloudflareZone
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "test-zone", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated zone: %v", err)
	}

	found := false
	for _, f := range updated.Finalizers {
		if f == cloudflarev1alpha1.FinalizerName {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected finalizer %q to be present", cloudflarev1alpha1.FinalizerName)
	}
}

func TestZoneReconcile_CreatesZone(t *testing.T) {
	zone := newTestCloudflareZone("test-zone", "default")
	zone.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	secret := newTestSecret("default")
	mock := newMockZoneLifecycleClient()

	r := buildZoneReconciler(mock, zone, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mock.createCalled {
		t.Error("expected CreateZone to be called")
	}

	var updated cloudflarev1alpha1.CloudflareZone
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "test-zone", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated zone: %v", err)
	}

	if updated.Status.ZoneID == "" {
		t.Error("expected ZoneID to be set in status")
	}
	if updated.Status.Status != "pending" {
		t.Errorf("expected status pending, got %s", updated.Status.Status)
	}
	if len(updated.Status.NameServers) != 2 {
		t.Errorf("expected 2 nameservers, got %d", len(updated.Status.NameServers))
	}
}

func TestZoneReconcile_AdoptsExistingZone(t *testing.T) {
	zone := newTestCloudflareZone("test-zone", "default")
	zone.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	secret := newTestSecret("default")

	mock := newMockZoneLifecycleClient()
	mock.zones["existing-zone-1"] = &cfclient.Zone{
		ID:          "existing-zone-1",
		Name:        "example.com",
		Status:      "active",
		Type:        "full",
		NameServers: []string{"ns1.cloudflare.com", "ns2.cloudflare.com"},
	}

	r := buildZoneReconciler(mock, zone, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mock.createCalled {
		t.Error("expected CreateZone NOT to be called when adopting")
	}

	var updated cloudflarev1alpha1.CloudflareZone
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "test-zone", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated zone: %v", err)
	}

	if updated.Status.ZoneID != "existing-zone-1" {
		t.Errorf("expected ZoneID=existing-zone-1, got %q", updated.Status.ZoneID)
	}
}

func TestZoneReconcile_SetsReadyTrueWhenActive(t *testing.T) {
	zone := newTestCloudflareZone("test-zone", "default")
	zone.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	zone.Status.ZoneID = "zone-active"
	secret := newTestSecret("default")

	mock := newMockZoneLifecycleClient()
	mock.zones["zone-active"] = &cfclient.Zone{
		ID:          "zone-active",
		Name:        "example.com",
		Status:      "active",
		Type:        "full",
		NameServers: []string{"ns1.cloudflare.com", "ns2.cloudflare.com"},
	}

	r := buildZoneReconciler(mock, zone, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated cloudflarev1alpha1.CloudflareZone
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "test-zone", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated zone: %v", err)
	}

	for _, c := range updated.Status.Conditions {
		if c.Type == "Ready" {
			if c.Status != metav1.ConditionTrue {
				t.Errorf("expected Ready=True, got %s", c.Status)
			}
			return
		}
	}
	t.Error("expected Ready condition to be set")
}

func TestZoneReconcile_SetsReadyFalseWhenPending(t *testing.T) {
	zone := newTestCloudflareZone("test-zone", "default")
	zone.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	zone.Status.ZoneID = "zone-pending"
	secret := newTestSecret("default")

	mock := newMockZoneLifecycleClient()
	mock.zones["zone-pending"] = &cfclient.Zone{
		ID:          "zone-pending",
		Name:        "example.com",
		Status:      "pending",
		Type:        "full",
		NameServers: []string{"ns1.cloudflare.com", "ns2.cloudflare.com"},
	}

	r := buildZoneReconciler(mock, zone, secret)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should requeue with shorter interval when pending
	if result.RequeueAfter > 5*time.Minute {
		t.Errorf("expected RequeueAfter <= 5m when pending, got %v", result.RequeueAfter)
	}

	var updated cloudflarev1alpha1.CloudflareZone
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "test-zone", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated zone: %v", err)
	}

	for _, c := range updated.Status.Conditions {
		if c.Type == "Ready" {
			if c.Status != metav1.ConditionFalse {
				t.Errorf("expected Ready=False when pending, got %s", c.Status)
			}
			if c.Reason != cloudflarev1alpha1.ReasonZonePending {
				t.Errorf("expected reason=%s, got %s", cloudflarev1alpha1.ReasonZonePending, c.Reason)
			}
			if !strings.Contains(c.Message, "ns1.cloudflare.com") {
				t.Errorf("expected message to contain nameservers, got %q", c.Message)
			}
			return
		}
	}
	t.Error("expected Ready condition to be set")
}

func TestZoneReconcile_TriggersActivationCheckWhenPending(t *testing.T) {
	zone := newTestCloudflareZone("test-zone", "default")
	zone.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	zone.Status.ZoneID = "zone-pending"
	secret := newTestSecret("default")

	mock := newMockZoneLifecycleClient()
	mock.zones["zone-pending"] = &cfclient.Zone{
		ID:          "zone-pending",
		Name:        "example.com",
		Status:      "pending",
		Type:        "full",
		NameServers: []string{"ns1.cloudflare.com", "ns2.cloudflare.com"},
	}

	r := buildZoneReconciler(mock, zone, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mock.activationCalled {
		t.Error("expected TriggerActivationCheck to be called when zone is pending")
	}
}

func TestZoneReconcile_DeletesZoneWithDeletePolicy(t *testing.T) {
	zone := newTestCloudflareZone("test-zone", "default")
	zone.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	zone.Spec.DeletionPolicy = "Delete"
	zone.Status.ZoneID = "zone-delete"
	now := metav1.Now()
	zone.DeletionTimestamp = &now

	secret := newTestSecret("default")

	mock := newMockZoneLifecycleClient()
	mock.zones["zone-delete"] = &cfclient.Zone{
		ID:     "zone-delete",
		Name:   "example.com",
		Status: "active",
	}

	r := buildZoneReconciler(mock, zone, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mock.deleteCalled {
		t.Error("expected DeleteZone to be called with Delete policy")
	}
}

func TestZoneReconcile_RetainsZoneWithRetainPolicy(t *testing.T) {
	zone := newTestCloudflareZone("test-zone", "default")
	zone.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	zone.Spec.DeletionPolicy = "Retain"
	zone.Status.ZoneID = "zone-retain"
	now := metav1.Now()
	zone.DeletionTimestamp = &now

	secret := newTestSecret("default")

	mock := newMockZoneLifecycleClient()
	mock.zones["zone-retain"] = &cfclient.Zone{
		ID:     "zone-retain",
		Name:   "example.com",
		Status: "active",
	}

	r := buildZoneReconciler(mock, zone, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mock.deleteCalled {
		t.Error("expected DeleteZone NOT to be called with Retain policy")
	}
}

func TestZoneReconcile_SecretNotFound(t *testing.T) {
	zone := newTestCloudflareZone("test-zone", "default")
	zone.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	mock := newMockZoneLifecycleClient()

	r := buildZoneReconciler(mock, zone) // no secret

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.RequeueAfter != 30*time.Second {
		t.Errorf("expected RequeueAfter=30s, got %v", result.RequeueAfter)
	}

	var updated cloudflarev1alpha1.CloudflareZone
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "test-zone", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated zone: %v", err)
	}

	for _, c := range updated.Status.Conditions {
		if c.Type == "Ready" && c.Reason == cloudflarev1alpha1.ReasonSecretNotFound {
			return
		}
	}
	t.Error("expected Ready condition with SecretNotFound reason")
}

func TestZoneReconcile_EditsZoneWhenPausedChanges(t *testing.T) {
	paused := true
	zone := newTestCloudflareZone("test-zone", "default")
	zone.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	zone.Spec.Paused = &paused
	zone.Status.ZoneID = "zone-edit"
	secret := newTestSecret("default")

	mock := newMockZoneLifecycleClient()
	mock.zones["zone-edit"] = &cfclient.Zone{
		ID:          "zone-edit",
		Name:        "example.com",
		Status:      "active",
		Type:        "full",
		Paused:      false, // different from spec
		NameServers: []string{"ns1.cloudflare.com", "ns2.cloudflare.com"},
	}

	r := buildZoneReconciler(mock, zone, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mock.editCalled {
		t.Error("expected EditZone to be called when paused differs")
	}
}
