package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	cfclient "github.com/jacaudi/cloudflare-operator/internal/cloudflare"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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
			GeneratedSecretName: "tunnel-creds",
			SecretRef: cloudflarev1alpha1.SecretReference{
				Name: "cf-secret",
			},
			Interval: &metav1.Duration{Duration: 30 * time.Minute},
		},
	}
}

// buildTunnelReconciler creates a CloudflareTunnelReconciler wired to a fake client and mock tunnel client.
func buildTunnelReconciler(t *testing.T, mock *mockTunnelClient, objs ...client.Object) *CloudflareTunnelReconciler {
	t.Helper()
	s := testScheme(t)

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
		ClientFactory: cfclient.NewClientFactory(fakeClient, fakeClient),
		TunnelClientFn: func(_ string) cfclient.TunnelClient {
			return mock
		},
	}
}

func TestTunnelReconcile_AddsFinalizerOnFirstReconcile(t *testing.T) {
	tunnel := newTestTunnel("test-tunnel", "default")
	secret := newTestSecret("default")
	mock := newMockTunnelClient()

	r := buildTunnelReconciler(t, mock, tunnel, secret)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-tunnel", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should requeue after adding finalizer
	if result.RequeueAfter == 0 {
		t.Error("expected requeue after adding finalizer")
	}

	// Verify finalizer was added
	var updated cloudflarev1alpha1.CloudflareTunnel
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-tunnel", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated tunnel: %v", err)
	}

	found := slices.Contains(updated.Finalizers, cloudflarev1alpha1.FinalizerName)
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

	r := buildTunnelReconciler(t, mock, tunnel, secret)

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
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-tunnel", Namespace: "default"}, &updated); err != nil {
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
	if err := r.Get(context.Background(), types.NamespacedName{Name: "tunnel-creds", Namespace: "default"}, &credSecret); err != nil {
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

	r := buildTunnelReconciler(t, mock, tunnel, secret)

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
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-tunnel", Namespace: "default"}, &updated); err != nil {
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
	if err := r.Get(context.Background(), types.NamespacedName{Name: "tunnel-creds", Namespace: "default"}, &credSecret); err != nil {
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

	r := buildTunnelReconciler(t, mock, tunnel, secret)

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
	err = r.Get(context.Background(), types.NamespacedName{Name: "test-tunnel", Namespace: "default"}, &updated)
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

	r := buildTunnelReconciler(t, mock, tunnel)

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
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-tunnel", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated tunnel: %v", err)
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

// buildInterceptedTunnelReconciler is the same as buildTunnelReconciler
// but wraps the fake client with the given interceptor.Funcs so individual
// API calls can be intercepted (e.g. to inject conflict storms).
func buildInterceptedTunnelReconciler(t *testing.T, mock *mockTunnelClient, funcs interceptor.Funcs, objs ...client.Object) *CloudflareTunnelReconciler {
	t.Helper()
	s := testScheme(t)

	var statusObjs []client.Object
	for _, o := range objs {
		if _, ok := o.(*cloudflarev1alpha1.CloudflareTunnel); ok {
			statusObjs = append(statusObjs, o)
		}
	}

	base := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(statusObjs...).
		Build()
	wrapped := interceptor.NewClient(base, funcs)

	return &CloudflareTunnelReconciler{
		Client:         wrapped,
		Scheme:         s,
		Recorder:       record.NewFakeRecorder(10),
		ClientFactory:  cfclient.NewClientFactory(wrapped, wrapped),
		TunnelClientFn: func(_ string) cfclient.TunnelClient { return mock },
	}
}

// TestReconcile_ConnectorConflictStormDoesNotInflateRequeue is the
// regression test for #59. When the connector Deployment Update path
// returns transient IsConflict errors, Reconcile must NOT propagate the
// error / set the 30s failReconcile RequeueAfter — that was the
// workqueue-backoff inflation source that wedged finalizer cleanup.
func TestReconcile_ConnectorConflictStormDoesNotInflateRequeue(t *testing.T) {
	tun := newTestTunnel("test-tunnel", "default")
	// Adopt an existing tunnel rather than creating one: pre-populate Status
	// so the connector reconcile is reached and uses a deterministic ID.
	tun.Spec.Connector = &cloudflarev1alpha1.ConnectorSpec{Enabled: true, Replicas: 2}
	// Add the finalizer so the reconcile takes the standard path, not the add-finalizer path.
	tun.Finalizers = append(tun.Finalizers, cloudflarev1alpha1.FinalizerName)
	tun.Status.TunnelID = "test-tunnel-id"
	tun.Status.TunnelCNAME = "test-tunnel-id.cfargotunnel.com"

	// Pre-create the connector Deployment so reconcile takes the update path
	// (where the conflict manifests).
	ndep := ConnectorNames(tun)
	existingDep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            ndep.Deployment,
			Namespace:       tun.Namespace,
			OwnerReferences: connectorOwnerRef(tun),
		},
	}
	secret := newTestSecret("default")

	mock := newMockTunnelClient()
	// Seed the mock so GetTunnel returns the existing tunnel and the reconciler
	// goes down the adoption path rather than CreateTunnel.
	mock.tunnels[tun.Status.TunnelID] = &cfclient.Tunnel{
		ID:   tun.Status.TunnelID,
		Name: tun.Spec.Name,
	}

	calls := 0
	funcs := interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*appsv1.Deployment); ok {
				calls++
				if calls <= 3 {
					return apierrors.NewConflict(
						schema.GroupResource{Group: "apps", Resource: "deployments"},
						obj.GetName(),
						fmt.Errorf("simulated conflict %d", calls),
					)
				}
			}
			return c.Update(ctx, obj, opts...)
		},
	}

	r := buildInterceptedTunnelReconciler(t, mock, funcs, tun, secret, existingDep)

	res, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: tun.Namespace, Name: tun.Name},
	})
	// failReconcile returns (Result, nil), so err is unhelpful as a regression
	// discriminator on its own — RequeueAfter and the persisted Ready condition
	// (asserted below) are the load-bearing checks.
	if err != nil {
		t.Fatalf("Reconcile returned unexpected error: %v", err)
	}
	// failReconcile path uses 30s; success path uses spec.Interval (30 min in newTestTunnel).
	// Anything ≤30s indicates the conflict propagated as a reconcile error and inflated the requeue.
	if res.RequeueAfter > 0 && res.RequeueAfter <= 30*time.Second {
		t.Errorf("RequeueAfter = %s; expected success-path requeue (>30s), not failReconcile (≤30s)", res.RequeueAfter)
	}
	if calls < 4 {
		t.Errorf("expected ≥4 Deployment Update attempts (3 conflicts + 1 success), got %d", calls)
	}

	// Persisted-status discriminator: on the success path, writeTunnelAggStatus
	// runs and persists Ready=True + IngressConfigured=True. On the pre-fix path
	// the connector branch returns early with RequeueAfter=30s before
	// writeTunnelAggStatus runs, so these conditions are absent / not True.
	var got cloudflarev1alpha1.CloudflareTunnel
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: tun.Namespace, Name: tun.Name}, &got); err != nil {
		t.Fatalf("Get tunnel after Reconcile: %v", err)
	}
	ready := meta.FindStatusCondition(got.Status.Conditions, cloudflarev1alpha1.ConditionTypeReady)
	if ready == nil || ready.Status != metav1.ConditionTrue {
		t.Errorf("expected Ready=True after Reconcile (success path); got %+v", ready)
	}
	ingress := meta.FindStatusCondition(got.Status.Conditions, cloudflarev1alpha1.ConditionTypeIngressConfigured)
	if ingress == nil || ingress.Status != metav1.ConditionTrue {
		t.Errorf("expected IngressConfigured=True after Reconcile; got %+v", ingress)
	}
}

