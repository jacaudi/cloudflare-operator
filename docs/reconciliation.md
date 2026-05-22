# Reconciliation cadence + forcing a re-reconcile

This page explains **why** the operator doesn't auto-retry stuck CRs every few
seconds, **when** the next reconcile actually fires, and **how** to trigger one
immediately when you can't or don't want to wait.

If a CR is sitting in `Phase=Error` and you expect it to come back faster than
the default 30-minute interval — read on.

---

## How a reconcile gets scheduled

The operator is built on [controller-runtime](https://pkg.go.dev/sigs.k8s.io/controller-runtime),
which decides when to call `Reconcile` based on three signals:

1. **The reconciler's own return value.**
   - `Reconcile(...) → (Result{}, err)` where `err != nil` → controller-runtime
     enqueues with **exponential backoff** (5 ms → 1000 s, capped). Fast retry.
   - `Reconcile(...) → (Result{RequeueAfter: D}, nil)` → controller-runtime
     enqueues exactly after `D`. No backoff.
   - `Reconcile(...) → (Result{Requeue: true}, nil)` → immediate re-enqueue.
2. **Event watches.** Each controller declares `.Owns(...)` / `.Watches(...)`
   on related objects. When any of those changes (Service, Gateway,
   HTTPRoute, TLSRoute, the cloudflared Deployment, the connector Secret,
   the tunnel CR itself, …), controller-runtime enqueues the owning CR
   immediately — independent of any time-based interval.
3. **External nudges.** Annotation changes, manual `kubectl annotate`, etc.
   land via the same watch path as item 2.

## Why a CR in `Error` doesn't retry every 5 seconds

**Operational failures are treated as Status data, not as Go errors.**

When the operator hits a Cloudflare API error, a connector spawn failure,
a name conflict, etc., it:

- Sets `Status.Phase = Error`
- Stamps `Ready = False` with a reason and message
- Persists the status via the unified
  [`reconcile.UpdateStatusIfChanged`](../internal/reconcile/status.go) helper
- Returns `(Result{RequeueAfter: <CRD interval>}, nil)` — **a successful
  Go-level return**

So controller-runtime sees "success, requeue in 30 minutes" and waits the
full interval. Exponential backoff doesn't fire because there was no Go
error to back off from.

This is deliberate. Cloudflare's API is rate-limited; hammering it every
few seconds on a sustained outage would create a bigger outage. The
fixed-interval cadence keeps API pressure bounded and predictable.

### Default intervals

| CRD | Default interval | Spec field |
|---|---|---|
| `CloudflareZone` | 30 min | `spec.interval` |
| `CloudflareZoneConfig` | 30 min | `spec.interval` |
| `CloudflareRuleset` | 30 min | `spec.interval` |
| `CloudflareDNSRecord` | 30 min | `spec.interval` |
| `CloudflareTunnel` | 30 min | `spec.interval` |

All five honor `spec.interval` if set. The floor is clamped via
`reconcile.ResolveInterval` (currently 10 s).

## Forcing an immediate reconcile

Three levers, ordered from most to least targeted:

### 1. Annotation: `cloudflare.io/reconcile-at` (recommended)

Slice 6 (Feature F) added an opaque-token annotation that every operator
controller acks. Setting the annotation re-enqueues the CR immediately AND
bypasses the change-detection short-circuit on the next reconcile (for the
CRDs whose reconcilers also use it to bypass change-detection — Zone,
ZoneConfig, Ruleset, Tunnel; DNSRecord acks the token without bypassing).

```sh
# Force a reconcile right now.
TOKEN=$(date -u +%Y-%m-%dT%H:%M:%SZ)
kubectl annotate cloudflaretunnel/<name> -n <ns> \
  "cloudflare.io/reconcile-at=$TOKEN" --overwrite
```

**Verify the loop observed it.** The controller copies the annotation
value into `Status.LastReconcileToken` on every successful reconcile —
if `annotation == status.lastReconcileToken`, you know the reconciler
saw it and ran a full pass.

```sh
kubectl get cloudflaretunnel/<name> -n <ns> \
  -o jsonpath='{"annotation: "}{.metadata.annotations.cloudflare\.io/reconcile-at}{"\nack:        "}{.status.lastReconcileToken}{"\n"}'
# annotation: 2026-05-21T17:52:35Z
# ack:        2026-05-21T17:52:35Z   ← match means the reconciler ran
```

**The token is opaque.** Any value works (RFC 3339 timestamps are convenient
because they're sortable and human-readable, but the operator doesn't
parse it). Re-use the same value to no-op; change it to force again.

**Restart-immune.** The ack is in `Status`, not memory — restarting the
operator does not re-fire previously-acked tokens.

**Works on every CRD.** Zone, ZoneConfig, Ruleset, DNSRecord, Tunnel — all
five carry the prelude.

### 2. Lower `spec.interval` on the CR

If a CR genuinely needs a tighter reconcile cadence than 30 min,
set it explicitly:

```yaml
apiVersion: cloudflare.io/v2alpha1
kind: CloudflareTunnel
metadata:
  name: example
spec:
  interval: 1m   # clamped to the controller's floor
  # ...
```

Use sparingly — most failures don't get fixed faster by polling harder, and
faster polling burns Cloudflare API budget that's better spent elsewhere.

### 3. Touch any watched object

Because controller-runtime enqueues the owner on watched-object changes,
mutating any of the following also triggers a reconcile:

- For a `CloudflareTunnel` — the cloudflared `Deployment`, the connector
  `Secret`, any source `Service` / `Gateway` / `HTTPRoute` / `TLSRoute`
  attached to it.
- For a `CloudflareDNSRecord` — its owning `CloudflareTunnel` (re-emission)
  or `CloudflareZone` (zone-id resolution).

In practice, option 1 (the annotation) is almost always the right choice —
it's targeted, restart-immune, and observable.

## When the annotation **isn't** enough

The annotation triggers a reconcile. It does not change the underlying
cause of an error. If the CR was `Error` because of a wrong credential,
a name conflict at Cloudflare, or a misconfigured source object, the next
reconcile observes the same problem and re-stamps `Phase = Error`.

In that case the next step is the usual:

- `kubectl describe <crd>/<name> -n <ns>` to read the conditions + events.
- `kubectl logs -n <operator-ns> -l <controller-label>` to read the
  operator's reasoning.
- Fix the underlying input, then either annotate or wait for the next
  scheduled reconcile to converge.

## Related

- [`internal/reconcile/status.go::UpdateStatusIfChanged`](../internal/reconcile/status.go) —
  the unified terminal status-write epilogue used by all 5 reconcilers.
  The Feature F ack is stamped here when the annotation differs from the
  prior `Status.LastReconcileToken`.
- [`internal/reconcile/halt.go`](../internal/reconcile/halt.go) —
  `HaltWith` / `HaltDependency` / `HaltCredentialsUnavailable` helpers
  used at short-circuit branches; each sets `Ready=False` + `Phase=Error`
  and returns `(Result{RequeueAfter: D}, nil)` for the no-fast-retry
  semantics described above.
