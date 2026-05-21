# cloudflare-operator Helm Chart

Helm chart for [cloudflare-operator](https://github.com/jacaudi/cloudflare-operator), built on the
[bjw-s common library](https://bjw-s-labs.github.io/helm-charts/) (v5).

---

## cloudflared connector image

`controllers.tunnel.connector.image` sets the cloudflared image seeded into every `CloudflareTunnel`
the operator auto-creates (from Gateway annotations). It is a `v2alpha1.ConnectorImage` shape:

```yaml
controllers:
  tunnel:
    connector:
      image:
        repository: ""   # optional — defaults to docker.io/cloudflare/cloudflared
        tag: ""          # optional — defaults to the operator's compile-time pin
```

**Default: `{}`** (both fields empty) — the operator uses its Renovate-tracked compile-time pin
(the `DefaultCloudflaredImage` constant in `internal/controller/tunnel/dataplane.go`).

**Per-axis override:** fields are independent.

- Setting only `repository` keeps the compile-time pinned tag (repository-only mirror).
- Setting only `tag` keeps the default `docker.io/cloudflare/cloudflared` repository.
- Setting both overrides fully.

**Precedence (most specific wins):**

1. `CloudflareTunnel.spec.connector.image` (per-CR, per-axis)
2. `controllers.tunnel.connector.image` (this chart value, per-axis)
3. Operator compile-time pin

**Scope:** applies only to `CloudflareTunnel` CRs the operator auto-creates. Manually-declared
`CloudflareTunnel` CRs use their own `spec.connector.image`.

**Example — mirror all auto-created tunnels through a private registry:**

```yaml
controllers:
  tunnel:
    connector:
      image:
        repository: <your-registry>/cloudflare/cloudflared
        tag: "2026.5.0"
```

### Docker Hub rate-limit mitigation

cloudflared is published exclusively to Docker Hub — there is no official
`ghcr.io` or `public.ecr.aws` image. To avoid Docker Hub pull-rate limits:

- Set `controllers.tunnel.connector.image.repository` to an ECR pull-through cache, Harbor,
  Artifactory, or other registry mirror. The compile-time pinned tag is inherited automatically
  (repository-only override), so Renovate-tracked bumps continue to work without further config
  changes.
- Alternatively (or additionally), configure image pull credentials via the bjw-s common chart's
  standard image pull-secret values.

---

## Tunnel Gateway apex (`cloudflare.io/gateway-apex`)

`cloudflare.io/gateway-apex` is an annotation set on a tunnel-targeted `Gateway`. It explicitly sets
the public apex hostname that per-route (`HTTPRoute`/`TLSRoute`) chain DNS records CNAME to, and
which the gateway-source publishes as `<apex> CNAME → tunnel CNAME`. The value must be a concrete,
non-wildcard DNS hostname (e.g. `external.example.com`).

### When it is REQUIRED

If a Gateway's listener hostnames are **wildcard-only** (e.g. only `*.example.com`) and
`cloudflare.io/gateway-apex` is not set, the operator **will not publish** per-route chain records.
A wildcard is an invalid CNAME target — Cloudflare rejects it with error 9007 — so a wildcard-only
Gateway cannot back a route chain without an explicit concrete apex.

When this condition is detected:

- The route's status condition is set to **not-ready** with reason **`GatewayApexRequired`**.
- A **Warning Event** is emitted on the route object.
- The operator does not hot-loop; it waits for the annotation to be added.

**Resolution:** add `cloudflare.io/gateway-apex` to the Gateway with a concrete hostname.

### When it is optional

If the Gateway has at least one **concrete (non-wildcard) listener hostname**, the annotation is
optional:

- **Without it:** per-route chain records CNAME **directly to the tunnel CNAME**
  (`<uuid>.cfargotunnel.com`).
- **With it:** per-route chain records CNAME to the override apex instead of to the tunnel CNAME
  directly.

### Behavior change note

> **Breaking change for wildcard-only Gateways**

Previously, per-route chain records CNAMEd to the parent Gateway's *first listener hostname*.

Now:

- **Concrete listener, no override:** per-route chain records CNAME directly to the tunnel CNAME
  (`<uuid>.cfargotunnel.com`), not to the Gateway's first listener hostname.
- **Wildcard-only Gateway, no override:** routes that previously emitted a (broken,
  Cloudflare-9007-rejected) wildcard-targeted record are now **Blocked** with reason
  `GatewayApexRequired` until the annotation is set.

**Action required if you have wildcard-only Gateways:** add `cloudflare.io/gateway-apex` to each
such Gateway or routes will stop publishing chain records.

Operators using concrete-listener Gateways that relied on the old first-hostname behavior should
verify their DNS chain is still correct — CNAMEs now target the tunnel directly rather than the
Gateway listener hostname.

### Invalid value

If `cloudflare.io/gateway-apex` is set but is not a valid, non-wildcard DNS hostname, the value is
ignored:

- A **Warning Event** with reason **`GatewayApexInvalid`** is emitted on the Gateway.
- Behavior falls back as if the annotation were unset (CNAME direct to tunnel for concrete-listener
  Gateways; Blocked with `GatewayApexRequired` for wildcard-only Gateways).

### Example

```yaml
metadata:
  annotations:
    cloudflare.io/tunnel: "true"
    cloudflare.io/tunnel-name: my-tunnel
    cloudflare.io/gateway-service: <namespace>/<service>
    cloudflare.io/gateway-apex: external.example.com
```

---

## Tunnel cascade-GC (orphaned tunnels)

The operator cascade-deletes a `CloudflareTunnel` it owns once its last source detaches: after a
two-tick grace window it removes the `CloudflareTunnel`, its cloudflared `Deployment`, the chain
`CloudflareDNSRecord`s it emitted, and the Cloudflare-side tunnel.

### Behavior change note

> **Pre-P4 / attached tunnels are now cascade-GC-eligible**

Previously, cascade-GC fired **only** for tunnels carrying the `cloudflare.io/auto-created`
annotation. That annotation is stamped only when the operator *creates* a tunnel — so tunnels
created by **pre-P4 operator builds**, or pre-existing tunnels a source **attached to** (rather than
the operator creating), were invisible to cascade-GC and **leaked silently** (orphaned
`CloudflareTunnel` + cloudflared Deployment + DNS records + live Cloudflare tunnel, with no Event,
condition, or cleanup) when their last source was removed.

Now a tunnel is cascade-GC-eligible if it carries the `cloudflare.io/auto-created` annotation
**or** the operator source labels (`cloudflare.io/source-kind`, `cloudflare.io/source-name`,
`cloudflare.io/source-namespace`). The source labels predate the annotation and survive into the
orphan state, so operator-authored tunnels are reliably identified. On its next reconcile such a
tunnel is **self-healed** — the `cloudflare.io/auto-created` annotation is stamped (idempotent) —
and from then on it is greppable and behaves identically to a natively auto-created tunnel.

**Action required if you intentionally retain orphan tunnels:** a `CloudflareTunnel` that carries
operator source labels but that you want to keep after detaching all of its sources will now be
cascade-deleted after the grace window. Remove the `cloudflare.io/source-*` labels (and the
`cloudflare.io/auto-created` annotation, if present) to opt it out of cascade-GC.

### Orphaned but unmanaged

A tunnel that is orphan-shaped (no owner references, no attached sources) but carries **neither**
the `cloudflare.io/auto-created` annotation **nor** operator source labels is treated as
**user-authored**. It is **never** auto-deleted. Instead it is surfaced so the state is not silent:

- A **Warning Event** with reason **`OrphanedUnmanaged`** is emitted (once, on the transition into
  this state — it is not re-emitted on subsequent reconciles).
- The `Ready` condition is set to **`False`** with reason **`OrphanedUnmanaged`** and message
  `orphaned but not operator-managed; operator will not auto-GC it — adopt/label it or delete it
  manually`.

**Resolution:** adopt the tunnel by adding the operator source labels (so it becomes
cascade-GC-managed), or delete it manually.

---

## TXT registry (Bug A + Bug C)

The operator's TXT-ownership registry writes a companion `TXT` record (default prefix
`cf-txt`) alongside each managed record to track ownership. Two correctness fixes:

### Quoted / multi-string TXT content (Bug A)

Cloudflare stores and returns TXT record content in RFC 1035 *presentation form* — one or
more whitespace-separated double-quoted character-strings (e.g. `"foo" "bar"`), with values
longer than 255 bytes automatically split into multiple ≤255-byte strings and embedded
`"`/`\` escaped. Previously the operator compared this wire form directly against the
logical desired content, so every reconcile saw spurious drift: managed TXT records (and the
ownership registry) churned indefinitely (an `UpdateRecord` every pass), and AES-GCM
ownership envelopes exceeding 255 bytes failed to reassemble — breaking ownership
classification.

The operator now canonicalizes TXT content at the single Cloudflare SDK read boundary:
quotes are stripped, multi-string values concatenated, RFC 1035 escapes decoded, and
Cloudflare's >255-byte auto-split transparently reassembled. All downstream logic (drift
comparison, codec decode, ownership verification, status) sees logical content.

#### Behavior change note

> **`Status` now reports logical TXT content**

`CloudflareDNSRecord` status fields that surface TXT content (e.g. `Status.CurrentContent` /
observed TXT) now show the **logical** value, not Cloudflare's quoted/split wire form. This
is intentional and back-compatible — a display/comparison change only: **no CRD spec change,
no data migration**. Pre-fix AES>255 ownership records that previously failed to classify
self-heal on the next reconcile once their content reassembles. The operator's write-side
`EncodeTXT` (see below) handles >255-byte content by splitting client-side, so correctness
does not depend on Cloudflare's server-side auto-split behavior.

### Write-side RFC 1035 encoding (Bug A, part 2)

Before this fix, TXT records whose content contains double-quote (`"`) or backslash (`\`)
characters — including all ownership companion records (the plaintext-JSON payload uses `"` as
structural punctuation), and any user-authored SPF, DKIM, or DMARC records with embedded
quotes — caused the Cloudflare API to reject the write with error 9207 or silently store a
mangled form, churning on every reconcile. The operator now handles this automatically on
every TXT create and update — no operator action or migration is required. Safe for all
existing records.

The read-side fix above resolved churn and AES-GCM reassembly; this is the complementary
write-side fix. The operator emits RFC 1035 presentation form on every TXT write via a
single chokepoint: `wireContent` in `internal/cloudflare/dns.go` calls `EncodeTXT`
for TXT records and passes all other types through unchanged. `EncodeTXT` is the pure
inverse of the read-side `CanonicalizeTXT` applied at `mapRecordResponse`:

- logical content → split into ≤255-byte chunks (RFC 1035 §3.3 character-string limit), escape
  embedded `"` as `\"` and `\` as `\\` within each chunk, wrap each chunk in double-quotes,
  space-join all chunks — producing complete RFC 1035 presentation form client-side
- for content ≤255 bytes this collapses to a single double-quoted string; for longer content
  the operator emits multiple space-separated quoted character-strings directly, without
  relying on Cloudflare's server-side auto-split for correctness

This covers **all** TXT records the operator manages — user-authored SPF, DKIM, DMARC, and
any other content with embedded quotes or backslashes — not only the ownership registry. TXT
content that contains no `"` or `\` is wrapped in quotes and otherwise passes through
byte-for-byte; Cloudflare accepts either a bare or a quoted string. Safe for all existing
records; no migration required.

#### Ownership payload format

The TXT ownership registry stores a versioned compact JSON claim. Quoting the schema's source comment in `internal/cloudflare/txt_registry.go`:

> Field names are compact because Cloudflare TXT records are capped at 1024 bytes; every
> character counts when a record may carry multiple ownership claims. Decoders must reject
> payloads whose V field is not equal to 1.

The fields (`v/k/ns/n/h`) map to:

| Field | JSON key | Meaning |
|-------|----------|---------|
| V     | `v`      | Schema version — only `1` is recognised; decoders reject other values |
| K     | `k`      | Kubernetes resource kind (e.g. `CloudflareDNSRecord`) |
| NS    | `ns`     | Kubernetes namespace of the owning object |
| N     | `n`      | Kubernetes name of the owning object |
| H     | `h`      | Optional SHA-256 content hash of the owned record (`sha256:<hex>`); omitted when not yet computed |

Example plaintext payload (after `CanonicalizeTXT` strips Cloudflare's quotes on read):

```
{"v":1,"k":"CloudflareDNSRecord","ns":"network","n":"my-record","h":"sha256:abcd1234…"}
```

The JSON is compact (no spaces) to minimise byte count. The `v=1` version field enables
forward-compatible schema evolution: a future codec change bumps the version; current
decoders fail-safe by classifying unknown versions as `AdoptRefusedNoTXT` rather than
silently mis-classifying ownership.

### Switching the TXT registry codec (plaintext vs AES)

> **Status: AES codec is code-present but not yet operator-wired (deferred)**
>
> The `aesCodec` (AES-256-GCM, wire format `v1:<base64-nonce>:<base64-ciphertext>`) and the
> `TxtRegistryKeySecretRef` field in `CloudflareCredentialRef` exist in the operator code and
> CRD schema. However, the DNSRecord reconciler (`internal/controller/zone/dnsrecord_controller.go`)
> currently passes a hardcoded `nil` key reference to `loadCodec`, so the plaintext codec is
> always used regardless of any `txtRegistryKeySecretRef` value set in a CR. The AES switch
> is explicitly deferred (deferred to a future release).

Until AES is wired, `txtRegistryKeySecretRef` is ignored — setting it has no effect, the plaintext codec is always used, and ownership metadata remains human-readable in public DNS.

**What the two codecs store:**

- **Plaintext** (default): ownership payloads are stored as bare JSON in public DNS
  (e.g. `{"v":1,"k":"CloudflareDNSRecord","ns":"network","n":"my-record"}`). The Kubernetes
  namespace and object name are visible to anyone who can query the DNS zone. The JSON
  contains double-quote characters, so `wireContent`/`EncodeTXT` wraps them in RFC 1035
  presentation form on write; `CanonicalizeTXT` strips the quotes on read.
- **AES-256-GCM** (opt-in, not yet wired): payloads are encrypted with a 32-byte key and
  stored as `v1:<base64-nonce>:<base64-ciphertext>`. The ownership claim is opaque in DNS.
  The `v1:` envelope contains no double-quote characters, so the RFC 1035 encoding wraps it
  in quotes on write and strips them on read — losslessly, as with any other content.

**Migration semantics (once the AES switch is wired):**

The read side uses `autoDetectingCodec`, which sniffs the `v1:` prefix: records written by
the AES codec decode with AES; records written by the plaintext codec decode with the
plaintext decoder. Plaintext and AES companions coexist in DNS without a flag-day. When
`TxtRegistryKeySecretRef` is configured and the reconciler starts using `loadCodec` to select
the AES encoder, each managed record's companion is re-encoded to AES on its next reconcile
(write) cycle — a rolling switch with no operator downtime.

**Caveats (once available):**

- The AES key is cluster-wide (one key per operator deployment, not per-zone or per-record).
- Key loss or rotation without re-encrypting existing companions renders those records
  undecodable: the operator classifies them as `TxtOwnershipUnrecognized` and treats them as
  unowned. Rotate keys by writing new companions with the new key before retiring the old one.

### Wildcard companion naming (Bug C)

`AffixName` derives the ownership-companion record name from the managed record's name. For a
wildcard record (a `*` label, e.g. `*.example.com`) it previously produced a companion
containing a literal asterisk (`cf-txt-*-example.com`) — a malformed name Cloudflare warns
about. The `*` label is now mapped to the asterisk-free sentinel `_wildcard`, so
`*.example.com` yields `cf-txt-_wildcard-example.com` and a bare `*` yields
`cf-txt._wildcard`. Non-wildcard names are byte-identical to before.

#### Migration

The companion name changes **only for wildcard records**; non-wildcard records are
unaffected (identical companion name — no migration).

- **`Managed` wildcard records:** on the next reconcile the operator creates the new
  `cf-txt-_wildcard-…` companion. Any pre-existing legacy literal-`*` companion
  (`cf-txt-*-…`) written by an older operator is **orphaned** — the operator does not touch
  it. Delete it manually once the new companion exists.
- **`Adopt` wildcard records:** follow the existing TXT-registry adopt/migration procedure
  for the new `_wildcard` companion name.

**Accepted limitation:** the `_wildcard` sentinel collides with a record literally named
`_wildcard.<zone>`. This is a documented, accepted limitation: no legitimate hostname has a
label equal to `*`, and a real `_wildcard` label is rare. Avoid managing a literal
`_wildcard.<zone>` record alongside a `*.<zone>` wildcard in the same zone with the same
companion prefix.

### TXT ownership companion — self-heal & name scheme (S1, 2026-05)

The TXT ownership companion record name scheme changed so the companion is
always a real subdomain of the same zone as the record it protects
(`cf-txt.<hostname>`, e.g. `cf-txt.external.example.com`). The previous scheme
could produce a name that did not end in the zone, which Cloudflare stored
zone-appended and the operator could then never find — causing a permanent
"identical record already exists" (API 81058) reconcile loop.

On upgrade the operator self-heals forward (it creates the correctly-named
companion) and **best-effort deletes the stale legacy-named companion only
when it can prove that companion is its own** (the decoded ownership payload
matches the record's identity) and only on records that reference their zone
via `zoneRef`. Records that reference the zone by literal `zoneID` keep the
old orphaned companion (harmless DNS clutter); delete it manually if desired.

A record whose primary DNS is healthy but whose ownership companion cannot be
reconciled (foreign owner, undecodable, or a Cloudflare write error) now
reports `Ready=False` with reason `OwnershipCompanionFailed` and a Warning
event, instead of misreporting `Ready=True "DNS record synced"`.

### Out-of-band emitted-CR self-heal (S2, 2026-05)

If you delete an operator-emitted `CloudflareDNSRecord` CR out-of-band
(e.g. `kubectl delete`), the owning tunnel source controller now notices
the deletion (via a child watch on `CloudflareDNSRecord`) and re-emits the
CR on the next reconcile. Previously this required either editing the
source object or restarting the controller. There is a brief window during
the delete cycle where the underlying Cloudflare record is also removed by
the DNSRecord controller's finalizer path; self-heal restores both within
one reconcile.

### Tunnel-emitted records default `proxied=true` (S3 / #4, 2026-05)

Tunnel-emitted `CloudflareDNSRecord` CRs (generated from annotated Service /
Gateway / HTTPRoute / TLSRoute sources) now default to **proxied = true**
(orange-clouded) on Cloudflare. Previously `Spec.Proxied` was left `nil` and
Cloudflare's per-zone default applied; manual dashboard toggles persisted.
Cloudflare-Tunnel `<uuid>.cfargotunnel.com` targets generally need to be
proxied to route, so this matches the common-case expectation.

**On upgrade:** existing tunnel-emitted records flip grey → orange on first
reconcile after upgrade. The DNSRecord controller's drift check is now
active for these records and will revert manual Cloudflare-dashboard
toggles — the annotation is the control surface, not the dashboard.

**Per-record override:** set `cloudflare.io/proxied: "false"` on the source
object (Service / Gateway / HTTPRoute / TLSRoute) to produce a grey-clouded
record.

The new `cloudflare.io/ttl` annotation (accepts an integer) propagates to
`Spec.TTL`. Absent or malformed values leave `Spec.TTL=0` (Cloudflare
interprets 0 as automatic).

### Tunnel-emitted CR names drop the redundant source-name segment (S4 / #6, 2026-05)

Operator-emitted `CloudflareDNSRecord` CRs (one per public hostname routed
through a Cloudflare Tunnel) are now named **`<sanitized-hostname>-<8hex>`**
instead of the legacy **`<source-name>-<sanitized-hostname>-<8hex>`** doubled
form. Two sources emitting the same hostname converge to a single CR — correct,
since DNS is per-hostname.

**On upgrade:** every existing operator-emitted CR is renamed on the first
reconcile after upgrade. The source controllers emit the new-form CR via
SSA; the existing `pruneOrphanedDNSRecords` helper deletes the legacy
doubled-name CR in the same Reconcile pass (gated by the three
`cloudflare.io/source-{kind,name,namespace}` labels — user-authored CRs
without those labels are NEVER touched).

**Brief one-time DNS flap per record:** the legacy CR's finalizer deletes
the Cloudflare record before the new CR's reconcile creates it back. S1's
81053-as-relist-verify path absorbs the unavoidable overlap as a self-
resolving transient (`Ready=False` briefly), not a stuck error. No manual
migration steps are required.

**Effect on observability:** `kubectl get cloudflarednsrecord` listings are
significantly less wordy; CRs like
`jellyfin-jellyfin-example-com-jellyfin-example-com-<hash>` collapse to
`jellyfin-example-com-<hash>`.

### Tunnel `originRequest` plumbing — controller-managed (OR, 2026-05)

The tunnel controller now manages the cloudflared `originRequest` block on
every synthesized ingress entry. Two fields are plumbed end-to-end:
`originServerName` (the expected SAN on the origin certificate) and
`noTLSVerify` (disable origin TLS verification).

**Configuration surfaces.** Per ingress entry, the operator resolves
`originRequest` in this order (first match wins per field):

1. Route annotation on `HTTPRoute` / `TLSRoute` — `cloudflare.io/origin-server-name` / `cloudflare.io/no-tls-verify`.
2. Gateway annotation (same keys) on the parent `Gateway`.
3. `CloudflareTunnel.Spec.Routing.OriginRequest.{originServerName,noTLSVerify}` (tunnel-level default).
4. Unset → no `originRequest` block on that entry.

For Service-sourced entries the precedence collapses to **service annotation > Spec default > unset** (no Gateway parent).

**Status reflection.** `tn.Status.observedIngress[i].originRequest` now
mirrors what was last PUT to Cloudflare, using the same conditional
projection rules as the read-from-Cloudflare path (`noTLSVerify` projected
only when `true`; `originServerName` only when non-empty). Drift detection
and PUT-skip become `originRequest`-aware automatically.

**Breaking (v2alpha1).** `TunnelOriginRequest.caPool` and
`TunnelOriginRequest.connectTimeoutSeconds` are removed. Internal types
`cloudflare.IngressOriginRequest.CAPool` / `.ConnectTimeoutSeconds` and
`tunnelsynth.IngressContribution.CAPoolPath` are removed. The cloudflared
remote-config API still accepts these fields; the operator simply does not
project them in either direction. Setting them in a CR Spec is silently
dropped on the next write.

**Migration — adopted tunnels with ghost `originRequest`.** A tunnel
adopted via `cloudflare.io/adopt: true` may have an `originRequest` block on
Cloudflare that has no matching source annotation or Spec default. The
operator's behavior is two-phase:

1. **On every reconcile until the next forced re-PUT** — a `DriftDetected`
   Warning event fires (live config differs from `observedIngress`).
2. **On the next reconcile that forces a re-PUT** (hostname/path/service
   change, annotation flip, etc.) — the operator PUTs without
   `originRequest`, Cloudflare clears the field, and an
   `OriginRequestWiped` Warning event names the affected hostname.

To preserve a ghost value, set `cloudflare.io/origin-server-name` on the
source (Gateway / Route / Service) or set
`Spec.Routing.OriginRequest.originServerName` on the CR before the next
forced re-PUT. Same for `cloudflare.io/no-tls-verify`. Detector:

```bash
kubectl get cloudflaretunnel -A -o json | jq -r '
  .items[] |
  select(.status.observedIngress // [] | length > 0) |
  "\(.metadata.namespace)/\(.metadata.name): check Cloudflare dashboard for originRequest values that may need to be expressed via Spec.Routing.OriginRequest or source annotations"'
```

### Auto-created tunnel CRs drop the `cf-` prefix (S5 / #5, 2026-05)

Operator-auto-created `CloudflareTunnel` CRs (per-namespace pool and named
tunnels) are now derived as `<namespace>[-<tunnel-name>]` instead of the
legacy `cf-<namespace>[-<tunnel-name>]`. The companion `cloudflared-<name>`
Deployment and `cloudflared-token-<name>` Secret pick up the new shape
automatically.

**On upgrade:** the existing P4 cascade-GC machinery migrates each
auto-created tunnel without any new operator-side migration code:

1. Each source object reconciles after upgrade.
2. The source reattaches to the new-shape tunnel (creating it if absent —
   new Cloudflare tunnel UUID, new `cloudflared-<newname>` Deployment, new
   `cloudflared-token-<newname>` Secret).
3. The old `cf-<namespace>` tunnel loses all attached sources, enters the
   P4 orphan-state grace window, and self-deletes.
4. Kubernetes cascade-deletes the old `cloudflared-cf-<namespace>`
   Deployment + token Secret via the tunnel CR's owner-refs.

**Brief one-time connector flap during cutover:** the new connector
registers with Cloudflare before the old one disconnects; most chains
stay routed. Total window is bounded by the operator's
`PendingDeletionGrace` (production default: 60 seconds; envtest: 3 seconds).

**Direct-create (user-authored) tunnel CRs are NEVER renamed or GC'd.**
The cascade-GC eligibility predicate (`cascadeGCEligible`) gates on the
`cloudflare.io/auto-created: "true"` annotation OR the operator source
labels — direct-create CRs carry neither and are invisible to the
migration.

### Force-reconcile annotation + structured error-class signal (S6 / #2 + #7, 2026-05)

#### Force-reconcile (`cloudflare.io/reconcile-at`)

Setting (or changing) the `cloudflare.io/reconcile-at` annotation on any
of the 5 operator CRDs (`CloudflareZone`, `CloudflareZoneConfig`,
`CloudflareDNSRecord`, `CloudflareRuleset`, `CloudflareTunnel`) forces a
**full re-check** on the next reconcile of that CR — bypassing the
controller's change-detection / no-drift short-circuit so the operator
will read the live Cloudflare state and re-apply the spec.

The value is **opaque** — the operator never parses it as a time. Any
change triggers exactly one full re-check. Common admin choices:

```yaml
metadata:
  annotations:
    cloudflare.io/reconcile-at: "2026-05-20T12:34:56Z"   # RFC3339 timestamp
    # — or —
    cloudflare.io/reconcile-at: "manual-1"                # bump on demand
```

The controller persists an ack in `status.lastReconcileToken`. As long as
the annotation value matches the ack, no force fires. **Controller
restart is not a re-trigger**: the ack lives in status, so only an
admin-driven *change* to the annotation re-arms the force.

**Source-object annotations:** the source controllers (Service / Gateway
/ HTTPRoute / TLSRoute) do NOT consume `cloudflare.io/reconcile-at` — they
are passive readers of K8s objects, not CRDs. To force-reconcile a
tunnel, set the annotation on the `CloudflareTunnel` CR itself.

#### Structured error-class signal

A shared helper (`internal/reconcile.ErrorClass`) classifies operator
errors into a stable set of strings for use in Warning Event reasons,
status condition reasons, and (future) metrics:

- `name-miss` — expected record not found in Cloudflare (list returned empty).
- `foreign` — record exists but is owned by something else (ownership-companion identity mismatch).
- `undecodable` — record exists but the ownership companion fails to decode.
- `cf-api-<code>` — Cloudflare API rejected the operation (e.g. `cf-api-81058`, `cf-api-9207`).
- `unknown` — fallthrough for errors that don't match the above.

This lets dashboards and alerting route on the underlying CAUSE instead
of grepping log lines. Existing S1 reasons (`OwnershipCompanionFailed`,
`AdoptRefusedForeign`, `DriftDetected`) are unchanged; the class is an
additional, machine-grep-able field. Consumer wiring lands incrementally
in later slices — the helper is in place today and is safe to adopt.

### Legacy-companion GC one-shot ack + tunnel→route watch fix (simplify slice 1, 2026-05)

#### `Status.LegacyCompanionGCDone` (B)

`CloudflareDNSRecord` records now carry a `status.legacyCompanionGCDone`
boolean ack for the pre-S1 legacy-name companion GC sweep. On first
successful reconcile the operator runs the sweep (deletes any legacy
companions found), sets the ack to `true`, and skips the sweep on
every subsequent reconcile. Pre-existing CRs migrate automatically on
their next reconcile pass; users do not need to do anything.

Behavior change: zone-API call volume drops by 2 List calls per
`CloudflareDNSRecord` per reconcile (zero CF API calls post-ack). At
N=200 records / 5-min interval this saves ~4,800 CF List calls per
hour.

#### Tunnel→Route watch fix (A)

A previously-silent bug in `tunnelToHTTPRoutes` / `tunnelToTLSRoutes`
filtered routes by a `cloudflare.io/tunnel=true` annotation that lives
on Gateways (not on Routes), so the watch was a no-op. After this
slice, Routes attached to a tunnel re-reconcile within seconds of the
tunnel's `Status.TunnelCNAME` populating. No user-visible action
required — first-time tunnel setup latency improves.

### Cost reductions (simplify slice 2 / C, E, F, G, H, 2026-05)

#### Secret cache now label-scoped (C) — MIGRATION REQUIRED

The operator now scopes its Kubernetes Secret cache to objects
carrying the `app.kubernetes.io/part-of: cloudflare-operator`
label. Operator-managed Secrets (e.g. the cloudflared connector
token Secret built by the tunnel controller) carry this label
automatically.

**User-credential Secrets MUST also carry this label going
forward.** For existing deploys, label each Cloudflare-credentials
Secret the operator should be able to read:

```
kubectl label secret -n <ns> <name> \
  app.kubernetes.io/part-of=cloudflare-operator
```

Unlabeled credential Secrets become invisible to the operator's
cache and credential resolution will fail with `ErrSecretNotFound`.

#### Cloudflare client connection reuse (E)

Internal change: the operator now reuses `*cfgo.Client` instances
(and their underlying HTTP/2 connection pools) across reconciles
for the same `(token, accountID)` pair. 32-entry LRU with a
30-minute absolute TTL (`golang-lru/v2/expirable.Get` does not
refresh the entry on lookup, so even an actively-used client is
rebuilt at most every 30 minutes — capping any stale-state
exposure and ensuring credential rotation propagates promptly).
No user-visible change.

#### Apiserver write-amplification fix (F)

`HTTPRoute` and `TLSRoute` source controllers now skip their
`Status().Update` when no condition has changed (semantic compare
ignoring `LastTransitionTime`). No user-visible change beyond
reduced apiserver round-trips per reconcile.

#### Tunnel drift detection slightly less eager (G)

The Cloudflare `GetConfiguration` call inside `applyRemoteConfig`
is now skipped when the operator's desired snapshot matches the
last-observed remote ingress snapshot. Out-of-band edits to the
remote tunnel config take one full reconcile interval longer to
surface as `DriftDetected` Events — for typical deploys the
30-minute default interval. Set the `cloudflare.io/reconcile-at`
annotation (introduced in S6) to force an immediate re-check.

#### Tunnel dataplane patches hash-gated (H)

The cloudflared Deployment and metrics Service are no longer
re-applied via SSA on every tunnel reconcile when nothing has
changed. Hash-gating uses two new optional Status fields:
`observedDataplaneDeploymentHash` and `observedDataplaneServiceHash`.
No user-visible change. Matches the spec-hash short-circuit pattern
that `CloudflareZoneConfig` already uses.

---

## Renovate tracking

cloudflared image bumps are Renovate-tracked and land as `fix(cloudflared)` conventional commits.
A cloudflared update produces at least a patch release of the operator.
