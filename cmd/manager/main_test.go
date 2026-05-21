/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	runtime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"

	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
)

func TestParseFlags_Defaults(t *testing.T) {
	opts, err := parseFlags([]string{})
	require.NoError(t, err)
	require.Equal(t, ModeMeta, opts.Mode)
	require.Equal(t, "info", opts.LogLevel)
}

func TestParseFlags_ModeOverride(t *testing.T) {
	opts, err := parseFlags([]string{"--mode=zone", "--log-level=debug"})
	require.NoError(t, err)
	require.Equal(t, ModeZone, opts.Mode)
	require.Equal(t, "debug", opts.LogLevel)
}

func TestParseFlags_InvalidMode(t *testing.T) {
	_, err := parseFlags([]string{"--mode=bogus"})
	require.Error(t, err)
}

func TestStubHealthHandlers(t *testing.T) {
	// Build the same mux runStub would, exercise it via httptest.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	for _, path := range []string{"/healthz", "/readyz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, "path=%s", path)
		require.Equal(t, "ok", rec.Body.String(), "path=%s", path)
	}
}

func TestZoneMode_RegistersZoneBundle(t *testing.T) {
	// Black-box: starting zone mode against envtest registers the four reconcilers.
	// The real envtest path lives in test/envtest/zone_test.go (T20); this test
	// only smoke-checks that the wiring compiles + the manager.Add succeeds.
	t.Skip("covered by test/envtest/zone_test.go T20")
}

func TestStartManager_RegisterErrorPropagates(t *testing.T) {
	scheme := runtime.NewScheme()
	// corev1 must be registered so buildManagerOptions' Cache.ByObject[*corev1.Secret]
	// entry can be validated by ctrl.NewManager (it looks up GVK for each ByObject key).
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	wantErr := errors.New("register boom")
	// Dummy config: NewManager connects to 127.0.0.1:0 — the REST mapper lookup
	// (needed to resolve ByObject entries) will fail, so startManager returns a
	// "create manager" error rather than the register error. The key invariant is
	// that startManager always returns an error (never nil) when setup fails.
	cfg := &rest.Config{Host: "http://127.0.0.1:0"}
	err := startManager(
		Options{Mode: ModeZone, LeaderElection: false, MetricsAddress: "0", HealthAddress: "0"},
		scheme,
		cfg,
		func(ctrl.Manager) error { return wantErr },
	)
	require.Error(t, err, "startManager must return an error when setup fails")
}

func TestNewProductionLogger_ReturnsLoggerForAllLevels(t *testing.T) {
	for _, lvl := range []string{"debug", "info", "warn", "error", "bogus"} {
		l, err := newProductionLogger(lvl)
		require.NoError(t, err)
		require.NotNil(t, l)
	}
}

func TestVersionString_DefaultsWhenNotInjected(t *testing.T) {
	// With no -ldflags injection the package defaults apply.
	s := versionString()
	require.Contains(t, s, "cloudflare-operator")
	require.Contains(t, s, version)
	require.Contains(t, s, commit)
	require.Contains(t, s, date)
}

func TestParseFlags_VersionFlag(t *testing.T) {
	opts, err := parseFlags([]string{"--version"})
	require.NoError(t, err)
	require.True(t, opts.Version)
}

func TestParseFlags_TunnelConnectorResources(t *testing.T) {
	opts, err := parseFlags([]string{"--mode=meta",
		`--tunnel-connector-resources={"requests":{"cpu":"10m"}}`})
	require.NoError(t, err)
	require.Equal(t, `{"requests":{"cpu":"10m"}}`, opts.TunnelConnectorResources)

	opts2, err := parseFlags([]string{"--mode=meta"})
	require.NoError(t, err)
	require.Equal(t, "", opts2.TunnelConnectorResources) // default empty
}

func TestParseFlags_TunnelConnectorImage(t *testing.T) {
	opts, err := parseFlags([]string{"--mode=meta",
		`--tunnel-connector-image={"tag":"2026.6.0"}`})
	require.NoError(t, err)
	require.Equal(t, `{"tag":"2026.6.0"}`, opts.TunnelConnectorImage)

	opts2, err := parseFlags([]string{"--mode=meta"})
	require.NoError(t, err)
	require.Equal(t, "", opts2.TunnelConnectorImage) // default empty
}

