package controller

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	cfgov6 "github.com/cloudflare/cloudflare-go/v6"
	"github.com/cloudflare/cloudflare-go/v6/shared"
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

const (
	testSSLModeFull   = "full"
	testMinTLSVersion = "1.2"
	testTLS13ZRT      = "zrt"
)

// mockZoneClient implements cfclient.ZoneClient for testing.
type mockZoneClient struct {
	settings           map[string]any
	botConfig          *cfclient.BotManagementConfig
	updateErrors       map[string]error
	botUpdateErr       error
	updateSettingCalls int
	updateBotCalled    bool
	lastZoneID         string
	appliedSettings    []string // ordered list of setting IDs successfully applied
}

func newMockZoneClient() *mockZoneClient {
	return &mockZoneClient{
		settings:     make(map[string]any),
		updateErrors: make(map[string]error),
	}
}

func (m *mockZoneClient) GetSettings(_ context.Context, _ string) ([]cfclient.ZoneSetting, error) {
	result := make([]cfclient.ZoneSetting, 0, len(m.settings))
	for id, val := range m.settings {
		result = append(result, cfclient.ZoneSetting{ID: id, Value: val})
	}
	return result, nil
}

func (m *mockZoneClient) UpdateSetting(_ context.Context, zoneID, settingID string, value any) error {
	if err, ok := m.updateErrors[settingID]; ok {
		return err
	}
	m.lastZoneID = zoneID
	m.settings[settingID] = value
	m.appliedSettings = append(m.appliedSettings, settingID)
	m.updateSettingCalls++
	return nil
}

func (m *mockZoneClient) GetBotManagement(_ context.Context, _ string) (*cfclient.BotManagementConfig, error) {
	if m.botConfig != nil {
		return m.botConfig, nil
	}
	return &cfclient.BotManagementConfig{}, nil
}

func (m *mockZoneClient) UpdateBotManagement(_ context.Context, zoneID string, config cfclient.BotManagementConfig) error {
	m.updateBotCalled = true
	m.lastZoneID = zoneID
	if m.botUpdateErr != nil {
		return m.botUpdateErr
	}
	m.botConfig = &config
	return nil
}

// Helper to create a base CloudflareZoneConfig for tests.
func newTestZoneConfig(name, namespace string) *cloudflarev1alpha1.CloudflareZoneConfig {
	return &cloudflarev1alpha1.CloudflareZoneConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Generation: 1,
		},
		Spec: cloudflarev1alpha1.CloudflareZoneConfigSpec{
			ZoneID: "zone-123",
			SecretRef: cloudflarev1alpha1.SecretReference{
				Name: "cf-secret",
			},
			Interval: &metav1.Duration{Duration: 30 * time.Minute},
		},
	}
}

