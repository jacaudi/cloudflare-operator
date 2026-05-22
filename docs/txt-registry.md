# TXT companion registry

Every DNS record the operator manages has a paired **TXT companion**
in the same zone, carrying a small ownership payload. The companion is
the operator's mechanism for distinguishing records it owns from
records owned by humans, other operators, or older versions of itself.

This page explains the companion's name, content format, encryption
options, threat model, and how to inspect one from the outside.

If you're trying to **adopt** an existing Cloudflare record, this
page's "I cannot accept the brief-outage migration path" workaround
lives at the bottom — but [`adopting-existing-records.md`](adopting-existing-records.md)
is the right starting point for the recommended adopt flow.

## What is a companion?

For every `CloudflareDNSRecord` the operator manages (primary record
`app.example.com → origin.example.net`), the operator writes a
sibling DNS record:

| Field | Value |
|---|---|
| Type | `TXT` |
| Name | `cf-txt.<sanitized-hostname>` (e.g. `cf-txt.app.example.com`) |
| Content | A JSON payload (or AES-GCM-encrypted JSON) identifying the owner |

The companion is **read on every reconcile** to verify ownership.
Write semantics (when the operator creates/updates the companion) and
read semantics (what the operator does when the companion is present
or absent) are the two halves of the protocol; both must agree.

## Companion naming: `cf-txt.<hostname>`

The name is derived by prepending `cf-txt.` to the primary record's
hostname. The prefix becomes a fresh leftmost DNS label:

```
primary:    app.example.com          →  companion: cf-txt.app.example.com
primary:    api.app.example.com      →  companion: cf-txt.api.app.example.com
primary:    example.com   (apex)     →  companion: cf-txt.example.com
primary:    *.example.com (wildcard) →  companion: cf-txt._wildcard.example.com
```

A few notes:

- **The companion is a real DNS record.** It's queryable via `dig`
  and visible in the Cloudflare dashboard. The operator is
  intentional about not hiding it — operators tools can see what
  this operator owns by inspecting the zone.
- **Wildcards get a `_wildcard` sentinel.** Cloudflare warns on
  literal `*` in companion names; the operator rewrites the `*`
  label to `_wildcard`, which is reserved (per RFC convention) and
  never collides with real hostnames in a normal zone.
- **The scheme is "prefix as the new leftmost label"** (Slice 1
  correction). The pre-S1 scheme treated the hostname's final label
  as the zone and produced `cf-txt-<sanitized>.dev` style names —
  which broke when the hostname's apex wasn't a zone (`external.jacaudi.dev`
  collapsed to `cf-txt-external-jacaudi.dev`, an entirely different
  apex). The current scheme is collision-free and zone-stable.

The legacy pre-S1 naming is still recognized on read (for GC of
pre-feature companions) but never used for new writes. The operator
prunes legacy companions opportunistically when it sees them.

## Companion content: JSON or AES-GCM

The companion's TXT content is a 5-field JSON payload, optionally
encrypted with AES-256-GCM.

### Plaintext form (default)

```json
{"v":1,"k":"CloudflareDNSRecord","ns":"default","n":"app-example-com","h":"sha256:abcdef..."}
```

Fields:

| Field | Meaning |
|---|---|
| `v` | Schema version. Only `1` is recognized; anything else → `ErrUnrecognizedCodec`. |
| `k` | The Kubernetes resource kind of the owner — typically `CloudflareDNSRecord`. |
| `ns` | The Kubernetes namespace of the owning CR. |
| `n` | The Kubernetes name of the owning CR. |
| `h` | Optional content hash of the primary record's content (`sha256:<hex>`). When present, lets the operator detect drift cheaply (compare hashes). When unknown or not yet computed, omitted. |

> **The identifier is `(kind, namespace, name)`, NOT UID.** If you
> delete a CR and recreate one with the same `ns` + `n` + `k`, the
> new CR can adopt the old CR's primary record without intervention —
> the companion still identifies it as the owner. This is intentional
> for GitOps workflows where CR objects come and go but the desired
> state is stable.

### Encrypted form (AES-256-GCM)

When the operator is configured with a 32-byte AES key (via
`CloudflareCredentialRef.TxtRegistryKeySecretRef`), it encrypts the
same JSON payload before writing. The wire format is:

```
v1:<base64-nonce>:<base64-ciphertext>
```

- **`v1:`** — stable envelope marker. The read-side autodetects this
  prefix to know it's the AES form.
- **`<base64-nonce>`** — standard-base64-encoded 12-byte GCM nonce
  (one per encode; randomly generated). 16 characters.
- **`<base64-ciphertext>`** — standard-base64-encoded AES-256-GCM
  ciphertext + auth tag. The plaintext is the same JSON payload
  shown above.

The full encoded value typically fits in one TXT segment (under 255
bytes); longer values get split into multiple TXT strings per RFC
1035.

## When to encrypt: the AES key Secret

Configure via `CloudflareCredentialRef.TxtRegistryKeySecretRef`:

```yaml
spec:
  cloudflare:
    tokenSecretRef:
      name: cloudflare-credentials
      key: token
    accountIDSecretRef:
      name: cloudflare-credentials
      key: accountID
    txtRegistryKeySecretRef:
      name: cloudflare-registry-key
      key: key                   # default is "key"; set explicitly for clarity
```

The key Secret carries a single 32-byte (256-bit) value under the
referenced key:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: cloudflare-registry-key
  namespace: cloudflare-system
  labels:
    app.kubernetes.io/part-of: cloudflare-operator
type: Opaque
data:
  key: <base64-of-32-random-bytes>
```

To generate the key:

```sh
head -c 32 /dev/urandom | base64
```

> **The key must be exactly 32 bytes after base64 decode.** The
> operator rejects mis-sized keys at credential resolve time with a
> dedicated error.

The `TxtRegistryKeySecretRef` field is part of `CloudflareCredentialRef`,
so it can be:

- **Operator-wide** — set at the chart's operator-level default to
  encrypt every CR's companion.
- **Per-CR** — set on a single CR's `spec.cloudflare` to encrypt
  only that CR's companion. Mixed deployments work (some CRs
  encrypted, others plaintext) because reads autodetect.

## When NOT to encrypt

The AES mode protects exactly one thing: the **identity of the
operator's owner CR** is no longer readable in public DNS. The
primary record itself (hostname, content, proxied bit, TTL) is fully
public regardless of mode — that's the nature of DNS.

If you're not in a regulated environment that treats Kubernetes
namespace + CR names as sensitive metadata, the plaintext form is
**simpler**, **debuggable** (a `dig` shows you the owner directly),
and **just as safe against operator-collision** (the goal of the
registry — the AES form encodes the same identifier, just opaquely).

The threat model the AES mode addresses:

- Someone with read-only access to your DNS records (the public
  internet, a SaaS DNS-monitoring tool, etc.) cannot determine that a
  given `app.example.com` is managed by a CR in your `prod` namespace
  named `frontend-app`.

The threat model the AES mode does NOT address:

- The cloudflare API token in the operator's Secret is still highly
  privileged; an attacker with API access sees the primary records
  directly (and can write them) regardless of companion encryption.
- A side-channel attacker (e.g. listing CRs via the Kubernetes API)
  sees the owners' identities trivially — Kubernetes RBAC is the
  control there, not companion encryption.

## Rolling between plaintext and AES

Because reads use an **auto-detecting decoder** that handles both
forms, switching modes is essentially free:

1. Generate a 32-byte AES key.
2. Create the Secret (with the `app.kubernetes.io/part-of` label).
3. Set `TxtRegistryKeySecretRef` on the relevant
   `CloudflareCredentialRef`.
4. Force a reconcile (`cloudflare.io/reconcile-at` annotation).
5. The operator re-writes every companion in the encrypted form on
   its next reconcile (writes always use the configured codec).

To roll back: unset the field, force a reconcile, the operator
re-writes companions in plaintext form. Old AES companions are
still readable in-between because the autodetect handles them.

There is no key-rotation primitive today — rotating the AES key
across an already-encrypted fleet means every companion would briefly
fail to decode (`ErrUnrecognizedCodec`) until rewritten. For now,
the recommended pattern is: rotate the key by switching to plaintext,
force-reconciling, then switching to a fresh AES key + force-reconciling
again. Backlog item for a proper rotation primitive.

## Inspecting companions

### From outside the cluster

```sh
dig +short TXT cf-txt.app.example.com
```

Plaintext companions return a JSON string; encrypted ones return a
`v1:<nonce>:<ciphertext>` envelope. Both tell you the record is
operator-managed (the `cf-txt.` prefix is the giveaway); only the
plaintext form tells you which CR owns it.

### From inside Kubernetes

```sh
kubectl get cloudflarednsrecord/<name> -n <ns> -o jsonpath='{.status.recordID}{"\n"}{.status.txtRecordID}{"\n"}'
```

`txtRecordID` is the Cloudflare-side ID of the companion record. You
can query Cloudflare for it directly if you need to inspect the raw
content; the operator never logs the companion's content to its
output stream.

### Operator log entries

The operator logs companion-write outcomes at INFO. Search for:

```sh
kubectl logs -n cloudflare-system -l app.kubernetes.io/name=cloudflare-zone-controller \
  | grep -E 'TxtCompanion|companion'
