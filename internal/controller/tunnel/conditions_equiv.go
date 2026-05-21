/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

// Package tunnel contains controllers for CloudflareTunnel and its source
// controllers (Gateway, HTTPRoute, TLSRoute, Service).
package tunnel

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// conditionsEquivalent reports whether two []metav1.Condition slices are
// semantically equal for the purpose of suppressing redundant Status.Update
// calls in writeParentStatus.
//
// Two slices are equivalent when:
//   - They contain the same number of conditions.
//   - For each condition in b there is a condition in a with the same Type
//     AND identical Status, Reason, Message, and ObservedGeneration.
//
// LastTransitionTime is intentionally ignored: controller-runtime's
// SetCondition (internal/reconcile/status.go) only updates LastTransitionTime
// when Status changes, so identical Status values will share the same
// LastTransitionTime on successive reconciles. Ignoring it avoids spurious
// inequality when the clock ticks between two identical writes.
//
// This helper is file-local to the tunnel package for Slice 2; Slice 3 (D)
// will replace it with a unified status-write helper consumed by all reconcilers.
func conditionsEquivalent(a, b []metav1.Condition) bool {
	if len(a) != len(b) {
		return false
	}
	// Build a map from Type → Condition for the existing (a) slice.
	am := make(map[string]metav1.Condition, len(a))
	for _, c := range a {
		am[c.Type] = c
	}
	// For each new condition in b, check that a matching entry exists in a
	// with identical semantics.
	for _, c := range b {
		prev, ok := am[c.Type]
		if !ok {
			return false
		}
		if prev.Status != c.Status ||
			prev.Reason != c.Reason ||
			prev.Message != c.Message ||
			prev.ObservedGeneration != c.ObservedGeneration {
			return false
		}
	}
	return true
}