// Helper to create the Cloudflare API token secret for zone config tests.
func newTestZoneConfigSecret(namespace string) *corev1.Secret {
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

// buildZoneConfigReconciler creates a CloudflareZoneConfigReconciler wired to a fake client and mock zone client.
func buildZoneConfigReconciler(mock *mockZoneClient, objs ...client.Object) *CloudflareZoneConfigReconciler {
	s := testScheme(&testing.T{})

	// Collect CRD objects for status subresource registration
	var statusObjs []client.Object
	for _, o := range objs {
		switch o.(type) {
		case *cloudflarev1alpha1.CloudflareZoneConfig, *cloudflarev1alpha1.CloudflareZone:
			statusObjs = append(statusObjs, o)
		}
	}

	builder := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(statusObjs...)

	fakeClient := builder.Build()

	return &CloudflareZoneConfigReconciler{
		Client:        fakeClient,
		Scheme:        s,
		Recorder:      record.NewFakeRecorder(10),
		ClientFactory: cfclient.NewClientFactory(fakeClient, fakeClient),
		ZoneClientFn: func(_ string) cfclient.ZoneClient {
			return mock
		},
	}
}

func TestZoneConfigReconcile_AppliesSSLSettings(t *testing.T) {
	zoneConfig := newTestZoneConfig("test-zone-config", "default")
	zoneConfig.Finalizers = []string{cloudflarev1alpha1.FinalizerName}

	sslMode := testSSLModeFull
	minTLS := testMinTLSVersion
	tls13 := testTLS13ZRT
	alwaysHTTPS := "on"
	autoRewrites := "on"
	oppEncryption := "on"
	zoneConfig.Spec.SSL = &cloudflarev1alpha1.SSLSettings{
		Mode:                    &sslMode,
		MinTLSVersion:           &minTLS,
		TLS13:                   &tls13,
		AlwaysUseHTTPS:          &alwaysHTTPS,
		AutomaticHTTPSRewrites:  &autoRewrites,
		OpportunisticEncryption: &oppEncryption,
	}

	secret := newTestZoneConfigSecret("default")
	mock := newMockZoneClient()

	r := buildZoneConfigReconciler(mock, zoneConfig, secret)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone-config", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should requeue after interval
	if result.RequeueAfter != 30*time.Minute {
		t.Errorf("expected RequeueAfter=30m, got %v", result.RequeueAfter)
	}

	// Verify 6 SSL settings were applied
	if mock.updateSettingCalls != 6 {
		t.Errorf("expected 6 UpdateSetting calls, got %d", mock.updateSettingCalls)
	}

	// Verify specific settings
	if mock.settings["ssl"] != testSSLModeFull {
		t.Errorf("expected ssl=full, got %v", mock.settings["ssl"])
	}
	if mock.settings["min_tls_version"] != testMinTLSVersion {
		t.Errorf("expected min_tls_version=1.2, got %v", mock.settings["min_tls_version"])
	}
	if mock.settings["tls_1_3"] != testTLS13ZRT {
		t.Errorf("expected tls_1_3=zrt, got %v", mock.settings["tls_1_3"])
	}
	if mock.settings["always_use_https"] != "on" {
		t.Errorf("expected always_use_https=on, got %v", mock.settings["always_use_https"])
	}
	if mock.settings["automatic_https_rewrites"] != "on" {
		t.Errorf("expected automatic_https_rewrites=on, got %v", mock.settings["automatic_https_rewrites"])
	}
	if mock.settings["opportunistic_encryption"] != "on" {
		t.Errorf("expected opportunistic_encryption=on, got %v", mock.settings["opportunistic_encryption"])
	}
}

func TestZoneConfigReconcile_AppliesAllSettings(t *testing.T) {
	zoneConfig := newTestZoneConfig("test-zone-config", "default")
	zoneConfig.Finalizers = []string{cloudflarev1alpha1.FinalizerName}

	// SSL settings (6)
	sslMode := testSSLModeFull
	minTLS := testMinTLSVersion
	tls13 := testTLS13ZRT
	alwaysHTTPS := "on"
	autoRewrites := "on"
	oppEncryption := "on"
	zoneConfig.Spec.SSL = &cloudflarev1alpha1.SSLSettings{
		Mode:                    &sslMode,
		MinTLSVersion:           &minTLS,
		TLS13:                   &tls13,
		AlwaysUseHTTPS:          &alwaysHTTPS,
		AutomaticHTTPSRewrites:  &autoRewrites,
		OpportunisticEncryption: &oppEncryption,
	}

	// Security settings (4)
	secLevel := "medium"
	challengeTTL := 1800
	browserCheck := "on"
	emailObfuscation := "on"
	zoneConfig.Spec.Security = &cloudflarev1alpha1.SecuritySettings{
		SecurityLevel:    &secLevel,
		ChallengeTTL:     &challengeTTL,
		BrowserCheck:     &browserCheck,
		EmailObfuscation: &emailObfuscation,
	}

	// Performance settings (8 — including minify)
	cacheLevel := "aggressive"
	browserCacheTTL := 14400
	brotli := "on"
	earlyHints := "on"
	http2 := "on"
	http3 := "on"
	polish := "lossless"
	cssMinify := "on"
	htmlMinify := "on"
	jsMinify := "on"
	zoneConfig.Spec.Performance = &cloudflarev1alpha1.PerformanceSettings{
		CacheLevel:      &cacheLevel,
		BrowserCacheTTL: &browserCacheTTL,
		Brotli:          &brotli,
		EarlyHints:      &earlyHints,
		HTTP2:           &http2,
		HTTP3:           &http3,
		Polish:          &polish,
		Minify: &cloudflarev1alpha1.MinifySettings{
			CSS:  &cssMinify,
			HTML: &htmlMinify,
			JS:   &jsMinify,
		},
	}

	// Network settings (5)
	ipv6 := "on"
	websockets := "on"
	pseudoIPv4 := "add_header"
	ipGeolocation := "on"
	oppOnion := "on"
	zoneConfig.Spec.Network = &cloudflarev1alpha1.NetworkSettings{
		IPv6:               &ipv6,
		WebSockets:         &websockets,
		PseudoIPv4:         &pseudoIPv4,
		IPGeolocation:      &ipGeolocation,
		OpportunisticOnion: &oppOnion,
	}

	// Bot management (+1 for bot management)
	enableJS := true
	fightMode := true
	zoneConfig.Spec.BotManagement = &cloudflarev1alpha1.BotManagementSettings{
		EnableJS:  &enableJS,
		FightMode: &fightMode,
	}

	secret := newTestZoneConfigSecret("default")
	mock := newMockZoneClient()

	r := buildZoneConfigReconciler(mock, zoneConfig, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone-config", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 6 SSL + 4 Security + 8 Performance + 5 Network = 23 regular settings
	expectedSettingCalls := 23
	if mock.updateSettingCalls != expectedSettingCalls {
		t.Errorf("expected %d UpdateSetting calls, got %d", expectedSettingCalls, mock.updateSettingCalls)
	}

	// Bot management called separately
	if !mock.updateBotCalled {
		t.Error("expected UpdateBotManagement to be called")
	}
}

func TestZoneConfigReconcile_AppliesBotManagement(t *testing.T) {
	zoneConfig := newTestZoneConfig("test-zone-config", "default")
	zoneConfig.Finalizers = []string{cloudflarev1alpha1.FinalizerName}

	enableJS := true
	fightMode := false
	zoneConfig.Spec.BotManagement = &cloudflarev1alpha1.BotManagementSettings{
		EnableJS:  &enableJS,
		FightMode: &fightMode,
	}

	secret := newTestZoneConfigSecret("default")
	mock := newMockZoneClient()

	r := buildZoneConfigReconciler(mock, zoneConfig, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone-config", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Bot management should be called
	if !mock.updateBotCalled {
		t.Error("expected UpdateBotManagement to be called")
	}

	// No regular settings
	if mock.updateSettingCalls != 0 {
		t.Errorf("expected 0 UpdateSetting calls, got %d", mock.updateSettingCalls)
	}

	// Verify bot config was set correctly
	if mock.botConfig == nil {
		t.Fatal("expected bot config to be set")
	}
	if mock.botConfig.EnableJS == nil || !*mock.botConfig.EnableJS {
		t.Error("expected EnableJS=true")
	}
	if mock.botConfig.FightMode == nil || *mock.botConfig.FightMode {
		t.Error("expected FightMode=false")
	}
}

func TestZoneConfigReconcile_DeleteDoesNotRevert(t *testing.T) {
	zoneConfig := newTestZoneConfig("test-zone-config", "default")
	zoneConfig.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	now := metav1.Now()
	zoneConfig.DeletionTimestamp = &now

	secret := newTestZoneConfigSecret("default")
	mock := newMockZoneClient()

	r := buildZoneConfigReconciler(mock, zoneConfig, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone-config", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No Cloudflare API calls should have been made
	if mock.updateSettingCalls != 0 {
		t.Errorf("expected 0 UpdateSetting calls during delete, got %d", mock.updateSettingCalls)
	}
	if mock.updateBotCalled {
		t.Error("expected UpdateBotManagement NOT to be called during delete")
	}

	// Verify finalizer was removed (object may be garbage-collected by fake client)
	var updated cloudflarev1alpha1.CloudflareZoneConfig
	err = r.Get(context.Background(), types.NamespacedName{Name: "test-zone-config", Namespace: "default"}, &updated)
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

func TestZoneConfigReconcile_SecretNotFound(t *testing.T) {
	zoneConfig := newTestZoneConfig("test-zone-config", "default")
	zoneConfig.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	// No secret created — should fail to get API token
	mock := newMockZoneClient()

	r := buildZoneConfigReconciler(mock, zoneConfig)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone-config", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error (should be handled gracefully): %v", err)
	}

	// Should requeue after 30s
	if result.RequeueAfter != 30*time.Second {
		t.Errorf("expected RequeueAfter=30s, got %v", result.RequeueAfter)
	}

	// Verify Ready condition is False with SecretNotFound reason
	var updated cloudflarev1alpha1.CloudflareZoneConfig
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-zone-config", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated zone config: %v", err)
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

func TestZoneConfigReconcile_ZoneRefResolvesFromCloudflareZone(t *testing.T) {
	mock := newMockZoneClient()

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

	// Create a CloudflareZoneConfig using zoneRef (not zoneID)
	sslMode := testSSLModeFull
	zoneConfig := &cloudflarev1alpha1.CloudflareZoneConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-zone-config",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cloudflarev1alpha1.FinalizerName},
		},
		Spec: cloudflarev1alpha1.CloudflareZoneConfigSpec{
			ZoneRef: &cloudflarev1alpha1.ZoneReference{Name: "my-zone"},
			SecretRef: cloudflarev1alpha1.SecretReference{
				Name: "cf-secret",
			},
			Interval: &metav1.Duration{Duration: 30 * time.Minute},
			SSL: &cloudflarev1alpha1.SSLSettings{
				Mode: &sslMode,
			},
		},
	}

	secret := newTestZoneConfigSecret("default")

	r := buildZoneConfigReconciler(mock, zone, zoneConfig, secret)

	// Set the CloudflareZone status after creation (fake client requires Status().Update())
	zone.Status.ZoneID = "resolved-zone-id"
	zone.Status.Status = "active"
	if err := r.Status().Update(context.Background(), zone); err != nil {
		t.Fatalf("failed to update zone status: %v", err)
	}

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone-config", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the mock zone client's UpdateSetting was called (meaning zone ID was resolved)
	if mock.updateSettingCalls == 0 {
		t.Error("expected UpdateSetting to be called after resolving zone ID from CloudflareZone")
	}

	// Verify the resolved zone ID was passed to the API
	if mock.lastZoneID != "resolved-zone-id" {
		t.Errorf("expected zone ID passed to zone client to be %q, got %q", "resolved-zone-id", mock.lastZoneID)
	}

	// Should requeue after interval
	if result.RequeueAfter != 30*time.Minute {
		t.Errorf("expected RequeueAfter=30m, got %v", result.RequeueAfter)
	}
}

func TestZoneConfigReconcile_ZoneRefNotReady(t *testing.T) {
	mock := newMockZoneClient()

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

	// Create a CloudflareZoneConfig using zoneRef pointing to the pending zone
	sslMode := testSSLModeFull
	zoneConfig := &cloudflarev1alpha1.CloudflareZoneConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-zone-config",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{cloudflarev1alpha1.FinalizerName},
		},
		Spec: cloudflarev1alpha1.CloudflareZoneConfigSpec{
			ZoneRef: &cloudflarev1alpha1.ZoneReference{Name: "pending-zone"},
			SecretRef: cloudflarev1alpha1.SecretReference{
				Name: "cf-secret",
			},
			Interval: &metav1.Duration{Duration: 30 * time.Minute},
			SSL: &cloudflarev1alpha1.SSLSettings{
				Mode: &sslMode,
			},
		},
	}

	secret := newTestZoneConfigSecret("default")

	r := buildZoneConfigReconciler(mock, zone, zoneConfig, secret)

	result, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone-config", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("unexpected error (should be handled gracefully): %v", err)
	}

	// Should requeue after 30s
	if result.RequeueAfter != 30*time.Second {
		t.Errorf("expected RequeueAfter=30s, got %v", result.RequeueAfter)
	}

	// Verify Ready condition is False with ZoneRefNotReady reason
	var updated cloudflarev1alpha1.CloudflareZoneConfig
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-zone-config", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated zone config: %v", err)
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

