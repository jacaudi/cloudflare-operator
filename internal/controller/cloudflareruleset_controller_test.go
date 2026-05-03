package controller

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	cfgo "github.com/cloudflare/cloudflare-go/v6"
	cloudflarev1alpha1 "github.com/jacaudi/cloudflare-operator/api/v1alpha1"
	cfclient "github.com/jacaudi/cloudflare-operator/internal/cloudflare"

	corev1 "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// mockRulesetClient implements cfclient.RulesetClient for testing.
//
// Stores one ruleset per (zoneID, phase) tuple — matching Cloudflare's
// single-entrypoint-per-phase semantics.
type mockRulesetClient struct {
	entrypoints  map[string]*cfclient.Ruleset // key: zoneID + "/" + phase
	nextID       int
	upsertCalled bool
	lastZoneID   string
	upsertErr    error
	getErr       error
	// When true, GetPhaseEntrypoint returns ErrPhaseEntrypointNotFound
	// regardless of whether a ruleset is in the store.
	forceNotFound bool
}

func newMockRulesetClient() *mockRulesetClient {
	return &mockRulesetClient{entrypoints: make(map[string]*cfclient.Ruleset)}
}

func entrypointKey(zoneID, phase string) string { return zoneID + "/" + phase }

func (m *mockRulesetClient) GetPhaseEntrypoint(_ context.Context, zoneID, phase string) (*cfclient.Ruleset, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	if m.forceNotFound {
		return nil, fmt.Errorf("%w: phase %s in zone %s", cfclient.ErrPhaseEntrypointNotFound, phase, zoneID)
	}
	rs, ok := m.entrypoints[entrypointKey(zoneID, phase)]
	if !ok {
		return nil, fmt.Errorf("%w: phase %s in zone %s", cfclient.ErrPhaseEntrypointNotFound, phase, zoneID)
	}
	return rs, nil
}

func (m *mockRulesetClient) UpsertPhaseEntrypoint(_ context.Context, zoneID, phase string, params cfclient.RulesetParams) (*cfclient.Ruleset, error) {
	m.upsertCalled = true
	m.lastZoneID = zoneID
	if m.upsertErr != nil {
		return nil, m.upsertErr
	}
	key := entrypointKey(zoneID, phase)
	existing, ok := m.entrypoints[key]
	if !ok {
		m.nextID++
		existing = &cfclient.Ruleset{
			ID:    fmt.Sprintf("ruleset-%d", m.nextID),
			Phase: phase,
		}
		m.entrypoints[key] = existing
	}
	existing.Name = params.Name
	existing.Description = params.Description
	existing.Rules = params.Rules
	m.forceNotFound = false
	return existing, nil
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
		switch o.(type) {
		case *cloudflarev1alpha1.CloudflareRuleset, *cloudflarev1alpha1.CloudflareZone:
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
	if !mock.upsertCalled {
		t.Error("expected UpsertPhaseEntrypoint to be called")
	}

	// Verify status was updated
	var updated cloudflarev1alpha1.CloudflareRuleset
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-ruleset", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated ruleset: %v", err)
	}

	if updated.Status.RulesetID == "" {
		t.Error("expected RulesetID to be set in status")
	}
	if updated.Status.RuleCount != 1 {
		t.Errorf("expected RuleCount=1, got %d", updated.Status.RuleCount)
	}
}

func TestRulesetReconcile_AdoptsExistingEntrypoint(t *testing.T) {
	ruleset := newTestRuleset("test-ruleset", "default")
	ruleset.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	// No RulesetID in status — Cloudflare already has an entrypoint with
	// different rules. The operator should upsert spec rules into that
	// existing entrypoint.
	secret := newTestRulesetSecret("default")

	mock := newMockRulesetClient()
	mock.entrypoints[entrypointKey("zone-123", "http_request_firewall_custom")] = &cfclient.Ruleset{
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

	// Spec rules differ from the pre-existing rules, so we expect an Upsert.
	if !mock.upsertCalled {
		t.Error("expected UpsertPhaseEntrypoint to be called (rules differ from adopted entrypoint)")
	}

	// Status should reflect the adopted entrypoint's ID.
	var updated cloudflarev1alpha1.CloudflareRuleset
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-ruleset", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated ruleset: %v", err)
	}
	if updated.Status.RulesetID != "existing-rs-123" {
		t.Errorf("expected RulesetID=existing-rs-123, got %q", updated.Status.RulesetID)
	}
}

