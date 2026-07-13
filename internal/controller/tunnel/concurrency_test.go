/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package tunnel

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Unit coverage for the issue #134 per-controller concurrency knob; the AddToManager wiring smoke lives in test/envtest/tunnel_concurrency_envtest_test.go.

// TestControllerOptions_Passthrough verifies controllerOptions passes the value straight through: controller-runtime clamps <= 0 to its default of 1, so callers need no special-casing.
func TestControllerOptions_Passthrough(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"zero -> passthrough (CR defaults to 1)", 0, 0},
		{"one -> explicit default", 1, 1},
		{"raised", 4, 4},
		{"high", 64, 64},
		{"negative -> passthrough (CR clamps to 1, no pre-validation)", -1, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := controllerOptions(tc.in)
			require.Equal(t, tc.want, got.MaxConcurrentReconciles)
		})
	}
}

// TestConcurrencyOptions_PerFieldIndependence proves each of the five fields threads to its own controller without cross-talk.
func TestConcurrencyOptions_PerFieldIndependence(t *testing.T) {
	opts := Options{Concurrency: ConcurrencyOptions{
		Tunnel:    2,
		Service:   3,
		Gateway:   4,
		HTTPRoute: 5,
		TLSRoute:  6,
	}}

	require.Equal(t, 2, controllerOptions(opts.Concurrency.Tunnel).MaxConcurrentReconciles)
	require.Equal(t, 3, controllerOptions(opts.Concurrency.Service).MaxConcurrentReconciles)
	require.Equal(t, 4, controllerOptions(opts.Concurrency.Gateway).MaxConcurrentReconciles)
	require.Equal(t, 5, controllerOptions(opts.Concurrency.HTTPRoute).MaxConcurrentReconciles)
	require.Equal(t, 6, controllerOptions(opts.Concurrency.TLSRoute).MaxConcurrentReconciles)
}

// TestApplyOptionDefaults_LeavesConcurrencyZero locks the zero-value contract: a non-zero default here belongs in the chart, not in Go.
func TestApplyOptionDefaults_LeavesConcurrencyZero(t *testing.T) {
	opts := Options{}
	applyOptionDefaults(&opts)
	require.Equal(t, ConcurrencyOptions{}, opts.Concurrency,
		"applyOptionDefaults must leave Concurrency zero so the CR default of 1 is preserved")

	// A caller-supplied value must also survive the defaulting pass unchanged.
	opts2 := Options{Concurrency: ConcurrencyOptions{HTTPRoute: 3}}
	applyOptionDefaults(&opts2)
	require.Equal(t, 3, opts2.Concurrency.HTTPRoute,
		"applyOptionDefaults must not clobber a caller-supplied concurrency value")
	require.Equal(t, ConcurrencyOptions{HTTPRoute: 3}, opts2.Concurrency)

	// Sanity: unrelated defaulting still fires, so this can't pass by applyOptionDefaults becoming a no-op.
	require.Equal(t, int32(2), opts2.DefaultConnector.Replicas)
}