func TestZoneConfigReconcile_PartialApply_BotManagement403(t *testing.T) {
	zoneConfig := newTestZoneConfig("test-zone-config", "default")
	zoneConfig.Finalizers = []string{cloudflarev1alpha1.FinalizerName}

	sslMode := testSSLModeFull
	zoneConfig.Spec.SSL = &cloudflarev1alpha1.SSLSettings{Mode: &sslMode}

	secLevel := "medium"
	zoneConfig.Spec.Security = &cloudflarev1alpha1.SecuritySettings{SecurityLevel: &secLevel}

	enableJS := true
	zoneConfig.Spec.BotManagement = &cloudflarev1alpha1.BotManagementSettings{EnableJS: &enableJS}

	secret := newTestZoneConfigSecret("default")
	mock := newMockZoneClient()
	mock.botUpdateErr = &cfgov6.Error{StatusCode: http.StatusForbidden}

	r := buildZoneConfigReconciler(mock, zoneConfig, secret)

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone-config", Namespace: "default"},
	})
	// failReconcile returns nil err and a requeue, so the outer Reconcile returns nil.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// SSL and Security applied; BotManagement attempted and failed.
	if !contains(mock.appliedSettings, "ssl") {
		t.Errorf("expected ssl to be applied, got %v", mock.appliedSettings)
	}
	if !contains(mock.appliedSettings, "security_level") {
		t.Errorf("expected security_level to be applied, got %v", mock.appliedSettings)
	}
	if !mock.updateBotCalled {
		t.Error("expected UpdateBotManagement to be attempted")
	}

	var updated cloudflarev1alpha1.CloudflareZoneConfig
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-zone-config", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("get updated: %v", err)
	}

	wantConditions := map[string]struct {
		status metav1.ConditionStatus
		reason string
	}{
		cloudflarev1alpha1.ConditionTypeSSLApplied:           {metav1.ConditionTrue, cloudflarev1alpha1.ReasonApplied},
		cloudflarev1alpha1.ConditionTypeSecurityApplied:      {metav1.ConditionTrue, cloudflarev1alpha1.ReasonApplied},
		cloudflarev1alpha1.ConditionTypePerformanceApplied:   {metav1.ConditionFalse, cloudflarev1alpha1.ReasonNotConfigured},
		cloudflarev1alpha1.ConditionTypeNetworkApplied:       {metav1.ConditionFalse, cloudflarev1alpha1.ReasonNotConfigured},
		cloudflarev1alpha1.ConditionTypeDNSApplied:           {metav1.ConditionFalse, cloudflarev1alpha1.ReasonNotConfigured},
		cloudflarev1alpha1.ConditionTypeBotManagementApplied: {metav1.ConditionFalse, cloudflarev1alpha1.ReasonPermissionDenied},
		cloudflarev1alpha1.ConditionTypeReady:                {metav1.ConditionFalse, cloudflarev1alpha1.ReasonPartialApply},
	}
	for ct, want := range wantConditions {
		got := findCondition(updated.Status.Conditions, ct)
		if got == nil {
			t.Errorf("condition %s not set", ct)
			continue
		}
		if got.Status != want.status {
			t.Errorf("%s status = %s, want %s", ct, got.Status, want.status)
		}
		if got.Reason != want.reason {
			t.Errorf("%s reason = %s, want %s", ct, got.Reason, want.reason)
		}
	}

	if updated.Status.AppliedSpecHash != "" {
		t.Errorf("expected appliedSpecHash to be empty on partial failure, got %q", updated.Status.AppliedSpecHash)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func findCondition(conds []metav1.Condition, t string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == t {
			return &conds[i]
		}
	}
	return nil
}