// TestCloudflareTunnelReconciler_TunnelGoneClearsStatus exercises the
// post-reconcileTunnel failReconcile site with a Cloudflare 404.
//
// reconcileTunnel swallows GetTunnel errors (logs + falls through); the 404
// must therefore come from ListTunnelsByName. We trigger the fallthrough by
// injecting a generic getErr, then let the 404 listErr propagate to the outer
// failReconcile where the classifier fires.
//
// The test verifies that ClassifyCloudflareError sees the 404, sets
// ResetRemoteID=true, and the reconciler clears BOTH Status.TunnelID AND
// Status.TunnelCNAME before persisting.
func TestCloudflareTunnelReconciler_TunnelGoneClearsStatus(t *testing.T) {
	tunnel := &cloudflarev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "tun",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cloudflarev1alpha1.FinalizerName},
		},
		Spec: cloudflarev1alpha1.CloudflareTunnelSpec{
			Name:      "my-tunnel",
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "creds"},
		},
		Status: cloudflarev1alpha1.CloudflareTunnelStatus{
			TunnelID:    "tun-123",
			TunnelCNAME: "tun-123.cfargotunnel.com",
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "default"},
		Data: map[string][]byte{
			cfclient.SecretKeyAPIToken:  []byte("token"),
			cfclient.SecretKeyAccountID: []byte("acct"),
		},
	}

	mock := newMockTunnelClient()
	// getErr triggers the in-flow fallthrough (reconcileTunnel logs + continues).
	// listErr 404 then propagates to Reconcile's failReconcile site.
	mock.getErr = errors.New("transient fetch error")
	mock.listErr = &cfgo.Error{StatusCode: http.StatusNotFound}

	r := buildTunnelReconciler(t, mock, tunnel, secret)
	res, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(tunnel),
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %v, want 0 (immediate requeue for RemoteGone)", res.RequeueAfter)
	}

	updated := &cloudflarev1alpha1.CloudflareTunnel{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(tunnel), updated); err != nil {
		t.Fatalf("get updated tunnel: %v", err)
	}
	if updated.Status.TunnelID != "" {
		t.Errorf("Status.TunnelID not cleared: %q", updated.Status.TunnelID)
	}
	if updated.Status.TunnelCNAME != "" {
		t.Errorf("Status.TunnelCNAME not cleared: %q", updated.Status.TunnelCNAME)
	}
	cond := meta.FindStatusCondition(updated.Status.Conditions, cloudflarev1alpha1.ConditionTypeReady)
	if cond == nil {
		t.Fatal("Ready condition missing")
	}
	if cond.Reason != cloudflarev1alpha1.ReasonRemoteGone {
		t.Errorf("Reason = %q, want %q", cond.Reason, cloudflarev1alpha1.ReasonRemoteGone)
	}
}