func TestRulesetReconcile_UpdatesEntrypoint(t *testing.T) {
	ruleset := newTestRuleset("test-ruleset", "default")
	ruleset.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	ruleset.Status.RulesetID = "rs-existing"
	secret := newTestRulesetSecret("default")

	mock := newMockRulesetClient()
	mock.entrypoints[entrypointKey("zone-123", "http_request_firewall_custom")] = &cfclient.Ruleset{
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

	if !mock.upsertCalled {
		t.Error("expected UpsertPhaseEntrypoint to be called to reconcile rule drift")
	}

	var updated cloudflarev1alpha1.CloudflareRuleset
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-ruleset", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated ruleset: %v", err)
	}
	if updated.Status.RulesetID != "rs-existing" {
		t.Errorf("expected RulesetID=rs-existing, got %q", updated.Status.RulesetID)
	}
	if updated.Status.RuleCount != 1 {
		t.Errorf("expected RuleCount=1, got %d", updated.Status.RuleCount)
	}
}

func TestRulesetReconcile_DeleteRetainsEntrypoint(t *testing.T) {
	// Phase entrypoints are zone-owned — CR deletion must NOT remove the
	// entrypoint from Cloudflare. It just drops the finalizer.
	ruleset := newTestRuleset("test-ruleset", "default")
	ruleset.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	ruleset.Status.RulesetID = "rs-delete"
	now := metav1.Now()
	ruleset.DeletionTimestamp = &now

	secret := newTestRulesetSecret("default")

	mock := newMockRulesetClient()
	key := entrypointKey("zone-123", "http_request_firewall_custom")
	mock.entrypoints[key] = &cfclient.Ruleset{
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

	// Entrypoint must still exist in Cloudflare after CR delete.
	if _, exists := mock.entrypoints[key]; !exists {
		t.Error("expected phase entrypoint to be retained in Cloudflare after CR deletion")
	}

	// No Upsert during delete path.
	if mock.upsertCalled {
		t.Error("expected no Cloudflare writes on CR deletion")
	}

	// Finalizer should be removed (object may be garbage-collected by the
	// fake client, which is equivalent to finalizer removal from our POV).
	var updated cloudflarev1alpha1.CloudflareRuleset
	err = r.Get(context.Background(), types.NamespacedName{Name: "test-ruleset", Namespace: "default"}, &updated)
	if err == nil {
		for _, f := range updated.Finalizers {
			if f == cloudflarev1alpha1.FinalizerName {
				t.Error("expected finalizer to be removed after deletion")
			}
		}
	}
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
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-ruleset", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated ruleset: %v", err)
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

func TestRulesetReconcile_ZoneRefResolvesFromCloudflareZone(t *testing.T) {
	s := testScheme(t)
	mock := newMockRulesetClient()

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

	// Create a CloudflareRuleset using zoneRef (not zoneID)
	enabled := true
	ruleset := &cloudflarev1alpha1.CloudflareRuleset{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-ruleset",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cloudflarev1alpha1.FinalizerName},
		},
		Spec: cloudflarev1alpha1.CloudflareRulesetSpec{
			ZoneRef:     &cloudflarev1alpha1.ZoneReference{Name: "my-zone"},
			Name:        "test-waf",
			Description: "Test WAF rules",
			Phase:       "http_request_firewall_custom",
			SecretRef:   cloudflarev1alpha1.SecretReference{Name: "cf-secret"},
			Interval:    &metav1.Duration{Duration: 30 * time.Minute},
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

	secret := newTestRulesetSecret("default")

	builder := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(zone, ruleset, secret).
		WithStatusSubresource(zone, ruleset)
	fakeClient := builder.Build()

	r := &CloudflareRulesetReconciler{
		Client:        fakeClient,
		Scheme:        s,
		Recorder:      record.NewFakeRecorder(10),
		ClientFactory: cfclient.NewClientFactory(fakeClient),
		RulesetClientFn: func(_ string) cfclient.RulesetClient {
			return mock
		},
	}

	// Set the CloudflareZone status after creation (fake client requires Status().Update())
	zone.Status.ZoneID = testResolvedZoneID
	zone.Status.Status = testZoneActive
	if err := r.Status().Update(context.Background(), zone); err != nil {
		t.Fatalf("failed to update zone status: %v", err)
	}

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-ruleset", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the mock ruleset client's CreateRuleset was called with the resolved zone ID
	if !mock.upsertCalled {
		t.Error("expected CreateRuleset to be called after resolving zone ID from CloudflareZone")
	}
	if mock.lastZoneID != testResolvedZoneID {
		t.Errorf("expected zone ID passed to ruleset client to be %q, got %q", testResolvedZoneID, mock.lastZoneID)
	}

	// Should requeue after interval
	if result.RequeueAfter != 30*time.Minute {
		t.Errorf("expected RequeueAfter=30m, got %v", result.RequeueAfter)
	}
}

func TestRulesetReconcile_ZoneRefNotReady(t *testing.T) {
	s := testScheme(t)
	mock := newMockRulesetClient()

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

	// Create a CloudflareRuleset using zoneRef pointing to the pending zone
	enabled := true
	ruleset := &cloudflarev1alpha1.CloudflareRuleset{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-ruleset",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cloudflarev1alpha1.FinalizerName},
		},
		Spec: cloudflarev1alpha1.CloudflareRulesetSpec{
			ZoneRef:     &cloudflarev1alpha1.ZoneReference{Name: "pending-zone"},
			Name:        "test-waf",
			Description: "Test WAF rules",
			Phase:       "http_request_firewall_custom",
			SecretRef:   cloudflarev1alpha1.SecretReference{Name: "cf-secret"},
			Interval:    &metav1.Duration{Duration: 30 * time.Minute},
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

	secret := newTestRulesetSecret("default")

	builder := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(zone, ruleset, secret).
		WithStatusSubresource(zone, ruleset)
	fakeClient := builder.Build()

	r := &CloudflareRulesetReconciler{
		Client:        fakeClient,
		Scheme:        s,
		Recorder:      record.NewFakeRecorder(10),
		ClientFactory: cfclient.NewClientFactory(fakeClient),
		RulesetClientFn: func(_ string) cfclient.RulesetClient {
			return mock
		},
	}

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

	// Verify Ready condition is False with ZoneRefNotReady reason
	var updated cloudflarev1alpha1.CloudflareRuleset
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-ruleset", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated ruleset: %v", err)
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

// Deletion behavior no longer depends on zone-ref resolution because the new
// delete path doesn't call Cloudflare at all (phase entrypoints are retained).
// The TestRulesetReconcile_DeleteRetainsEntrypoint test above covers the
// full scenario.

func TestBuildRules_Logging(t *testing.T) {
	r := &CloudflareRulesetReconciler{}
	en := true
	specs := []cloudflarev1alpha1.RulesetRuleSpec{{
		Action:     "skip",
		Expression: "true",
		Logging:    &cloudflarev1alpha1.RuleLogging{Enabled: &en},
	}}
	got, err := r.buildRules(specs)
	if err != nil {
		t.Fatalf("buildRules: %v", err)
	}
	if len(got) != 1 || got[0].Logging == nil || got[0].Logging.Enabled == nil || !*got[0].Logging.Enabled {
		t.Errorf("logging not propagated: %+v", got)
	}
}

func TestBuildRules_NoLogging(t *testing.T) {
	r := &CloudflareRulesetReconciler{}
	specs := []cloudflarev1alpha1.RulesetRuleSpec{{Action: "block", Expression: "true"}}
	got, err := r.buildRules(specs)
	if err != nil {
		t.Fatalf("buildRules: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rules, want 1", len(got))
	}
	if got[0].Logging != nil {
		t.Errorf("logging should be nil; got %+v", got[0].Logging)
	}
}

func TestRulesetMatches_LoggingDiffer(t *testing.T) {
	en := true
	desired := cfclient.RulesetParams{
		Name: "rs", Description: "", Rules: []cfclient.RulesetRule{{
			Action: "skip", Expression: "true", Enabled: true,
			Logging: &cfclient.RuleLogging{Enabled: &en},
		}},
	}
	existing := &cfclient.Ruleset{
		Name: "rs", Description: "", Rules: []cfclient.RulesetRule{{
			Action: "skip", Expression: "true", Enabled: true,
		}},
	}
	if rulesetMatches(existing, desired) {
		t.Errorf("should mismatch when desired has logging but existing does not")
	}
}

func TestRulesetMatches_LoggingEqual(t *testing.T) {
	en := true
	desired := cfclient.RulesetParams{
		Name: "rs", Rules: []cfclient.RulesetRule{{
			Action: "skip", Expression: "true", Enabled: true,
			Logging: &cfclient.RuleLogging{Enabled: &en},
		}},
	}
	existing := &cfclient.Ruleset{
		Name: "rs", Rules: []cfclient.RulesetRule{{
			Action: "skip", Expression: "true", Enabled: true,
			Logging: &cfclient.RuleLogging{Enabled: &en},
		}},
	}
	if !rulesetMatches(existing, desired) {
		t.Errorf("should match when both have logging enabled=true")
	}
}

func TestRuleLoggingEqual(t *testing.T) {
	en := true
	en2 := true
	dis := false
	cases := []struct {
		name string
		a, b *cfclient.RuleLogging
		want bool
	}{
		{"both nil", nil, nil, true},
		{"a nil only", nil, &cfclient.RuleLogging{Enabled: &en}, false},
		{"b nil only", &cfclient.RuleLogging{Enabled: &en}, nil, false},
		{"both set true equal", &cfclient.RuleLogging{Enabled: &en}, &cfclient.RuleLogging{Enabled: &en2}, true},
		{"both set, differ", &cfclient.RuleLogging{Enabled: &en}, &cfclient.RuleLogging{Enabled: &dis}, false},
		{"a Enabled nil", &cfclient.RuleLogging{}, &cfclient.RuleLogging{Enabled: &en}, false},
		{"both Enabled nil", &cfclient.RuleLogging{}, &cfclient.RuleLogging{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ruleLoggingEqual(tc.a, tc.b); got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestRulesetReconcile_BadRequestSetsInvalidSpec(t *testing.T) {
	ruleset := &cloudflarev1alpha1.CloudflareRuleset{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "rs",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cloudflarev1alpha1.FinalizerName},
		},
		Spec: cloudflarev1alpha1.CloudflareRulesetSpec{
			ZoneID:    "zone-1",
			Name:      "test",
			Phase:     "http_request_firewall_custom",
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "creds"},
			Rules: []cloudflarev1alpha1.RulesetRuleSpec{
				{Action: "block", Expression: `(http.host eq "example.com")`},
			},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "default"},
		Data:       map[string][]byte{cfclient.SecretKeyAPIToken: []byte("token")},
	}
	mock := newMockRulesetClient()
	mock.upsertErr = &cfgo.Error{StatusCode: http.StatusBadRequest}

	r := buildRulesetReconciler(mock, ruleset, secret)
	res, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(ruleset),
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if res.RequeueAfter != time.Hour {
		t.Errorf("RequeueAfter = %v, want 1h", res.RequeueAfter)
	}

	updated := &cloudflarev1alpha1.CloudflareRuleset{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(ruleset), updated); err != nil {
		t.Fatalf("get updated ruleset: %v", err)
	}
	cond := meta.FindStatusCondition(updated.Status.Conditions, cloudflarev1alpha1.ConditionTypeReady)
	if cond == nil {
		t.Fatal("Ready condition missing")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("Ready status = %v, want False", cond.Status)
	}
	if cond.Reason != cloudflarev1alpha1.ReasonInvalidSpec {
		t.Errorf("Ready reason = %q, want %q", cond.Reason, cloudflarev1alpha1.ReasonInvalidSpec)
	}
}

// TestRulesetReconcile_BadRequest_EmitsInvalidSpecEvent asserts that a 400 Bad
// Request from the Cloudflare API emits an "InvalidSpec" recorder event, NOT the
// legacy "SyncFailed" event. A future regression that reverts to hardcoded
// "SyncFailed" for classified errors will be caught here.
func TestRulesetReconcile_BadRequest_EmitsInvalidSpecEvent(t *testing.T) {
	ruleset := &cloudflarev1alpha1.CloudflareRuleset{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "rs",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cloudflarev1alpha1.FinalizerName},
		},
		Spec: cloudflarev1alpha1.CloudflareRulesetSpec{
			ZoneID:    "zone-1",
			Name:      "test",
			Phase:     "http_request_firewall_custom",
			SecretRef: cloudflarev1alpha1.SecretReference{Name: "creds"},
			Rules: []cloudflarev1alpha1.RulesetRuleSpec{
				{Action: "block", Expression: `(http.host eq "example.com")`},
			},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "default"},
		Data:       map[string][]byte{cfclient.SecretKeyAPIToken: []byte("token")},
	}
	mock := newMockRulesetClient()
	mock.upsertErr = &cfgo.Error{StatusCode: http.StatusBadRequest}

	s := testScheme(t)
	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(ruleset, secret).
		WithStatusSubresource(ruleset).
		Build()
	fakeRec := record.NewFakeRecorder(10)
	r := &CloudflareRulesetReconciler{
		Client:        fakeClient,
		Scheme:        s,
		Recorder:      fakeRec,
		ClientFactory: cfclient.NewClientFactory(fakeClient),
		RulesetClientFn: func(_ string) cfclient.RulesetClient {
			return mock
		},
	}

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKeyFromObject(ruleset),
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