func TestZoneConfigReconcile_PartialApply_Generic5xx(t *testing.T) {
	zoneConfig := newTestZoneConfig("test-zone-config", "default")
	zoneConfig.Finalizers = []string{cloudflarev1alpha1.FinalizerName}

	sslMode := testSSLModeFull
	zoneConfig.Spec.SSL = &cloudflarev1alpha1.SSLSettings{Mode: &sslMode}

	secLevel := "medium"
	zoneConfig.Spec.Security = &cloudflarev1alpha1.SecuritySettings{SecurityLevel: &secLevel}

	secret := newTestZoneConfigSecret("default")
	mock := newMockZoneClient()
	mock.updateErrors["ssl"] = &cfgov6.Error{StatusCode: http.StatusBadGateway}

	r := buildZoneConfigReconciler(mock, zoneConfig, secret)

	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone-config", Namespace: "default"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if contains(mock.appliedSettings, "ssl") {
		t.Error("ssl should not have been recorded as applied")
	}
	if !contains(mock.appliedSettings, "security_level") {
		t.Errorf("security_level expected; appliedSettings=%v", mock.appliedSettings)
	}

	var updated cloudflarev1alpha1.CloudflareZoneConfig
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-zone-config", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("get updated: %v", err)
	}
	ssl := findCondition(updated.Status.Conditions, cloudflarev1alpha1.ConditionTypeSSLApplied)
	if ssl == nil || ssl.Status != metav1.ConditionFalse || ssl.Reason != cloudflarev1alpha1.ReasonCloudflareError {
		t.Errorf("ssl condition = %+v, want False/CloudflareAPIError", ssl)
	}
	sec := findCondition(updated.Status.Conditions, cloudflarev1alpha1.ConditionTypeSecurityApplied)
	if sec == nil || sec.Status != metav1.ConditionTrue || sec.Reason != cloudflarev1alpha1.ReasonApplied {
		t.Errorf("security condition = %+v, want True/Applied", sec)
	}
}

func TestZoneConfigReconcile_FullSuccess_SetsHashAndReady(t *testing.T) {
	zoneConfig := newTestZoneConfig("test-zone-config", "default")
	zoneConfig.Finalizers = []string{cloudflarev1alpha1.FinalizerName}

	sslMode := testSSLModeFull
	zoneConfig.Spec.SSL = &cloudflarev1alpha1.SSLSettings{Mode: &sslMode}
	enableJS := true
	zoneConfig.Spec.BotManagement = &cloudflarev1alpha1.BotManagementSettings{EnableJS: &enableJS}

	secret := newTestZoneConfigSecret("default")
	mock := newMockZoneClient()
	r := buildZoneConfigReconciler(mock, zoneConfig, secret)

	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone-config", Namespace: "default"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated cloudflarev1alpha1.CloudflareZoneConfig
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-zone-config", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("get updated: %v", err)
	}
	if updated.Status.AppliedSpecHash == "" {
		t.Error("expected appliedSpecHash to be set on full success")
	}
	ready := findCondition(updated.Status.Conditions, cloudflarev1alpha1.ConditionTypeReady)
	if ready == nil || ready.Status != metav1.ConditionTrue || ready.Reason != cloudflarev1alpha1.ReasonReconcileSuccess {
		t.Errorf("ready = %+v, want True/ReconcileSuccess", ready)
	}
}

