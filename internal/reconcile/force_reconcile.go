/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/

// Package reconcile force-reconcile prelude (Feature F / backlog #2,
// 2026-05-20). The pattern is uniform across all 5 CRD controllers
// (Zone, ZoneConfig, DNSRecord, Ruleset, Tunnel):
//
//  1. Read the source object's cloudflare.io/reconcile-at annotation
//     (opaque string; the operator never parses it as a time).
//  2. Compare against the controller-owned ack stored in
//     <Type>Status.LastReconcileToken.
//  3. If they differ (or if the ack is empty and the annotation is
//     non-empty), this reconcile is FORCED — controllers MUST bypass
//     any change-detection / no-drift short-circuit and perform a
//     full re-check against the upstream (Cloudflare) state.
//  4. On a successful reconcile, the controller writes the current
//     annotation token to status.LastReconcileToken (idempotent ack).
//     The annotation itself is NEVER modified by the operator.
//  5. Controller restart does NOT re-trigger: the ack is persisted in
//     status; only a TOKEN CHANGE triggers a force.
//
// The helper is intentionally schema-free (raw strings) — each
// controller's typed Status has its own LastReconcileToken field. The
// caller threads in annotation and ack values; this package never
// touches the typed objects directly.

package reconcile

// ForceReconcileRequested reports whether the controller should force a
// full re-check this reconcile by comparing the current
// cloudflare.io/reconcile-at annotation token against the persisted ack.
//
// Returns true when the annotation is non-empty AND differs from the ack
// (covers both "first-ever set" — ack is "" — and "admin changed the
// token"). Returns false when the annotation is empty (no force
// requested) or when annotation == ack (already acked, no re-trigger).
//
// The annotation value is opaque: callers MUST NOT parse it. Common
// admin choices include RFC3339 timestamps (`2026-05-20T12:00:00Z`),
// UUIDs, or short tokens. The operator's contract is "any change in
// this string forces a full re-check exactly once."
func ForceReconcileRequested(annotationToken, lastAck string) bool {
	if annotationToken == "" {
		return false
	}
	return annotationToken != lastAck
}