```

Failures emit `Reason=OwnershipCompanionFailed` on the CR's Status
(see [`troubleshooting.md`](troubleshooting.md)).

## The engineering migration procedure

> Use this ONLY if the pragmatic "delete primary + let the operator
> recreate" flow in [`adopting-existing-records.md`](adopting-existing-records.md)
> is unacceptable.

To migrate a pre-companion record to operator ownership without an
outage:

1. **Pick the mode.** Either plaintext (recommended for inspectability)
   or AES (if your environment treats CR identities as sensitive).
2. **Compute the payload.** Construct the JSON:
   ```json
   {"v":1,"k":"CloudflareDNSRecord","ns":"<your-namespace>","n":"<your-cr-name>"}
   ```
   (Omit `h` — the operator will populate it on its next reconcile.)
3. **For AES mode**, encrypt the JSON via the same scheme the operator
   uses:
   - AES-256-GCM with a 12-byte random nonce
   - Plaintext = the JSON bytes
   - Wire format = `v1:` + `base64(nonce)` + `:` + `base64(ciphertext)`
4. **Write the companion** via the Cloudflare dashboard or `flarectl`:
   - Name: `cf-txt.<your-record-hostname>`
   - Type: `TXT`
   - Content: the JSON (plaintext mode) or the `v1:...` envelope
     (AES mode)
   - TTL: any; the operator doesn't care
5. **Set `spec.adopt: true`** on the CR (which should already exist in
   Observe mode per the recommended adopt flow).
6. **Flip to `spec.mode: Managed`**.
7. **Force a reconcile.** The operator reads the companion, sees the
   `(ns, n)` matches the CR, marks the record as adopted, and proceeds
   normally.

The engineering effort is non-trivial (especially for AES mode);
the brief-outage path is almost always strictly better unless you
have a hard SLA reason not to take it.

## Common gotchas

### "OwnershipCompanionFailed keeps firing"

Most often: the operator's API token lacks permission to read or
write TXT records in the zone. Verify the token has `Zone:DNS:Edit`
for the zone in question.

Second most often: the companion was written by an older operator
version with the pre-S1 affix scheme (`cf-txt-<hostname>` style).
The current operator recognizes both names on read but only writes
the new form. Old companions are pruned opportunistically.

### "A primary record exists but no companion does"

Two possibilities:

- Pre-feature record (created by an older tool / hand). Use the
  adopt flow (see [`adopting-existing-records.md`](adopting-existing-records.md)).
- Operator wrote the primary but failed mid-reconcile before
  writing the companion. Should self-heal on the next reconcile —
  if not, force one with `cloudflare.io/reconcile-at`.

### "Encrypted companion doesn't decrypt"

Usually means the AES key Secret changed or rotated. The
`OwnershipCompanionFailed` condition's `message` field carries the
specific error. To re-key, see the rolling section above.

## Related

- [`adopting-existing-records.md`](adopting-existing-records.md) —
  the user-facing adopt flow; this page is the under-the-hood detail.
- [`credentials.md`](credentials.md) — the
  `TxtRegistryKeySecretRef` field is part of
  `CloudflareCredentialRef`.
- [`troubleshooting.md`](troubleshooting.md) —
  `OwnershipCompanionFailed` and related condition reasons.
- [`annotations.md`](annotations.md) — no annotations directly
  control the registry; the codec choice is Secret-driven.