func TestZoneConfigReconcile_HashSkip(t *testing.T) {
	zoneConfig := newTestZoneConfig("test-zone-config", "default")
	zoneConfig.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	sslMode := testSSLModeFull
	zoneConfig.Spec.SSL = &cloudflarev1alpha1.SSLSettings{Mode: &sslMode}

	// Pre-set the hash to match the current spec to simulate "already converged".
	zoneConfig.Status.AppliedSpecHash = hashZoneConfigSpec(&zoneConfig.Spec)

	secret := newTestZoneConfigSecret("default")
	mock := newMockZoneClient()
	r := buildZoneConfigReconciler(mock, zoneConfig, secret)

	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone-config", Namespace: "default"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mock.updateSettingCalls != 0 {
		t.Errorf("expected 0 UpdateSetting calls when hash matches, got %d", mock.updateSettingCalls)
	}
	if mock.updateBotCalled {
		t.Error("expected UpdateBotManagement NOT to be called when hash matches")
	}
}

func TestZoneConfigReconcile_RecoverFromPartial(t *testing.T) {
	zoneConfig := newTestZoneConfig("test-zone-config", "default")
	zoneConfig.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	sslMode := testSSLModeFull
	zoneConfig.Spec.SSL = &cloudflarev1alpha1.SSLSettings{Mode: &sslMode}
	enableJS := true
	zoneConfig.Spec.BotManagement = &cloudflarev1alpha1.BotManagementSettings{EnableJS: &enableJS}

	secret := newTestZoneConfigSecret("default")
	mock := newMockZoneClient()
	mock.botUpdateErr = &cfgov6.Error{StatusCode: http.StatusForbidden}

	recorder := record.NewFakeRecorder(20)
	r := buildZoneConfigReconciler(mock, zoneConfig, secret)
	r.Recorder = recorder

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-zone-config", Namespace: "default"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first reconcile error: %v", err)
	}

	// Now clear the bot 403 and re-reconcile.
	mock.botUpdateErr = nil
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile error: %v", err)
	}

	var updated cloudflarev1alpha1.CloudflareZoneConfig
	if err := r.Get(context.Background(), req.NamespacedName, &updated); err != nil {
		t.Fatalf("get updated: %v", err)
	}
	if updated.Status.AppliedSpecHash == "" {
		t.Error("expected appliedSpecHash to be set after recovery")
	}
	bm := findCondition(updated.Status.Conditions, cloudflarev1alpha1.ConditionTypeBotManagementApplied)
	if bm == nil || bm.Status != metav1.ConditionTrue {
		t.Errorf("BotManagementApplied = %+v, want True", bm)
	}

	// Drain the recorder and assert at least one Normal SettingsApplied event for BotManagement.
	got := drainEvents(recorder)
	wantPrefix := "Normal SettingsApplied BotManagement applied"
	found := false
	for _, e := range got {
		if strings.HasPrefix(e, wantPrefix) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected event with prefix %q; got %v", wantPrefix, got)
	}
}

func TestZoneConfigReconcile_NotConfiguredCondition(t *testing.T) {
	zoneConfig := newTestZoneConfig("test-zone-config", "default")
	zoneConfig.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	sslMode := testSSLModeFull
	zoneConfig.Spec.SSL = &cloudflarev1alpha1.SSLSettings{Mode: &sslMode}
	// No Security / Performance / Network / BotManagement.

	secret := newTestZoneConfigSecret("default")
	mock := newMockZoneClient()
	r := buildZoneConfigReconciler(mock, zoneConfig, secret)

	if _, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone-config", Namespace: "default"},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated cloudflarev1alpha1.CloudflareZoneConfig
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-zone-config", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("get updated: %v", err)
	}
	for _, ct := range []string{
		cloudflarev1alpha1.ConditionTypeSecurityApplied,
		cloudflarev1alpha1.ConditionTypePerformanceApplied,
		cloudflarev1alpha1.ConditionTypeNetworkApplied,
		cloudflarev1alpha1.ConditionTypeDNSApplied,
		cloudflarev1alpha1.ConditionTypeBotManagementApplied,
	} {
		c := findCondition(updated.Status.Conditions, ct)
		if c == nil {
			t.Errorf("%s not set", ct)
			continue
		}
		if c.Status != metav1.ConditionFalse || c.Reason != cloudflarev1alpha1.ReasonNotConfigured {
			t.Errorf("%s = %+v, want False/NotConfigured", ct, c)
		}
	}
	ssl := findCondition(updated.Status.Conditions, cloudflarev1alpha1.ConditionTypeSSLApplied)
	if ssl == nil || ssl.Status != metav1.ConditionTrue || ssl.Reason != cloudflarev1alpha1.ReasonApplied {
		t.Errorf("SSLApplied = %+v, want True/Applied", ssl)
	}
}

