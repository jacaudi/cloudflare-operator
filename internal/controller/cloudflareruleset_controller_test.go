package controller

import (
	"context"
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

// mockRulesetClient implements cfclient.RulesetClient for testing.
type mockRulesetClient struct {
	rulesets     map[string]*cfclient.Ruleset
	nextID       int
	createCalled bool
	updateCalled bool
	deleteCalled bool
	createErr    error
	updateErr    error
	deleteErr    error
	listErr      error
	getErr       error
}

func newMockRulesetClient() *mockRulesetClient {
	return &mockRulesetClient{rulesets: make(map[string]*cfclient.Ruleset)}
}

func (m *mockRulesetClient) GetRuleset(_ context.Context, _, rulesetID string) (*cfclient.Ruleset, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	rs, ok := m.rulesets[rulesetID]
	if !ok {
		return nil, fmt.Errorf("ruleset not found")
	}
	return rs, nil
}

func (m *mockRulesetClient) ListRulesetsByPhase(_ context.Context, _, phase string) ([]cfclient.Ruleset, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	var results []cfclient.Ruleset
	for _, rs := range m.rulesets {
		if rs.Phase == phase {
			results = append(results, *rs)
		}
	}
	return results, nil
}

func (m *mockRulesetClient) CreateRuleset(_ context.Context, _ string, params cfclient.RulesetParams) (*cfclient.Ruleset, error) {
	m.createCalled = true
	if m.createErr != nil {
		return nil, m.createErr
	}
	m.nextID++
	id := fmt.Sprintf("ruleset-%d", m.nextID)
	rs := &cfclient.Ruleset{
		ID:    id,
		Name:  params.Name,
		Phase: params.Phase,
		Rules: params.Rules,
	}
	m.rulesets[id] = rs
	return rs, nil
}

func (m *mockRulesetClient) UpdateRuleset(_ context.Context, _, rulesetID string, params cfclient.RulesetParams) (*cfclient.Ruleset, error) {
	m.updateCalled = true
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	rs, ok := m.rulesets[rulesetID]
	if !ok {
		return nil, fmt.Errorf("ruleset not found")
	}
	rs.Name = params.Name
	rs.Rules = params.Rules
	return rs, nil
}

func (m *mockRulesetClient) DeleteRuleset(_ context.Context, _, rulesetID string) error {
	m.deleteCalled = true
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.rulesets, rulesetID)
	return nil
}

// Helper to create a base CloudflareRuleset for tests.
func newTestRuleset(name, namespace string) *cloudflarev1alpha1.CloudflareRuleset {
	enabled := true
	return &cloudflarev1alpha1.CloudflareRuleset{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Generation: 1,
		},
		Spec: cloudflarev1alpha1.CloudflareRulesetSpec{
			ZoneID:      "zone-123",
			Name:        "test-waf",
			Description: "Test WAF rules",
			Phase:       "http_request_firewall_custom",
			SecretRef: cloudflarev1alpha1.SecretReference{
				Name: "cf-secret",
			},
			Interval: &metav1.Duration{Duration: 30 * time.Minute},
			Rules: []cloudflarev1alpha1.RulesetRuleSpec{
				{
					Action:      "block",
					Expression:  `(cf.client.bot) or (cf.threat_score gt 14)`,
					Description: "Block bots and threats",
					Enabled:     &enabled,
				},
			},
		},
	}
}

// Helper to create the Cloudflare API token secret for ruleset tests.
func newTestRulesetSecret(namespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cf-secret",
			Namespace: namespace,
		},
		Data: map[string][]byte{
			"apiToken": []byte("test-token"),
		},
	}
}

// buildRulesetReconciler creates a CloudflareRulesetReconciler wired to a fake client and mock ruleset client.
func buildRulesetReconciler(mock *mockRulesetClient, objs ...client.Object) *CloudflareRulesetReconciler {
	s := testScheme(&testing.T{})

	// Collect CRD objects for status subresource registration
	var statusObjs []client.Object
	for _, o := range objs {
		if _, ok := o.(*cloudflarev1alpha1.CloudflareRuleset); ok {
			statusObjs = append(statusObjs, o)
		}
	}

	builder := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(statusObjs...)

	fakeClient := builder.Build()

	return &CloudflareRulesetReconciler{
		Client:        fakeClient,
		Scheme:        s,
		Recorder:      record.NewFakeRecorder(10),
		ClientFactory: cfclient.NewClientFactory(fakeClient),
		RulesetClientFn: func(_ string) cfclient.RulesetClient {
			return mock
		},
	}
}

func TestRulesetReconcile_CreatesRuleset(t *testing.T) {
	ruleset := newTestRuleset("test-ruleset", "default")
	// Pre-add the finalizer so we proceed to the create path
	ruleset.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	secret := newTestRulesetSecret("default")
	mock := newMockRulesetClient()

	r := buildRulesetReconciler(mock, ruleset, secret)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-ruleset", Namespace: "default"},
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
		t.Error("expected CreateRuleset to be called")
	}

	// Verify status was updated
	var updated cloudflarev1alpha1.CloudflareRuleset
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "test-ruleset", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated ruleset: %v", err)
	}

	if updated.Status.RulesetID == "" {
		t.Error("expected RulesetID to be set in status")
	}
	if updated.Status.RuleCount != 1 {
		t.Errorf("expected RuleCount=1, got %d", updated.Status.RuleCount)
	}
}