// TestCloudflareTunnelReconciler_BadRequest_EmitsInvalidSpecEvent asserts that a
// 400 Bad Request from the Cloudflare API emits an "InvalidSpec" recorder event,
// NOT the legacy "SyncFailed" event. A future regression that reverts to hardcoded
// "SyncFailed" for classified errors will be caught here.
//
// We inject the 400 via ListTunnelsByName (mock.listErr) which is reached on the
// reconcileTunnel path when no TunnelID is stored in Status.
func TestCloudflareTunnelReconciler_BadRequest_EmitsInvalidSpecEvent(t *testing.T) {
	tunnel := &cloudflarev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "tun",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cloudflarev1alpha1.FinalizerName},
		},
		Spec: cloudflarev1alpha1.CloudflareTunnelSpec{
			Name:      "my-tunnel",
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "creds"},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "default"},
		Data: map[string][]byte{
			cfclient.SecretKeyAPIToken:  []byte("token"),
			cfclient.SecretKeyAccountID: []byte("acct"),
		},
	}

	mock := newMockTunnelClient()
	mock.listErr = &cfgo.Error{StatusCode: http.StatusBadRequest}

	s := testScheme(t)
	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(tunnel, secret).
		WithStatusSubresource(tunnel).
		Build()
	fakeRec := record.NewFakeRecorder(10)
	r := &CloudflareTunnelReconciler{
		Client:        fakeClient,
		Scheme:        s,
		Recorder:      fakeRec,
		ClientFactory: cfclient.NewClientFactory(fakeClient, fakeClient),
		TunnelClientFn: func(_ string) cfclient.TunnelClient {
			return mock
		},
	}

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(tunnel),
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
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

// TestCloudflareTunnelReconciler_DeleteTunnelNotFound_RemovesFinalizer mirrors
// TestZoneReconcile_DeleteZoneNotFound_RemovesFinalizer. When DeleteTunnel
// returns 404 the operator must treat it as success (the remote object is gone,
// which is the goal), remove the finalizer, and return with RequeueAfter == 0.
func TestCloudflareTunnelReconciler_DeleteTunnelNotFound_RemovesFinalizer(t *testing.T) {
	now := metav1.Now()
	tunnel := &cloudflarev1alpha1.CloudflareTunnel{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "tun",
			Namespace:         "default",
			Generation:        1,
			Finalizers:        []string{cloudflarev1alpha1.FinalizerName},
			DeletionTimestamp: &now,
		},
		Spec: cloudflarev1alpha1.CloudflareTunnelSpec{
			Name:      "my-tunnel",
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "creds"},
		},
		Status: cloudflarev1alpha1.CloudflareTunnelStatus{
			TunnelID: "tun-already-gone",
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "default"},
		Data: map[string][]byte{
			cfclient.SecretKeyAPIToken:  []byte("token"),
			cfclient.SecretKeyAccountID: []byte("acct"),
		},
	}

	mock := newMockTunnelClient()
	mock.deleteErr = &cfgo.Error{StatusCode: http.StatusNotFound}

	r := buildTunnelReconciler(t, mock, tunnel, secret)
	res, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(tunnel),
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %v, want 0 (delete-404 should be success-equivalent)", res.RequeueAfter)
	}

	got := &cloudflarev1alpha1.CloudflareTunnel{}
	getErr := r.Get(context.Background(), client.ObjectKeyFromObject(tunnel), got)
	if getErr == nil {
		if len(got.Finalizers) != 0 {
			t.Errorf("Finalizer still present: %v; expected removal", got.Finalizers)
		}
	} else if !apierrors.IsNotFound(getErr) {
		t.Fatalf("unexpected error: %v", getErr)
	}
}
