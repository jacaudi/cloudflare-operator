package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	cfclient "github.com/jacaudi/cloudflare-operator/internal/cloudflare"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// mockTunnelClient implements cfclient.TunnelClient for testing.
type mockTunnelClient struct {
	tunnels      map[string]*cfclient.Tunnel
	nextID       int
	createCalled bool
	deleteCalled bool
	createErr    error
	deleteErr    error
	listErr      error
	getErr       error
}

func newMockTunnelClient() *mockTunnelClient {
	return &mockTunnelClient{tunnels: make(map[string]*cfclient.Tunnel)}
}

func (m *mockTunnelClient) GetTunnel(_ context.Context, _, tunnelID string) (*cfclient.Tunnel, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	t, ok := m.tunnels[tunnelID]
	if !ok {
		return nil, fmt.Errorf("tunnel not found")
	}
	return t, nil
}

func (m *mockTunnelClient) ListTunnelsByName(_ context.Context, _, name string) ([]cfclient.Tunnel, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	var results []cfclient.Tunnel
	for _, t := range m.tunnels {
		if t.Name == name {
			results = append(results, *t)
		}
	}
	return results, nil
}

func (m *mockTunnelClient) CreateTunnel(_ context.Context, _ string, params cfclient.TunnelParams) (*cfclient.Tunnel, error) {
	m.createCalled = true
	if m.createErr != nil {
		return nil, m.createErr
	}
	m.nextID++
	id := fmt.Sprintf("tunnel-%d", m.nextID)
	t := &cfclient.Tunnel{
		ID:   id,
		Name: params.Name,
	}
	m.tunnels[id] = t
	return t, nil
}

func (m *mockTunnelClient) DeleteTunnel(_ context.Context, _, tunnelID string) error {
	m.deleteCalled = true
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.tunnels, tunnelID)
	return nil
}

// Helper to create a base CloudflareTunnel for tests.
func newTestTunnel(name, namespace string) *cloudflarev1alpha1.CloudflareTunnel {
	return &cloudflarev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Generation: 1,
		},
		Spec: cloudflarev1alpha1.CloudflareTunnelSpec{
			Name:                "my-tunnel",
			AccountID:           "acct-123",
			GeneratedSecretName: "tunnel-creds",
			SecretRef: cloudflarev1alpha1.SecretReference{
				Name: "cf-secret",
			},
			Interval: &metav1.Duration{Duration: 30 * time.Minute},
		},
	}
}

// buildTunnelReconciler creates a CloudflareTunnelReconciler wired to a fake client and mock tunnel client.
func buildTunnelReconciler(mock *mockTunnelClient, objs ...client.Object) *CloudflareTunnelReconciler {
	s := testScheme(&testing.T{})

	// Collect CRD objects for status subresource registration
	var statusObjs []client.Object
	for _, o := range objs {
		if _, ok := o.(*cloudflarev1alpha1.CloudflareTunnel); ok {
			statusObjs = append(statusObjs, o)
		}
	}

	builder := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(statusObjs...)

	fakeClient := builder.Build()

	return &CloudflareTunnelReconciler{
		Client:        fakeClient,
		Scheme:        s,
		Recorder:      record.NewFakeRecorder(10),
		ClientFactory: cfclient.NewClientFactory(fakeClient),
		TunnelClientFn: func(_ string) cfclient.TunnelClient {
			return mock
		},
	}
}

func TestTunnelReconcile_AddsFinalizerOnFirstReconcile(t *testing.T) {
	tunnel := newTestTunnel("test-tunnel", "default")
	secret := newTestSecret("default")
	mock := newMockTunnelClient()

	r := buildTunnelReconciler(mock, tunnel, secret)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-tunnel", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should requeue after adding finalizer
	if !result.Requeue {
		t.Error("expected Requeue=true after adding finalizer")
	}

	// Verify finalizer was added
	var updated cloudflarev1alpha1.CloudflareTunnel
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "test-tunnel", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated tunnel: %v", err)
	}

	found := false
	for _, f := range updated.Finalizers {
		if f == cloudflarev1alpha1.FinalizerName {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected finalizer %q to be present, got finalizers: %v", cloudflarev1alpha1.FinalizerName, updated.Finalizers)
	}
}

