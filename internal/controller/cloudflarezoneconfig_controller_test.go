package controller

import (
	"context"
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

// mockZoneClient implements cfclient.ZoneClient for testing.
type mockZoneClient struct {
	settings          map[string]any
	botConfig         *cfclient.BotManagementConfig
	updateErrors      map[string]error
	botUpdateErr      error
	updateSettingCalls int
	updateBotCalled   bool
}

func newMockZoneClient() *mockZoneClient {
	return &mockZoneClient{
		settings:     make(map[string]any),
		updateErrors: make(map[string]error),
	}
}

func (m *mockZoneClient) GetSettings(_ context.Context, _ string) ([]cfclient.ZoneSetting, error) {
	var result []cfclient.ZoneSetting
	for id, val := range m.settings {
		result = append(result, cfclient.ZoneSetting{ID: id, Value: val})
	}
	return result, nil
}

func (m *mockZoneClient) UpdateSetting(_ context.Context, _, settingID string, value any) error {
	if err, ok := m.updateErrors[settingID]; ok {
		return err
	}
	m.settings[settingID] = value
	m.updateSettingCalls++
	return nil
}

func (m *mockZoneClient) GetBotManagement(_ context.Context, _ string) (*cfclient.BotManagementConfig, error) {
	if m.botConfig != nil {
		return m.botConfig, nil
	}
	return &cfclient.BotManagementConfig{}, nil
}

func (m *mockZoneClient) UpdateBotManagement(_ context.Context, _ string, config cfclient.BotManagementConfig) error {
	m.updateBotCalled = true
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
		if _, ok := o.(*cloudflarev1alpha1.CloudflareZoneConfig); ok {
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
		ClientFactory: cfclient.NewClientFactory(fakeClient),
		ZoneClientFn: func(_ string) cfclient.ZoneClient {
			return mock
		},
	}
}

func TestZoneConfigReconcile_AppliesSSLSettings(t *testing.T) {
	zoneConfig := newTestZoneConfig("test-zone-config", "default")
	zoneConfig.Finalizers = []string{cloudflarev1alpha1.FinalizerName}

	sslMode := "full"
	minTLS := "1.2"
	tls13 := "zrt"
	alwaysHTTPS := "on"
	autoRewrites := "on"
	oppEncryption := "on"
	zoneConfig.Spec.SSL = &cloudflarev1alpha1.SSLSettings{
		Mode:                   &sslMode,
		MinTLSVersion:          &minTLS,
		TLS13:                  &tls13,
		AlwaysUseHTTPS:         &alwaysHTTPS,
		AutomaticHTTPSRewrites: &autoRewrites,
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
	if mock.settings["ssl"] != "full" {
		t.Errorf("expected ssl=full, got %v", mock.settings["ssl"])
	}
	if mock.settings["min_tls_version"] != "1.2" {
		t.Errorf("expected min_tls_version=1.2, got %v", mock.settings["min_tls_version"])
	}
	if mock.settings["tls_1_3"] != "zrt" {
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

	// Verify status was updated
	var updated cloudflarev1alpha1.CloudflareZoneConfig
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "test-zone-config", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated zone config: %v", err)
	}

	if updated.Status.AppliedSettings != 6 {
		t.Errorf("expected AppliedSettings=6, got %d", updated.Status.AppliedSettings)
	}
}

func TestZoneConfigReconcile_AppliesAllSettings(t *testing.T) {
	zoneConfig := newTestZoneConfig("test-zone-config", "default")
	zoneConfig.Finalizers = []string{cloudflarev1alpha1.FinalizerName}

	// SSL settings (6)
	sslMode := "full"
	minTLS := "1.2"
	tls13 := "zrt"
	alwaysHTTPS := "on"
	autoRewrites := "on"
	oppEncryption := "on"
	zoneConfig.Spec.SSL = &cloudflarev1alpha1.SSLSettings{
		Mode:                   &sslMode,
		MinTLSVersion:          &minTLS,
		TLS13:                  &tls13,
		AlwaysUseHTTPS:         &alwaysHTTPS,
		AutomaticHTTPSRewrites: &autoRewrites,
		OpportunisticEncryption: &oppEncryption,
	}

	// Security settings (4)
	secLevel := "medium"
	challengeTTL := 1800
	browserCheck := "on"
	emailObfuscation := "on"
	zoneConfig.Spec.Security = &cloudflarev1alpha1.SecuritySettings{
		SecurityLevel:    &secLevel,
		ChallengeTTL:    &challengeTTL,
		BrowserCheck:    &browserCheck,
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

	// Verify status: 23 settings + 1 bot management = 24
	var updated cloudflarev1alpha1.CloudflareZoneConfig
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "test-zone-config", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated zone config: %v", err)
	}

	expectedApplied := 24
	if updated.Status.AppliedSettings != expectedApplied {
		t.Errorf("expected AppliedSettings=%d, got %d", expectedApplied, updated.Status.AppliedSettings)
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

	// Verify status: 1 for bot management
	var updated cloudflarev1alpha1.CloudflareZoneConfig
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "test-zone-config", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated zone config: %v", err)
	}

	if updated.Status.AppliedSettings != 1 {
		t.Errorf("expected AppliedSettings=1, got %d", updated.Status.AppliedSettings)
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
	err = r.Client.Get(context.Background(), types.NamespacedName{Name: "test-zone-config", Namespace: "default"}, &updated)
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
	if err := r.Client.Get(context.Background(), types.NamespacedName{Name: "test-zone-config", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated zone config: %v", err)
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
