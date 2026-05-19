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

---

## Renovate tracking

cloudflared image bumps are Renovate-tracked and land as `fix(cloudflared)` conventional commits.
A cloudflared update produces at least a patch release of the operator.
