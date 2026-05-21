# Troubleshooting

A field guide for diagnosing the operator from outside. Pairs with
[`reconciliation.md`](reconciliation.md) (when does the next reconcile
actually fire?) — this page is "the reconcile fired and something is
wrong, now what?".

## Step 0: confirm the operator is alive

```sh
kubectl get pods -A -l app.kubernetes.io/name=cloudflare-operator
```

You're looking for the meta-operator pod plus its sub-operator pods
(zone + tunnel). All should be `Running` with `Ready: True`.

If pods are crash-looping, jump to [logs](#reading-the-operator-logs)
before going further. The rest of this page assumes the operator pods
are running.

## Step 1: read the CR's `Status`

```sh
kubectl get <crd>/<name> -n <ns> -o yaml | yq '.status'
```

The operator publishes everything you need to diagnose into `Status`.
Look at three things, in this order:

### `Status.Phase`

The coarse summary:

| Phase | Meaning |
|---|---|
| `Pending` | Reconcile has never completed. First-time setup or restart. |
| `Reconciling` | Reconcile is in progress. Transitional; should resolve to Ready or Error within an interval. |
| `Ready` | Operator considers the CR converged with Cloudflare. |
| `Error` | Something went wrong. Read `Status.Conditions` for the why. |

### `Status.Conditions[].Ready`

The most important condition. When `Ready=False`, the `reason` and
`message` fields tell you what's blocking convergence.

```sh
kubectl get <crd>/<name> -n <ns> -o jsonpath='{range .status.conditions[?(@.type=="Ready")]}{.reason}{"\t"}{.message}{"\n"}{end}'
```

Reasons are well-defined strings — they're the key to this page.

### `Status.LastSyncedAt` + `Status.LastReconcileToken`

When did the last reconcile finish? If `LastSyncedAt` is more than one
reconcile-interval old, the loop may be stuck. If you annotated with
`cloudflare.io/reconcile-at` and `Status.LastReconcileToken` doesn't
match the annotation, the reconcile hasn't run yet (or is failing
before the ack writes — see [reconciliation.md](reconciliation.md) for
the contract).

## Step 2: match the `Ready=False` reason to a fix

The operator emits a small fixed vocabulary of reasons. Find yours
below; each entry has the symptom, the root cause, and a fix.

### Credential reasons

| Reason | Root cause | Fix |
|---|---|---|
| `CredentialsUnavailable` | The credential resolve returned `ErrSecretNotFound`, `ErrSecretKeyMissing`, or `ErrAccountIDUnset`. | See [`credentials.md`](credentials.md). 9 times out of 10 it's the `app.kubernetes.io/part-of: cloudflare-operator` label missing on the Secret. |
| `CredentialsInsufficient` | Token exists but Cloudflare API returns 401 / 403 — the token doesn't have the required scope. | Re-issue the token with the right zone-level or account-level permissions and update the Secret. |

### Adoption reasons

| Reason | Root cause | Fix |
|---|---|---|
| `AdoptRefusedNoTXT` | `spec.adopt: true` but the live Cloudflare record has no TXT ownership companion. | Walk the migration flow in [`adopting-existing-records.md`](adopting-existing-records.md). |
| `AdoptRefusedForeign` | TXT companion exists but identifies a different CR (or a different operator). | Resolve the conflicting owner first; don't force-adopt. |
| `AdoptedExistingRecord` | Informational success state — adopted cleanly. | No action. |

### Tunnel-specific reasons

| Reason | Root cause | Fix |
|---|---|---|
| `GatewayServiceUnspecified` | An annotated Gateway lacks `cloudflare.io/gateway-service`. The operator can't pick a backend Service without it. | Set the annotation. Format: `<namespace>/<service-name>[:<port>]`. See [`gateway-api.md`](gateway-api.md). |
| `DuplicateHostname` | Two sources (Gateways / Routes / Services) want to publish the same hostname to the same tunnel. | Pick a single owner. Detach the conflicting source or rename. |
| `ControllerOffline` | The cloudflared connector pod isn't running or can't reach Cloudflare's edge. | `kubectl logs -n <ns> -l app.kubernetes.io/name=cloudflared` — usually an egress / network-policy issue. |

### Zone / DNSRecord reasons

| Reason | Root cause | Fix |
|---|---|---|
| `DependencyMissing` | A `zoneRef` points at a `CloudflareZone` CR that doesn't exist, or its `Status.ZoneID` isn't populated yet. | Confirm the referenced zone CR exists and is `Ready`. Check the namespace if you set `zoneRefNamespace`. |
| `DriftDetected` | Operator detected an out-of-band change at Cloudflare (someone edited via the dashboard). Informational; the operator reverts. | If you didn't expect the drift, investigate who changed it. To make a config change permanent, edit the CR — not the dashboard. |
| `OwnershipCompanionFailed` | The paired TXT companion couldn't be written or verified. Common during S1-era TXT-companion-affixname issues. | Usually transient. If persistent, see [`adopting-existing-records.md`](adopting-existing-records.md) + the operator logs. |
| `Ignored` | The CR is in `Mode: Observe` — operator reads but never writes. | If you intended Managed mode, set `spec.mode: Managed`. |

### ZoneConfig / Ruleset reasons

| Reason | Root cause | Fix |
|---|---|---|
| `SettingsApplied` | Informational success — zone-level settings applied cleanly. | No action. |
| `SettingsApplyFailed` | Cloudflare rejected one or more settings. The `message` field carries the specific complaint. | Read the message; fix the offending field; force-reconcile. |
| `PlanTierInsufficient` | A setting requires a Cloudflare plan tier the account doesn't have (e.g. Bot Management on Free). | Upgrade the plan, or drop the offending setting from the CR. |

