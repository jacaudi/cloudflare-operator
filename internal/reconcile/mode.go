/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

package reconcile

import (
	v2alpha1 "github.com/jacaudi/cloudflare-operator/api/v2alpha1"
)

// ShouldMutate reports whether a reconciler should perform write operations
// on Cloudflare for a CR with the given mode. Used by CRDs that expose an
// observe/read-only mode (CloudflareDNSRecord in P5; future CRDs to follow).
//
// String-typed for reusability across CRDs regardless of their Mode enum
// type — the caller passes the raw string from spec.mode and the helper
// compares it against the canonical "Observe" sentinel value (bound to
// v2alpha1.RecordModeObserve so an enum rename breaks the build, not the
// gate). Empty input is treated as the default mutating mode (every CRD's
// Managed-equivalent).
//
// Future CRDs with their own Mode enum must use "Observe" as the read-only
// sentinel value so this helper stays applicable. If a different sentinel
// value is ever needed, add a sibling ShouldMutateWith(mode, sentinel)
// rather than overloading this function.
func ShouldMutate(mode string) bool {
	return mode != string(v2alpha1.RecordModeObserve)
}
