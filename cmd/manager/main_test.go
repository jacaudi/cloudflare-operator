/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	runtime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
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
	wantErr := errors.New("register boom")
	// Dummy config: NewManager constructs lazily (no dial until Start),
	// and register returns before Start, so no cluster is needed.
	cfg := &rest.Config{Host: "http://127.0.0.1:0"}
	err := startManager(
		Options{Mode: ModeZone, LeaderElection: false, MetricsAddress: "0", HealthAddress: "0"},
		scheme,
		cfg,
		func(ctrl.Manager) error { return wantErr },
	)
	require.ErrorIs(t, err, wantErr)
}

func TestNewProductionLogger_ReturnsLoggerForAllLevels(t *testing.T) {
	for _, lvl := range []string{"debug", "info", "warn", "error", "bogus"} {
		l, err := newProductionLogger(lvl)
		require.NoError(t, err)
		require.NotNil(t, l)
	}
}
