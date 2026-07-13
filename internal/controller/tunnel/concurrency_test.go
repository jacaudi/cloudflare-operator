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

// Test-category coverage for the issue #134 per-controller concurrency knob
// (a Go-library change: ConcurrencyOptions on tunnel.Options threaded through
// the five setup.go builders). The end-to-end wiring smoke — that all five
// builders accept the threaded controller.Options — lives in the envtest
// integration suite (test/envtest/tunnel_concurrency_envtest_test.go), which
// runs in maintainer CI where the kube binaries are available.
//
//	1. Happy path      — TestControllerOptions_Passthrough (positive value)
//	2. Boundary/default — TestControllerOptions_Passthrough (zero -> 0, which
//	                      controller-runtime normalizes to its default of 1)
//	3. Negative input   — TestControllerOptions_Passthrough (negative -> passed
//	                      through unchanged; controller-runtime clamps <=0 to 1)
//	4. Per-field wiring — TestConcurrencyOptions_PerFieldIndependence
//	5. Regression       — TestApplyOptionDefaults_LeavesConcurrencyZero (a future
//	                      default must not clobber the zero-value passthrough)
//	6. Integration      — envtest AddToManager smoke (see file note above)
//	7. Concurrency      — N/A: this knob *enables* parallel reconciles; the
//	                      reconcilers' own thread-safety under
//	                      MaxConcurrentReconciles > 1 is already guarded by the
//	                      sync.Once lazy-state init in source_base.go and the
//	                      *_source_controller.go files, and exercised by the
//	                      existing tunnel envtest suite. No new race surface is
//	                      introduced by wiring the option through.

// TestControllerOptions_Passthrough verifies that controllerOptions maps a
// per-controller MaxConcurrentReconciles value straight through to
// controller.Options without pre-validation. The zero and negative rows document
// the contract that controller-runtime's controller.New clamps any value <= 0 to
// its default of 1 (controller.go: `if MaxConcurrentReconciles <= 0 { = 1 }`),
// so callers do not need to special-case the default here.
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

// TestConcurrencyOptions_PerFieldIndependence proves the five named fields are
// independently addressable and each threads to its own controller value — the
// reason for a typed struct over a stringly-typed map[string]int (go-standards
// §1). Distinct per-field values must survive the controllerOptions mapping
// without cross-talk.
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

// TestApplyOptionDefaults_LeavesConcurrencyZero is the regression lock for the
// zero-value contract: applyOptionDefaults must NOT populate Concurrency, so an
// unset ConcurrencyOptions stays all-zero and every controller keeps
// controller-runtime's default of 1 (pre-feature behavior). If a future edit
// adds a non-zero concurrency default here, this test fails and forces a
// deliberate decision (see the issue's "default-bump alternative" discussion —
// any such bump belongs in the chart, not in the Go zero-value default).
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

	// Sanity: unrelated scalar defaulting still fires (guards against this test
	// accidentally passing because applyOptionDefaults became a no-op).
	require.Equal(t, int32(2), opts2.DefaultConnector.Replicas)
}