func TestTunnelReconcile_CreatesTunnel(t *testing.T) {
	tunnel := newTestTunnel("test-tunnel", "default")
	// Pre-add the finalizer so we proceed to the create path
	tunnel.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	secret := newTestSecret("default")
	mock := newMockTunnelClient()

	r := buildTunnelReconciler(mock, tunnel, secret)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-tunnel", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should requeue after interval
	if result.RequeueAfter != 30*time.Minute {
		t.Errorf("expected RequeueAfter=30m, got %v", result.RequeueAfter)
	}

	// Verify the mock was called
	if !mock.createCalled {
		t.Error("expected CreateTunnel to be called")
	}

	// Verify status was updated
	var updated cloudflarev1alpha1.CloudflareTunnel
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "test-tunnel", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated tunnel: %v", err)
	}

	if updated.Status.TunnelID == "" {
		t.Error("expected TunnelID to be set in status")
	}
	expectedCNAME := fmt.Sprintf("%s.cfargotunnel.com", updated.Status.TunnelID)
	if updated.Status.TunnelCNAME != expectedCNAME {
		t.Errorf("expected TunnelCNAME=%q, got %q", expectedCNAME, updated.Status.TunnelCNAME)
	}
	if updated.Status.CredentialsSecretName != "tunnel-creds" {
		t.Errorf("expected CredentialsSecretName=tunnel-creds, got %q", updated.Status.CredentialsSecretName)
	}

	// Verify the credentials Secret was created
	var credSecret corev1.Secret
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "tunnel-creds", Namespace: "default"}, &credSecret); err != nil {
		t.Fatalf("expected credentials secret to be created: %v", err)
	}

	credsData, ok := credSecret.Data["credentials.json"]
	if !ok {
		t.Fatal("expected credentials.json key in secret data")
	}

	var creds map[string]string
	if err := json.Unmarshal(credsData, &creds); err != nil {
		t.Fatalf("failed to unmarshal credentials.json: %v", err)
	}
	if creds["AccountTag"] != "acct-123" {
		t.Errorf("expected AccountTag=acct-123, got %q", creds["AccountTag"])
	}
	if creds["TunnelID"] != updated.Status.TunnelID {
		t.Errorf("expected TunnelID=%s, got %q", updated.Status.TunnelID, creds["TunnelID"])
	}
	if creds["TunnelSecret"] == "" {
		t.Error("expected TunnelSecret to be non-empty")
	}

	// Verify owner reference is set on credentials Secret
	if len(credSecret.OwnerReferences) == 0 {
		t.Error("expected owner reference to be set on credentials secret")
	}
}

func TestTunnelReconcile_AdoptsExistingTunnel(t *testing.T) {
	tunnel := newTestTunnel("test-tunnel", "default")
	tunnel.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	secret := newTestSecret("default")

	mock := newMockTunnelClient()
	// Pre-populate an existing tunnel in Cloudflare that matches by name
	mock.tunnels["existing-tunnel-456"] = &cfclient.Tunnel{
		ID:   "existing-tunnel-456",
		Name: "my-tunnel",
	}

	r := buildTunnelReconciler(mock, tunnel, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-tunnel", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should NOT have created a new tunnel
	if mock.createCalled {
		t.Error("expected CreateTunnel NOT to be called when adopting")
	}

	// Verify the status has the adopted tunnel ID
	var updated cloudflarev1alpha1.CloudflareTunnel
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "test-tunnel", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated tunnel: %v", err)
	}

	if updated.Status.TunnelID != "existing-tunnel-456" {
		t.Errorf("expected TunnelID=existing-tunnel-456, got %q", updated.Status.TunnelID)
	}

	expectedCNAME := "existing-tunnel-456.cfargotunnel.com"
	if updated.Status.TunnelCNAME != expectedCNAME {
		t.Errorf("expected TunnelCNAME=%q, got %q", expectedCNAME, updated.Status.TunnelCNAME)
	}

	// Verify the credentials Secret was created even for adopted tunnels
	var credSecret corev1.Secret
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "tunnel-creds", Namespace: "default"}, &credSecret); err != nil {
		t.Fatalf("expected credentials secret to be created for adopted tunnel: %v", err)
	}
}

func TestTunnelReconcile_DeletesTunnel(t *testing.T) {
	tunnel := newTestTunnel("test-tunnel", "default")
	tunnel.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	tunnel.Status.TunnelID = "tunnel-delete"
	now := metav1.Now()
	tunnel.DeletionTimestamp = &now

	secret := newTestSecret("default")

	mock := newMockTunnelClient()
	mock.tunnels["tunnel-delete"] = &cfclient.Tunnel{
		ID:   "tunnel-delete",
		Name: "my-tunnel",
	}

	r := buildTunnelReconciler(mock, tunnel, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-tunnel", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify delete was called on the mock
	if !mock.deleteCalled {
		t.Error("expected DeleteTunnel to be called")
	}

	// Verify the tunnel was removed from the mock
	if _, exists := mock.tunnels["tunnel-delete"]; exists {
		t.Error("expected tunnel to be removed from mock after deletion")
	}

	// The fake client with DeletionTimestamp set will garbage-collect the object
	// once the finalizer is removed, so we verify the object is gone (which proves
	// the finalizer was successfully removed).
	var updated cloudflarev1alpha1.CloudflareTunnel
	err = r.Client.Get(context.Background(), types.NamespacedName{Name: "test-tunnel", Namespace: "default"}, &updated)
	if err == nil {
		// Object still exists - verify finalizer was removed
		for _, f := range updated.Finalizers {
			if f == cloudflarev1alpha1.FinalizerName {
				t.Error("expected finalizer to be removed after deletion")
			}
		}
	}
	// If err is not-found, the object was garbage-collected after finalizer removal - that's correct
}

func TestTunnelReconcile_SecretNotFound(t *testing.T) {
	tunnel := newTestTunnel("test-tunnel", "default")
	tunnel.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	// No secret created - should fail to get API token
	mock := newMockTunnelClient()

	r := buildTunnelReconciler(mock, tunnel)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-tunnel", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error (should be handled gracefully): %v", err)
	}

	// Should requeue after 30s
	if result.RequeueAfter != 30*time.Second {
		t.Errorf("expected RequeueAfter=30s, got %v", result.RequeueAfter)
	}

	// Verify Ready condition is False with SecretNotFound reason
	var updated cloudflarev1alpha1.CloudflareTunnel
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "test-tunnel", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated tunnel: %v", err)
	}

	foundCondition := false
	for _, c := range updated.Status.Conditions {
		if c.Type == "Ready" {
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
