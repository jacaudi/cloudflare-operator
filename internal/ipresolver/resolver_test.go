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

package ipresolver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolver_SingleProviderHappyPath(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("192.0.2.42"))
	}))
	defer good.Close()

	r := NewResolver(WithProviders([]string{good.URL}), WithHTTPTimeout(2*time.Second))
	ip, err := r.GetExternalIP(context.Background())
	require.NoError(t, err)
	require.Equal(t, "192.0.2.42", ip)
}

func TestResolver_Cache(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte("192.0.2.1"))
	}))
	defer srv.Close()
	r := NewResolver(WithProviders([]string{srv.URL}), WithCacheTTL(10*time.Second))
	_, _ = r.GetExternalIP(context.Background())
	_, _ = r.GetExternalIP(context.Background())
	require.Equal(t, 1, calls, "cache hit on second call")
}

func TestResolver_AllProvidersFail(t *testing.T) {
	r := NewResolver(WithProviders([]string{"http://127.0.0.1:1"}), WithHTTPTimeout(50*time.Millisecond))
	_, err := r.GetExternalIP(context.Background())
	require.Error(t, err)
}

// TestResolver_RejectsNonIPResponse pairs a provider returning a malformed
// body with a provider returning a valid IP. The malformed response must be
// rejected by the IP-validation branch (not surface as the result), and the
// surviving good vote must win the tally.
func TestResolver_RejectsNonIPResponse(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not-an-ip"))
	}))
	defer bad.Close()

	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("192.0.2.7"))
	}))
	defer good.Close()

	r := NewResolver(
		WithProviders([]string{bad.URL, good.URL}),
		WithCacheTTL(0),
	)
	ip, err := r.GetExternalIP(context.Background())
	require.NoError(t, err)
	require.Equal(t, "192.0.2.7", ip)
}

// TestResolver_ContextCancel covers the context-cancel branch: passing an
// already-canceled context should propagate cancellation rather than spinning
// on the provider fan-out.
func TestResolver_ContextCancel(t *testing.T) {
	// Slow server that would otherwise return a valid IP, so we can be sure
	// the failure comes from the canceled context and not the response.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte("192.0.2.99"))
	}))
	defer srv.Close()

	r := NewResolver(
		WithProviders([]string{srv.URL}),
		WithHTTPTimeout(5*time.Second),
		WithCacheTTL(0),
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled
	start := time.Now()
	_, err := r.GetExternalIP(ctx)
	elapsed := time.Since(start)
	require.ErrorIs(t, err, context.Canceled, "expected wrapped context.Canceled when ctx is canceled")
	require.Less(t, elapsed, time.Second, "should return promptly on canceled context")
}

// TestResolver_OneProviderErrorsOneSucceeds_ReturnsSurvivingVote pairs a
// failing provider (500) with a good provider. Because the lifted
// implementation tallies provider votes rather than short-circuiting on the
// first success, the returned IP must come from the surviving provider when
// all others error.
func TestResolver_OneProviderErrorsOneSucceeds_ReturnsSurvivingVote(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()

	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("192.0.2.55"))
	}))
	defer good.Close()

	r := NewResolver(
		WithProviders([]string{bad.URL, good.URL}),
		WithCacheTTL(0),
	)
	ip, err := r.GetExternalIP(context.Background())
	require.NoError(t, err)
	require.Equal(t, "192.0.2.55", ip)
}