## Step 3: read the operator logs

When `Status` isn't enough, jump to logs. The operator logs at INFO by
default; the relevant entries carry `controller`, `namespace`, `name`
fields.

```sh
# Meta-operator (the sub-operator manager).
kubectl logs -n cloudflare-system -l app.kubernetes.io/name=cloudflare-operator --tail=100

# Zone sub-operator.
kubectl logs -n cloudflare-system -l app.kubernetes.io/name=cloudflare-zone-controller --tail=100

# Tunnel sub-operator.
kubectl logs -n cloudflare-system -l app.kubernetes.io/name=cloudflare-tunnel-controller --tail=100
```

For a specific CR's reconcile trail:

```sh
kubectl logs -n cloudflare-system -l app.kubernetes.io/name=cloudflare-zone-controller \
  | grep '"name":"<crname>"' | tail -30
```

The structured-log key `commit` carries the operator's git SHA — useful
to confirm which build is actually running.

## Step 4: check Events

The operator emits Kubernetes Events on key transitions (drift
detection, ownership-companion failures, adoption refusals, etc.).
They're scoped to the CR, so:

```sh
kubectl get events -n <ns> --field-selector involvedObject.name=<crname> --sort-by=.lastTimestamp
```

Events complement Status — Status reflects the CURRENT state; Events
are the historical record of what happened to get there. If a CR
flipped between Ready and Error several times, Events will show the
trail; Status will show only the latest.

## Step 5: did the reconcile actually run?

If you changed the CR (spec edit, annotation update, etc.) and the
state isn't moving:

```sh
TOKEN=$(date -u +%Y-%m-%dT%H:%M:%SZ)
kubectl annotate <crd>/<name> -n <ns> cloudflare.io/reconcile-at=$TOKEN --overwrite

# Wait ~10s, then verify the operator observed the annotation.
kubectl get <crd>/<name> -n <ns> -o jsonpath='{"annotation: "}{.metadata.annotations.cloudflare\.io/reconcile-at}{"\nack:        "}{.status.lastReconcileToken}{"\n"}'
```

If `annotation == ack`, the operator saw the change and ran a full
reconcile. The fact that state still hasn't converged means the
reconcile *observed* the same problem and kept the Phase the same —
i.e. the underlying cause hasn't been fixed yet. Loop back to Step 2.

If `annotation != ack` after a minute, the reconcile isn't firing.
Possibilities:

- The operator pod is wedged. Check Step 0 again.
- The operator is busy elsewhere (high reconcile pressure). Check pod
  CPU; consider whether you have many CRs and a slow Cloudflare
  response.
- A pre-prelude crash (rare). Check operator logs for panics.

## Step 6: read the Cloudflare side

When operator-side state looks fine but the record isn't behaving as
expected at Cloudflare:

- **Cloudflare dashboard** — DNS tab, search for the record name.
  Confirm content, proxied status, TTL, and that the companion
  `cf-txt.<hostname>` exists.
- **`dig <hostname>` from outside the cluster.** Cloudflare's
  authoritative NS should answer with the configured content. If
  not, the record isn't actually live — usually a propagation /
  zone-activation issue (a `CloudflareZone` CR in `ZoneActivating`
  state means Cloudflare hasn't yet recognized the zone as
  authoritative).

## Quick-reference cheat sheet

```sh
# 1. Is the operator alive?
kubectl get pods -n cloudflare-system -l app.kubernetes.io/name=cloudflare-operator

# 2. What's the CR's coarse state?
kubectl get <crd> -A   # the PHASE column is what to look at

# 3. What's the specific Ready condition reason?
kubectl get <crd>/<name> -n <ns> \
  -o jsonpath='{range .status.conditions[?(@.type=="Ready")]}{.reason}{"\t"}{.message}{"\n"}{end}'

# 4. Recent operator log for this CR.
kubectl logs -n cloudflare-system -l app.kubernetes.io/name=cloudflare-<bundle>-controller \
  | grep '"name":"<crname>"' | tail -30

# 5. Events for this CR.
kubectl get events -n <ns> --field-selector involvedObject.name=<crname> --sort-by=.lastTimestamp

# 6. Force a reconcile + verify it ran.
TOKEN=$(date -u +%Y-%m-%dT%H:%M:%SZ)
kubectl annotate <crd>/<name> -n <ns> cloudflare.io/reconcile-at=$TOKEN --overwrite
sleep 10
kubectl get <crd>/<name> -n <ns> -o jsonpath='{"ann="}{.metadata.annotations.cloudflare\.io/reconcile-at}{" ack="}{.status.lastReconcileToken}{"\n"}'
```

## When to involve a maintainer

The operator's surface is small enough that most failure modes fit one
of the reasons above. If you hit a `Ready=False` reason that isn't in
this page or a panic in the logs, that's worth filing. Include:

- The CR's full `Status` block.
- Operator log entries for the affected controller (last 100 lines).
- The chart version: `helm get metadata cloudflare-operator -n <ns>`.
- The operator image: `kubectl get deploy -n cloudflare-system <op> -o yaml | grep image:`.

## Related

- [`reconciliation.md`](reconciliation.md) — when the next reconcile fires.
- [`credentials.md`](credentials.md) — credential-failure diagnostics.
- [`adopting-existing-records.md`](adopting-existing-records.md) — adopt refusals.
- [`gateway-api.md`](gateway-api.md) — Gateway-API-specific gotchas.
- [`annotations.md`](annotations.md) — the `cloudflare.io/reconcile-at` deep-dive lives in `reconciliation.md`, but the annotation table is here.