func TestZoneConfigReconcile_GroupRemovedFromSpec(t *testing.T) {
	zoneConfig := newTestZoneConfig("test-zone-config", "default")
	zoneConfig.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	sslMode := testSSLModeFull
	zoneConfig.Spec.SSL = &cloudflarev1alpha1.SSLSettings{Mode: &sslMode}
	enableJS := true
	zoneConfig.Spec.BotManagement = &cloudflarev1alpha1.BotManagementSettings{EnableJS: &enableJS}

	secret := newTestZoneConfigSecret("default")
	mock := newMockZoneClient()
	r := buildZoneConfigReconciler(mock, zoneConfig, secret)

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-zone-config", Namespace: "default"}}

	// First reconcile: both groups apply.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	// Mutate the spec — remove BotManagement.
	var updated cloudflarev1alpha1.CloudflareZoneConfig
	if err := r.Get(context.Background(), req.NamespacedName, &updated); err != nil {
		t.Fatalf("get updated: %v", err)
	}
	updated.Spec.BotManagement = nil
	updated.Generation = 2
	if err := r.Update(context.Background(), &updated); err != nil {
		t.Fatalf("update spec: %v", err)
	}

	// Second reconcile.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	if err := r.Get(context.Background(), req.NamespacedName, &updated); err != nil {
		t.Fatalf("get updated: %v", err)
	}
	bm := findCondition(updated.Status.Conditions, cloudflarev1alpha1.ConditionTypeBotManagementApplied)
	if bm == nil || bm.Status != metav1.ConditionFalse || bm.Reason != cloudflarev1alpha1.ReasonNotConfigured {
		t.Errorf("BotManagementApplied = %+v, want False/NotConfigured", bm)
	}
	ready := findCondition(updated.Status.Conditions, cloudflarev1alpha1.ConditionTypeReady)
	if ready == nil || ready.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %+v, want True", ready)
	}
}

// TestZoneConfigReconcile_NoEventSpamInSteadyDegradedState verifies that a
// CloudflareZoneConfig stuck in a degraded state (e.g. persistent BotManagement
// 403) does not produce a fresh Warning event on every reconcile. Per design
// §4.5, per-group events fire only on transitions, and the top-level
// SyncFailed event has been removed in favor of the Ready=False condition's
// aggregated message.
func TestZoneConfigReconcile_NoEventSpamInSteadyDegradedState(t *testing.T) {
	zoneConfig := newTestZoneConfig("test-zone-config", "default")
	zoneConfig.Finalizers = []string{cloudflarev1alpha1.FinalizerName}
	sslMode := testSSLModeFull
	zoneConfig.Spec.SSL = &cloudflarev1alpha1.SSLSettings{Mode: &sslMode}
	enableJS := true
	zoneConfig.Spec.BotManagement = &cloudflarev1alpha1.BotManagementSettings{EnableJS: &enableJS}

	secret := newTestZoneConfigSecret("default")
	mock := newMockZoneClient()
	mock.botUpdateErr = &cfgov6.Error{StatusCode: http.StatusForbidden}

	recorder := record.NewFakeRecorder(20)
	r := buildZoneConfigReconciler(mock, zoneConfig, secret)
	r.Recorder = recorder

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-zone-config", Namespace: "default"}}

	// Two consecutive failing reconciles with the bot 403 still injected.
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first reconcile error: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile error: %v", err)
	}

	// Confirm steady-state degraded conditions persist on the object.
	var updated cloudflarev1alpha1.CloudflareZoneConfig
	if err := r.Get(context.Background(), req.NamespacedName, &updated); err != nil {
		t.Fatalf("get updated: %v", err)
	}
	bm := findCondition(updated.Status.Conditions, cloudflarev1alpha1.ConditionTypeBotManagementApplied)
	if bm == nil || bm.Status != metav1.ConditionFalse || bm.Reason != cloudflarev1alpha1.ReasonPermissionDenied {
		t.Errorf("BotManagementApplied = %+v, want False/PermissionDenied", bm)
	}
	ready := findCondition(updated.Status.Conditions, cloudflarev1alpha1.ConditionTypeReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != cloudflarev1alpha1.ReasonPartialApply {
		t.Errorf("Ready = %+v, want False/PartialApply", ready)
	}

	// Drain events accumulated across both reconciles.
	got := drainEvents(recorder)

	// Count by reason.
	syncFailedCount := 0
	bmApplyFailedCount := 0
	for _, e := range got {
		if strings.Contains(e, " SyncFailed ") {
			syncFailedCount++
		}
		if strings.HasPrefix(e, "Warning SettingsApplyFailed BotManagement") {
			bmApplyFailedCount++
		}
	}

	if syncFailedCount != 0 {
		t.Errorf("expected 0 SyncFailed events (top-level event was removed); got %d. events=%v", syncFailedCount, got)
	}
	if bmApplyFailedCount != 1 {
		t.Errorf("expected exactly 1 SettingsApplyFailed BotManagement event (transition-only); got %d. events=%v", bmApplyFailedCount, got)
	}
}

// equalSettingUpdates compares two []settingUpdate slices using reflect.DeepEqual.
// settingUpdate.value is `any`, so DeepEqual handles both flat string values and
// nested map[string]any payloads.
func equalSettingUpdates(a, b []settingUpdate) bool {
	return reflect.DeepEqual(a, b)
}