func TestRulesetReconcile_AdoptsExistingRuleset(t *testing.T) {
	ruleset := newTestRuleset("test-ruleset", "default")
	ruleset.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	// No RulesetID in status — should adopt
	secret := newTestRulesetSecret("default")

	mock := newMockRulesetClient()
	// Pre-populate an existing ruleset in Cloudflare that matches by phase
	mock.rulesets["existing-rs-123"] = &cfclient.Ruleset{
		ID:    "existing-rs-123",
		Name:  "existing-waf",
		Phase: "http_request_firewall_custom",
		Rules: []cfclient.RulesetRule{
			{
				ID:          "rule-1",
				Action:      "block",
				Expression:  `(cf.client.bot)`,
				Description: "Old rule",
				Enabled:     true,
			},
		},
	}

	r := buildRulesetReconciler(mock, ruleset, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-ruleset", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should NOT have created a new ruleset
	if mock.createCalled {
		t.Error("expected CreateRuleset NOT to be called when adopting")
	}

	// Should have updated the adopted ruleset with desired rules
	if !mock.updateCalled {
		t.Error("expected UpdateRuleset to be called for adopted ruleset")
	}

	// Verify the status has the adopted ruleset ID
	var updated cloudflarev1alpha1.CloudflareRuleset
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "test-ruleset", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated ruleset: %v", err)
	}

	if updated.Status.RulesetID != "existing-rs-123" {
		t.Errorf("expected RulesetID=existing-rs-123, got %q", updated.Status.RulesetID)
	}
}

func TestRulesetReconcile_UpdatesRuleset(t *testing.T) {
	ruleset := newTestRuleset("test-ruleset", "default")
	ruleset.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	ruleset.Status.RulesetID = "rs-existing"
	secret := newTestRulesetSecret("default")

	mock := newMockRulesetClient()
	// Existing ruleset with different rules
	mock.rulesets["rs-existing"] = &cfclient.Ruleset{
		ID:    "rs-existing",
		Name:  "test-waf",
		Phase: "http_request_firewall_custom",
		Rules: []cfclient.RulesetRule{
			{
				ID:          "rule-old",
				Action:      "log",
				Expression:  `(cf.client.bot)`,
				Description: "Old rule",
				Enabled:     true,
			},
		},
	}

	r := buildRulesetReconciler(mock, ruleset, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-ruleset", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !mock.updateCalled {
		t.Error("expected UpdateRuleset to be called for existing ruleset")
	}

	// Verify status
	var updated cloudflarev1alpha1.CloudflareRuleset
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "test-ruleset", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated ruleset: %v", err)
	}

	if updated.Status.RulesetID != "rs-existing" {
		t.Errorf("expected RulesetID=rs-existing, got %q", updated.Status.RulesetID)
	}
	if updated.Status.RuleCount != 1 {
		t.Errorf("expected RuleCount=1, got %d", updated.Status.RuleCount)
	}
}

func TestRulesetReconcile_DeletesRuleset(t *testing.T) {
	ruleset := newTestRuleset("test-ruleset", "default")
	ruleset.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	ruleset.Status.RulesetID = "rs-delete"
	now := metav1.Now()
	ruleset.DeletionTimestamp = &now

	secret := newTestRulesetSecret("default")

	mock := newMockRulesetClient()
	mock.rulesets["rs-delete"] = &cfclient.Ruleset{
		ID:    "rs-delete",
		Name:  "test-waf",
		Phase: "http_request_firewall_custom",
	}

	r := buildRulesetReconciler(mock, ruleset, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-ruleset", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify delete was called on the mock
	if !mock.deleteCalled {
		t.Error("expected DeleteRuleset to be called")
	}

	// Verify the ruleset was removed from the mock
	if _, exists := mock.rulesets["rs-delete"]; exists {
		t.Error("expected ruleset to be removed from mock after deletion")
	}

	// The fake client with DeletionTimestamp set will garbage-collect the object
	// once the finalizer is removed, so we verify the object is gone (which proves
	// the finalizer was successfully removed).
	var updated cloudflarev1alpha1.CloudflareRuleset
	err = r.Client.Get(context.Background(), types.NamespacedName{Name: "test-ruleset", Namespace: "default"}, &updated)
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

func TestRulesetReconcile_SecretNotFound(t *testing.T) {
	ruleset := newTestRuleset("test-ruleset", "default")
	ruleset.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	// No secret created — should fail to get API token
	mock := newMockRulesetClient()

	r := buildRulesetReconciler(mock, ruleset)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-ruleset", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error (should be handled gracefully): %v", err)
	}

	// Should requeue after 30s
	if result.RequeueAfter != 30*time.Second {
		t.Errorf("expected RequeueAfter=30s, got %v", result.RequeueAfter)
	}

	// Verify Ready condition is False with SecretNotFound reason
	var updated cloudflarev1alpha1.CloudflareRuleset
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "test-ruleset", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated ruleset: %v", err)
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
