# Source-Controller Orphan DNS Record GC — Design

**Date:** 2026-05-28
**Status:** Approved
**Issue:** [#145](https://github.com/jacaudi/cloudflare-operator/issues/145)

## Problem

Source-controllers (HTTPRoute, Gateway, Service, TLSRoute) emit derived `CloudflareDNSRecord` CRs based on the source's current state (annotations, parent refs, listener hostnames, port mappings). When the source transitions to a state that requests **zero** records — annotations removed, parent ref dropped, all listener hostnames cleared — the previously-emitted CRs are not deleted. They become orphans:

- The orphaned CR continues to reconcile against its own spec (`Ready=True`).
- The CR still holds the live DNS + TXT ownership entries in Cloudflare.
- A new actor (e.g., a user-authored `CloudflareDNSRecord` for the same hostname) cannot create a competing record: its TXT companion write fails with `OwnershipCompanionFailed: foreign`.

The existing `pruneOrphanedDNSRecords` helper handles the in-spec case (CR's hostname dropped while other hostnames remain). The gap is the all-records-gone case: it lives entirely on early-return code paths that exit the reconcile **before** the pruner runs.

## Goal

Close the GC gap by adding `pruneOrphanedDNSRecords(..., desired=nil)` calls at the early-return branches of each source-controller's `Reconcile` that represent **definitive deactivation** (the source's current state says "I want zero records"), without introducing pruning on **transient** branches (waiting on tunnel CNAME, waiting on chain content, ambiguous resolution errors).

## Non-goals

- Refactoring `pruneOrphanedDNSRecords`. Its signature already accepts a `desired` map; passing `nil` reads cleanly as "nothing desired."
- Pruning on the source-deleted (`apierrors.IsNotFound`) branch. OwnerReference cascade handles it.
- New status conditions on the source ("emitted N, pruned M").
- A periodic GC controller. Reactive prune at the source level is sufficient once the deactivation branches are covered.

## Design principle

A branch is **definitive deactivation** if reaching it means the source's current state says "I want zero records" — not "I'd want records but something else isn't ready yet."

| Branch | Classification | Action |
|---|---|---|
| HTTPRoute `parent == nil` | Definitive | Prune |
| TLSRoute `parent == nil` | Definitive | Prune |
| Gateway `len(hostnames) == 0` | Definitive | Prune |
| TLSRoute deferred emission (`chainContent == ""`, `tn.Status.TunnelCNAME == ""`) | Transient | No change |
| Service deferred emission (`tn.Status.TunnelCNAME == ""`) | Transient | No change |
| Gateway `resolveGatewayService(...)` error | Ambiguous (missing annotation vs. transient lookup failure) | No change pending field evidence |
| Source `IsNotFound` | Cascade-handled by OwnerReference | No change |

Pruning at a transient branch would delete and recreate CRs every time the tunnel restarts — worse than the original bug. Pruning at an ambiguous branch risks false-positive deletes; the conservative choice is to defer until field evidence confirms it as deactivation.

## Per-controller call sites

Three new prune calls. One existing pattern reused: `if _, perr := pruneOrphanedDNSRecords(ctx, r.Client, kind, name, ns, nil); perr != nil { logger.Error(perr, "orphan-prune failed during deactivation sweep") }`, placed **after** the existing `r.Cache.Clear(prev, srcKey)` so cache state is consistent first.

| Controller | File | Branch | Kind constant |
|---|---|---|---|
| HTTPRoute | `internal/controller/tunnel/httproute_source_controller.go` | `parent == nil` (~L123–L129) | `"HTTPRoute"` |
| TLSRoute | `internal/controller/tunnel/tlsroute_source_controller.go` | `parent == nil` (~L121–L125) | `"TLSRoute"` |
| Gateway | `internal/controller/tunnel/gateway_source_controller.go` | `len(hostnames) == 0` (~L135–L141) | `"Gateway"` |

**Service:** no new call. The Service reconcile's `desired` set is computed unconditionally from ports/zones; annotation removal naturally yields an empty `desired`, which the **existing** end-of-reconcile prune already handles. The implementation plan adds a verification test (see Tests) to confirm this; if the test fails, add a prune call at the appropriate Service branch.

The helper is in the same package (`internal/controller/tunnel/orphan_prune.go`); no new imports.

## Error handling and observability

- **Log-and-continue.** Match the existing happy-path prune call (`logger.Error(perr, "orphan-prune failed (continuing)")`). The early-return path is already a "nothing meaningful to do" path; bailing on a prune error doesn't help and the controller will retry on the next reconcile.
- **Distinct log message** (`"...during deactivation sweep"`) so log greps can tell the two call sites apart.
- **V(1) log line** listing pruned CR names when `pruned` is non-empty, mirroring the existing call's logging shape.
- No new metrics in this change.

## Tests

One envtest case per controller getting a new prune call, plus the Service verification test. Each follows the same shape: create source → assert derived CR exists → mutate source to deactivation state → assert derived CR is deleted within the `Eventually` timeout.

| Controller | Test scenario | File |
|---|---|---|
| HTTPRoute | Create route with tunnel-targeted parent; remove the parent's tunnel annotation (or drop parentRef); assert CR deleted. | `httproute_source_controller_test.go` |
| TLSRoute | Same shape, TLSRoute kind. | `tlsroute_source_controller_test.go` |
| Gateway | Create Gateway with one hostname listener + cloudflare annotation; remove all listener hostnames; assert CRs deleted. | `gateway_source_controller_test.go` |
| Service (verification) | Create Service with `cloudflare.io/zones`; remove the annotation; assert CRs deleted. **If this fails**, the implementation plan adds a prune call at the right Service branch; if it passes, the test serves as regression coverage for the existing end-of-reconcile prune. | `service_source_controller_test.go` |

**Acceptance:** each test must pass with the new code; each test must fail (orphan persists) without it.

## Standards alignment

- **DRY:** reuses existing `pruneOrphanedDNSRecords`. No new abstraction. Same call shape at every site.
- **KISS:** smallest correct fix. Three lines per call site. No restructuring, no `defer` indirection, no named wrapper helper.
- **12-Factor:** logs as event streams (V(1) on prune, Error on failure); stateless idempotent reconcile; no out-of-band processes.
- **Go standards:** explicit control flow (no `defer` for control-flow-significant work); errors wrapped via `fmt.Errorf` inside the existing helper; log-and-continue matches existing pattern.

## Open questions for implementation plan

1. The Service verification test: which exact Service deactivation branch should it exercise? Confirm by reading Service reconcile end-to-end during implementation.
2. The Gateway `resolveGatewayService` error branch: if the implementation reveals it can be disambiguated cheaply (e.g., explicit "annotation absent" error type), reconsider the design's "no change" stance. Otherwise, defer.

## References

- Issue [#145](https://github.com/jacaudi/cloudflare-operator/issues/145)
- Existing pruner: `internal/controller/tunnel/orphan_prune.go`
- Label scheme: `internal/conventions/labels.go` (`LabelSourceKind`, `LabelSourceName`, `LabelSourceNamespace`)
- Cross-reference: S2/S4 work — `.Owns(&v2alpha1.CloudflareDNSRecord{})` + `pruneOrphanedDNSRecords` for the emitted-CR rename migration.