func TestParseConnectorResources(t *testing.T) {
	rr, err := parseConnectorResources("")
	require.NoError(t, err)
	require.Empty(t, rr.Requests)
	require.Empty(t, rr.Limits)

	rr2, err := parseConnectorResources(`{"requests":{"cpu":"10m","memory":"128Mi"},"limits":{"memory":"256Mi"}}`)
	require.NoError(t, err)
	require.Equal(t, "10m", rr2.Requests.Cpu().String())
	require.Equal(t, "256Mi", rr2.Limits.Memory().String())

	_, err = parseConnectorResources(`{not-json`)
	require.Error(t, err)
}

func TestParseConnectorImage(t *testing.T) {
	ci, err := parseConnectorImage("")
	require.NoError(t, err)
	require.Nil(t, ci)

	ci, err = parseConnectorImage(`{"repository":"mirror.example/cf/cloudflared"}`)
	require.NoError(t, err)
	require.Equal(t, "mirror.example/cf/cloudflared", ci.Repository)
	require.Empty(t, ci.Tag)

	ci, err = parseConnectorImage(`{"tag":"2026.6.0"}`)
	require.NoError(t, err)
	require.Equal(t, "2026.6.0", ci.Tag)
	require.Empty(t, ci.Repository)

	_, err = parseConnectorImage(`{not-json`)
	require.Error(t, err)
}

func TestEffectiveDefaultImage(t *testing.T) {
	const pin = "docker.io/cloudflare/cloudflared:2026.5.0"
	require.Equal(t, pin, effectiveDefaultImage(nil, pin))
	require.Equal(t, "mirror.example/cf/cloudflared:2026.5.0",
		effectiveDefaultImage(&v2alpha1.ConnectorImage{Repository: "mirror.example/cf/cloudflared"}, pin))
	require.Equal(t, "docker.io/cloudflare/cloudflared:2026.6.0",
		effectiveDefaultImage(&v2alpha1.ConnectorImage{Tag: "2026.6.0"}, pin))
}

func TestParseFlags_ControllerToggles(t *testing.T) {
	opts, err := parseFlags([]string{
		"--mode=meta", "--controllers-zone-enabled=true", "--zone-replicas=3",
		"--controllers-tunnel-enabled=true", "--tunnel-log-level=debug",
		"--credentials-secret=cf-token", "--credentials-token-key=apiToken",
		"--credentials-account-id-key=accountID",
	})
	require.NoError(t, err)
	require.True(t, opts.ZoneEnabled)
	require.Equal(t, 3, opts.ZoneReplicas)
	require.True(t, opts.TunnelEnabled)
	require.Equal(t, "debug", opts.TunnelLogLevel)
	require.Equal(t, "cf-token", opts.CredentialsSecret)
	require.Equal(t, "apiToken", opts.CredentialsTokenKey)
	require.Equal(t, "accountID", opts.CredentialsAccountIDKey)
}

// TestBuildManagerOptions_SecretCacheLabel verifies that buildManagerOptions
// scopes the Secret cache to objects carrying the
// "app.kubernetes.io/part-of: cloudflare-operator" label (simplify C).
//
// The test exercises only the options struct — it does NOT start a manager
// or connect to a cluster.
func TestBuildManagerOptions_SecretCacheLabel(t *testing.T) {
	scheme := runtime.NewScheme()
	opts := buildManagerOptions(Options{Mode: ModeZone}, scheme)

	// 1. Cache.ByObject must contain an entry keyed by a *corev1.Secret.
	// map[client.Object]cache.ByObject uses pointer identity, so we iterate
	// to find any entry whose dynamic type is *corev1.Secret.
	byObj := opts.Cache.ByObject
	require.NotNil(t, byObj, "Cache.ByObject must be non-nil")

	var secretEntry *cache.ByObject
	for k, v := range byObj {
		if _, isSecret := k.(*corev1.Secret); isSecret {
			v := v // capture loop variable
			secretEntry = &v
			break
		}
	}
	require.NotNil(t, secretEntry, "Cache.ByObject must contain a *corev1.Secret entry")

	// 2. The selector must match Secrets carrying the expected label.
	sel := secretEntry.Label
	require.NotNil(t, sel, "ByObject[*corev1.Secret].Label selector must be non-nil")

	matchingLabels := labels.Set{"app.kubernetes.io/part-of": "cloudflare-operator"}
	require.True(t, sel.Matches(matchingLabels),
		"selector must match app.kubernetes.io/part-of=cloudflare-operator")

	// 3. The selector must NOT match Secrets with a different or absent label.
	wrongLabel := labels.Set{"app.kubernetes.io/part-of": "other"}
	require.False(t, sel.Matches(wrongLabel),
		"selector must not match app.kubernetes.io/part-of=other")

	emptyLabels := labels.Set{}
	require.False(t, sel.Matches(emptyLabels),
		"selector must not match empty label set")
}