func TestAppendSecurity_NewFields(t *testing.T) {
	t.Run("nil section emits nothing", func(t *testing.T) {
		got := appendSecurity(nil, nil)
		if len(got) != 0 {
			t.Fatalf("got %d updates, want 0", len(got))
		}
	})

	t.Run("server_side_exclude only", func(t *testing.T) {
		on := "on"
		got := appendSecurity(nil, &cloudflarev1alpha1.SecuritySettings{ServerSideExclude: &on})
		want := []settingUpdate{{id: "server_side_exclude", value: "on"}}
		if !equalSettingUpdates(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("hotlink_protection only", func(t *testing.T) {
		off := "off"
		got := appendSecurity(nil, &cloudflarev1alpha1.SecuritySettings{HotlinkProtection: &off})
		want := []settingUpdate{{id: "hotlink_protection", value: "off"}}
		if !equalSettingUpdates(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("security_header full", func(t *testing.T) {
		en, sub, pre, ns := true, true, false, true
		ma := 31536000
		got := appendSecurity(nil, &cloudflarev1alpha1.SecuritySettings{
			SecurityHeader: &cloudflarev1alpha1.SecurityHeaderSettings{
				Enabled:           &en,
				MaxAge:            &ma,
				IncludeSubdomains: &sub,
				Preload:           &pre,
				Nosniff:           &ns,
			},
		})
		if len(got) != 1 || got[0].id != "security_header" {
			t.Fatalf("got %+v", got)
		}
		val, ok := got[0].value.(map[string]any)
		if !ok {
			t.Fatalf("value type %T, want map[string]any", got[0].value)
		}
		sts, ok := val["strict_transport_security"].(map[string]any)
		if !ok {
			t.Fatalf("strict_transport_security missing or wrong type: %+v", val)
		}
		if len(sts) != 5 {
			t.Errorf("inner len=%d, want 5; got %+v", len(sts), sts)
		}
		if sts["enabled"] != true || sts["max_age"] != 31536000 ||
			sts["include_subdomains"] != true || sts["preload"] != false ||
			sts["nosniff"] != true {
			t.Errorf("inner payload: %+v", sts)
		}
	})

	t.Run("security_header partial — only Enabled", func(t *testing.T) {
		en := true
		got := appendSecurity(nil, &cloudflarev1alpha1.SecuritySettings{
			SecurityHeader: &cloudflarev1alpha1.SecurityHeaderSettings{Enabled: &en},
		})
		if len(got) != 1 {
			t.Fatalf("got %d updates, want 1", len(got))
		}
		val := got[0].value.(map[string]any)
		sts := val["strict_transport_security"].(map[string]any)
		if len(sts) != 1 {
			t.Errorf("inner payload should have only 'enabled'; got %+v", sts)
		}
		if sts["enabled"] != true {
			t.Errorf("enabled=%v want true", sts["enabled"])
		}
	})

	t.Run("security_header partial — only MaxAge", func(t *testing.T) {
		ma := 86400
		got := appendSecurity(nil, &cloudflarev1alpha1.SecuritySettings{
			SecurityHeader: &cloudflarev1alpha1.SecurityHeaderSettings{MaxAge: &ma},
		})
		if len(got) != 1 {
			t.Fatalf("got %d updates, want 1", len(got))
		}
		val := got[0].value.(map[string]any)
		sts := val["strict_transport_security"].(map[string]any)
		if len(sts) != 1 || sts["max_age"] != 86400 {
			t.Errorf("inner payload: %+v", sts)
		}
	})

	t.Run("security_header all-nil — skip", func(t *testing.T) {
		got := appendSecurity(nil, &cloudflarev1alpha1.SecuritySettings{
			SecurityHeader: &cloudflarev1alpha1.SecurityHeaderSettings{},
		})
		for _, u := range got {
			if u.id == "security_header" {
				t.Errorf("security_header should not be emitted when all inner fields nil; got %+v", u)
			}
		}
	})
}

func TestAppendPerformance_NewFields(t *testing.T) {
	t.Run("always_online only", func(t *testing.T) {
		on := "on"
		got := appendPerformance(nil, &cloudflarev1alpha1.PerformanceSettings{AlwaysOnline: &on})
		want := []settingUpdate{{id: "always_online", value: "on"}}
		if !equalSettingUpdates(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("rocket_loader only", func(t *testing.T) {
		on := "on"
		got := appendPerformance(nil, &cloudflarev1alpha1.PerformanceSettings{RocketLoader: &on})
		want := []settingUpdate{{id: "rocket_loader", value: "on"}}
		if !equalSettingUpdates(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
}

func TestAppendDNS(t *testing.T) {
	t.Run("nil section emits nothing", func(t *testing.T) {
		got := appendDNS(nil, nil)
		if len(got) != 0 {
			t.Fatalf("got %d updates, want 0", len(got))
		}
	})

	t.Run("cname_flattening only", func(t *testing.T) {
		v := "flatten_at_root"
		got := appendDNS(nil, &cloudflarev1alpha1.DNSSettings{CNAMEFlattening: &v})
		want := []settingUpdate{{id: "cname_flattening", value: "flatten_at_root"}}
		if !equalSettingUpdates(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})
}

func TestApplyDNSGroup(t *testing.T) {
	ctx := context.Background()
	zoneID := "zone-123"

	t.Run("nil section returns not-configured and makes no API calls", func(t *testing.T) {
		mock := newMockZoneClient()
		g := applyDNSGroup(ctx, mock, zoneID, nil)
		if g.configured {
			t.Errorf("configured=true, want false")
		}
		if g.err != nil {
			t.Errorf("err=%v, want nil", g.err)
		}
		if g.conditionType != cloudflarev1alpha1.ConditionTypeDNSApplied {
			t.Errorf("conditionType=%q, want %q", g.conditionType, cloudflarev1alpha1.ConditionTypeDNSApplied)
		}
		if mock.updateSettingCalls != 0 {
			t.Errorf("expected 0 UpdateSetting calls, got %d", mock.updateSettingCalls)
		}
		if g.status() != metav1.ConditionFalse {
			t.Errorf("status=%v, want False", g.status())
		}
		if g.reason() != cloudflarev1alpha1.ReasonNotConfigured {
			t.Errorf("reason=%q, want %q", g.reason(), cloudflarev1alpha1.ReasonNotConfigured)
		}
	})

	t.Run("success applies settings and reports True/Applied", func(t *testing.T) {
		mock := newMockZoneClient()
		v := "flatten_at_root"
		dns := &cloudflarev1alpha1.DNSSettings{CNAMEFlattening: &v}

		g := applyDNSGroup(ctx, mock, zoneID, dns)
		if !g.configured {
			t.Errorf("configured=false, want true")
		}
		if g.err != nil {
			t.Errorf("err=%v, want nil", g.err)
		}
		if g.settingsCount != 1 {
			t.Errorf("settingsCount=%d, want 1", g.settingsCount)
		}
		if mock.updateSettingCalls != 1 {
			t.Errorf("expected 1 UpdateSetting call, got %d", mock.updateSettingCalls)
		}
		if mock.settings["cname_flattening"] != "flatten_at_root" {
			t.Errorf("expected cname_flattening=flatten_at_root, got %v", mock.settings["cname_flattening"])
		}
		if g.status() != metav1.ConditionTrue {
			t.Errorf("status=%v, want True", g.status())
		}
		if g.reason() != cloudflarev1alpha1.ReasonApplied {
			t.Errorf("reason=%q, want %q", g.reason(), cloudflarev1alpha1.ReasonApplied)
		}
	})

	t.Run("permission denied classifies as PermissionDenied", func(t *testing.T) {
		mock := newMockZoneClient()
		mock.updateErrors["cname_flattening"] = &cfgov6.Error{StatusCode: http.StatusForbidden}
		v := "flatten_at_root"
		dns := &cloudflarev1alpha1.DNSSettings{CNAMEFlattening: &v}

		g := applyDNSGroup(ctx, mock, zoneID, dns)
		if !g.configured {
			t.Errorf("configured=false, want true")
		}
		if g.err == nil {
			t.Fatal("err=nil, want non-nil")
		}
		if g.status() != metav1.ConditionFalse {
			t.Errorf("status=%v, want False", g.status())
		}
		if g.reason() != cloudflarev1alpha1.ReasonPermissionDenied {
			t.Errorf("reason=%q, want %q", g.reason(), cloudflarev1alpha1.ReasonPermissionDenied)
		}
	})

	t.Run("generic API error classifies as CloudflareError", func(t *testing.T) {
		mock := newMockZoneClient()
		mock.updateErrors["cname_flattening"] = &cfgov6.Error{StatusCode: http.StatusBadGateway}
		v := "flatten_at_root"
		dns := &cloudflarev1alpha1.DNSSettings{CNAMEFlattening: &v}

		g := applyDNSGroup(ctx, mock, zoneID, dns)
		if !g.configured {
			t.Errorf("configured=false, want true")
		}
		if g.err == nil {
			t.Fatal("err=nil, want non-nil")
		}
		if g.status() != metav1.ConditionFalse {
			t.Errorf("status=%v, want False", g.status())
		}
		if g.reason() != cloudflarev1alpha1.ReasonCloudflareError {
			t.Errorf("reason=%q, want %q", g.reason(), cloudflarev1alpha1.ReasonCloudflareError)
		}
	})
}

func TestHashZoneConfigSpec_IncludesDNS(t *testing.T) {
	a := "flatten_at_root"
	b := "flatten_all"
	specA := cloudflarev1alpha1.CloudflareZoneConfigSpec{DNS: &cloudflarev1alpha1.DNSSettings{CNAMEFlattening: &a}}
	specB := cloudflarev1alpha1.CloudflareZoneConfigSpec{DNS: &cloudflarev1alpha1.DNSSettings{CNAMEFlattening: &b}}
	if hashZoneConfigSpec(&specA) == hashZoneConfigSpec(&specB) {
		t.Errorf("hash should differ when DNS.CNAMEFlattening changes")
	}
}

func TestHashZoneConfigSpec_ChangesOnNewFields(t *testing.T) {
	on := "on"
	off := "off"
	a := cloudflarev1alpha1.CloudflareZoneConfigSpec{
		Security: &cloudflarev1alpha1.SecuritySettings{ServerSideExclude: &on},
	}
	b := cloudflarev1alpha1.CloudflareZoneConfigSpec{
		Security: &cloudflarev1alpha1.SecuritySettings{ServerSideExclude: &off},
	}
	if hashZoneConfigSpec(&a) == hashZoneConfigSpec(&b) {
		t.Errorf("hash should differ when ServerSideExclude flips")
	}

	c := cloudflarev1alpha1.CloudflareZoneConfigSpec{
		Performance: &cloudflarev1alpha1.PerformanceSettings{RocketLoader: &on},
	}
	d := cloudflarev1alpha1.CloudflareZoneConfigSpec{
		Performance: &cloudflarev1alpha1.PerformanceSettings{RocketLoader: &off},
	}
	if hashZoneConfigSpec(&c) == hashZoneConfigSpec(&d) {
		t.Errorf("hash should differ when RocketLoader flips")
	}
}

func TestZoneConfigReconcile_BotManagement_PlanTier_Distinct(t *testing.T) {
	zoneConfig := newTestZoneConfig("test-zone-config", "default")
	zoneConfig.Finalizers = []string{cloudflarev1alpha1.FinalizerName}

	enableJS := true
	zoneConfig.Spec.BotManagement = &cloudflarev1alpha1.BotManagementSettings{EnableJS: &enableJS}

	secret := newTestZoneConfigSecret("default")
	mock := newMockZoneClient()
	// 403 with code 1015 — plan-tier restriction, not a token-permission error.
	mock.botUpdateErr = &cfgov6.Error{
		StatusCode: http.StatusForbidden,
		Errors:     []shared.ErrorData{{Code: 1015}},
	}

	r := buildZoneConfigReconciler(mock, zoneConfig, secret)
	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-zone-config", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}

	var updated cloudflarev1alpha1.CloudflareZoneConfig
	if err := r.Get(context.Background(), types.NamespacedName{Name: "test-zone-config", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("get updated: %v", err)
	}
	cond := findCondition(updated.Status.Conditions, cloudflarev1alpha1.ConditionTypeBotManagementApplied)
	if cond == nil {
		t.Fatal("BotManagementApplied condition missing")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Errorf("BotManagementApplied status = %v, want False", cond.Status)
	}
	if cond.Reason != cloudflarev1alpha1.ReasonPlanTierRequired {
		t.Errorf("BotManagementApplied reason = %q, want %q (plan-tier 403/1015 must classify distinctly from PermissionDenied)",
			cond.Reason, cloudflarev1alpha1.ReasonPlanTierRequired)
	}
}
