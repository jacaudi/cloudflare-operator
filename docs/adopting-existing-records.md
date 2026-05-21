# Adopting existing Cloudflare records

When you want a `CloudflareDNSRecord` CR to take over a record that
already exists at Cloudflare (created by a script, an older tool, hand
edits, or a different operator) — that's adoption. The operator
supports it safely, with one big caveat:

> **Adoption is TXT-companion-verified.** The operator will only adopt
> a record whose paired TXT registry entry identifies *this specific
> CR*. A record with no companion, or with a companion that identifies
> a different CR (or a different operator), is **refused** — not
> silently overwritten.

This page explains why that's the design, what the safety states are,
how to adopt cleanly, and the migration path for records that
pre-date the TXT-companion registry.

## Why adopt exists

You have a record like `app.example.com → 10.0.0.5` already in
Cloudflare. You want to manage it from Kubernetes going forward — but
deleting it and recreating it would cause a DNS outage. Adoption lets
you point a `CloudflareDNSRecord` CR at the existing record and have
the operator take ownership without dropping a beat.

## Why the TXT companion exists

Without an ownership marker, two failure modes loom large:

1. **Operator wars.** Another operator (or a different instance of this
   operator) thinks it owns the same record. Both push competing
   updates; the record flaps between states. Hard to debug.
2. **Accidental clobber.** A hand-authored record happens to have the
   same name as a CR. Without verification, the operator overwrites
   it on the next reconcile. Production goes sideways.

The TXT companion solves both. For every DNS record the operator
manages, it also writes a paired **TXT registry entry**:

- **Name:** `cf-txt.<hostname>` — a sibling DNS name. For
  `app.example.com` the companion is `cf-txt.app.example.com`.
- **Type:** `TXT`
- **Content:** a JSON payload (or AES-GCM-encrypted JSON; see
  [`txt-registry.md`](txt-registry.md) *(future)*) identifying which
  CR (namespace + name + UID) owns the primary record.

The companion is the operator's ownership marker. On every reconcile
the operator reads it, verifies the identity matches the CR it's
processing, and only then mutates the primary record.

## The three adopt outcomes

When you set `spec.adopt: true` on a `CloudflareDNSRecord` whose
primary record already exists in Cloudflare, the operator walks one
of three paths:

### 1. Adopted

The TXT companion exists, parses cleanly, and identifies *this CR*.
The operator updates `Status.RecordID` to the live Cloudflare record's
ID and proceeds with normal managed reconciliation. `Ready=True`.

This is what happens after the recommended migration flow below.

### 2. AdoptRefusedNoTXT

The primary record exists, but there's no TXT companion. The operator
can't verify ownership and refuses to overwrite. Surfaces as:

```yaml
status:
  phase: Error
  conditions:
    - type: Ready
      status: "False"
      reason: AdoptRefusedNoTXT
      message: "TXT companion missing; refusing adoption (design §5.4)"
```

This is the case for **pre-feature records** — records created before
this operator started writing companions. See the migration section
below.

### 3. AdoptRefusedForeign

The primary record exists AND a TXT companion exists, but the
companion identifies a different CR (different namespace, different
name, different operator). Refused with:

```yaml
status:
  phase: Error
  conditions:
    - type: Ready
      status: "False"
      reason: AdoptRefusedForeign
      message: "TXT companion claims a different owner; refusing adoption"
```

This is the safety net. If you see this, *something else* thinks it
owns the record. Resolve the conflict at the owner end before forcing
adoption (delete the other CR / migrate it; or pick a different
hostname).

## The recommended adopt flow

```
Step 1:  Mode: Observe        →  verify operator can see the record
Step 2:  add TXT companion    →  one-time migration write
Step 3:  Adopt: true + Mode: Managed  →  flip to ownership
```

### Step 1: Observe mode (reconnaissance)

Create the CR in Observe mode first:

```yaml
apiVersion: cloudflare.io/v2alpha1
kind: CloudflareDNSRecord
metadata:
  name: app-example-com
spec:
  zoneRef: { name: example-com }
  name: app.example.com
  type: CNAME
  content: origin.example.net   # the current Cloudflare-side value
  mode: Observe                  # READ-ONLY: never mutates Cloudflare
  # adopt: false (default — we're not claiming ownership yet)
```

Observe mode polls Cloudflare every reconcile interval and surfaces
the live state on the CR's `Status`. It does NOT mutate anything. Use
it to confirm:

- The credential resolves and has read access to the zone.
- The record's content matches what you expect.
- `Status.CurrentContent` matches what you'll put under `spec.content`
  when you flip to Managed.

If anything mismatches, fix it at the manifest level *before* moving
on. Observe mode is a no-cost dress rehearsal.

### Step 2: Add the TXT companion (one-time migration)

The operator never writes a companion for an Observe-mode CR (it can't
— Observe is read-only). To bootstrap adoption, write the companion
yourself, once. The companion content must be the operator's JSON
ownership payload for this CR.

You can stamp it via the `kubectl annotate` + force-reconcile loop
once you have the CR in Managed mode (Step 3), OR you can use
`dig` / the Cloudflare dashboard to inspect what the operator writes
for a similar already-managed record and replicate that format
manually.

For most users, the **pragmatic path** is:

```
1. Create the CR in Observe mode (Step 1).
2. Manually delete the pre-existing primary record from the Cloudflare
   dashboard (brief DNS outage acceptable).
3. Flip the CR to Mode: Managed (drop Observe). On the next reconcile,
   the operator creates the primary record AND the TXT companion from
   scratch — no adoption needed.
```

The brief outage in step 2 is the trade-off for not running the
manual TXT-companion-injection procedure. If the outage is
unacceptable, see the engineering procedure in
[`txt-registry.md`](txt-registry.md) *(future)*.

### Step 3: Set `adopt: true` + `mode: Managed`

Once the TXT companion is in place (either via the manual procedure
above or because you're adopting a record the operator previously
created in a different cluster), flip the CR:

```yaml
spec:
  mode: Managed
  adopt: true
  # ... rest unchanged
```

On the next reconcile the operator:

1. Reads the TXT companion.
2. Verifies it identifies THIS CR (namespace + name + UID).
3. Updates `Status.RecordID` to point at the live record.
4. Marks `Ready=True`.

From then on, the CR is managed normally — drift is detected and
reconciled, the record is deleted with the CR (per
`DeletionPolicy`), etc.

## Why no silent backfill?

The natural temptation is: "if no companion exists, just write one and
adopt." The operator deliberately does NOT do this. Two reasons:

1. **No way to distinguish "pre-feature record" from "another
   operator's record".** If a different operator manages the primary
   without writing a companion, silent backfill would steal it. The
   adopt-refusal is the only safe default.
2. **It would mask configuration mistakes.** A user with a typo'd CR
   name and `adopt: true` would have the operator clobber a record
   the user didn't intend to touch. Refusal forces the user to
   explicitly walk the migration path.

The cost is one-time-per-record manual work; the benefit is "this
operator never destroys data it doesn't own."

## Status to watch

| Status / Condition | Meaning |
|---|---|
| `Phase: Ready`, `Ready: True`, `Status.RecordID` populated | Adopted successfully. The CR owns the record going forward. |
| `Phase: Error`, `Ready: False`, `reason: AdoptRefusedNoTXT` | No TXT companion. Walk the migration flow above. |
| `Phase: Error`, `Ready: False`, `reason: AdoptRefusedForeign` | TXT companion identifies a different owner. Investigate the other CR / operator first; don't force-adopt. |
| `Phase: Error`, `Ready: False`, `reason: ResolveFailed` | Couldn't reach Cloudflare or the credential is broken. See [`credentials.md`](credentials.md). |

When you fix the underlying state (add the companion, remove the
conflicting CR, etc.), trigger an immediate re-check with
`cloudflare.io/reconcile-at`:

```sh
kubectl annotate cloudflarednsrecord/<name> -n <ns> \
  cloudflare.io/reconcile-at=$(date -u +%Y-%m-%dT%H:%M:%SZ) --overwrite
```

See [`reconciliation.md`](reconciliation.md) for the force-reconcile
contract.

## When NOT to use adopt

- **Greenfield deploys.** If you're creating new records, just leave
  `adopt: false` (default). The operator creates the primary record
  AND the companion from scratch; there's nothing to adopt.
- **You're not sure the record exists.** Use Observe mode first; let
  `Status.CurrentContent` tell you whether it's there.
- **You're recreating after a delete.** If you deleted the CR and want
  to bring it back, just re-apply the CR (no `adopt: true` needed) —
  the operator's blind-create path handles the "I see a CF record
  that matches what I'm about to create" case via the 81058 / 81053
  collision protocol (S1 / Slice 1 self-heal).

## Related

- [`credentials.md`](credentials.md) — credential resolution (a wrong
  token surfaces as `ResolveFailed`, not as an adopt-refusal).
- [`reconciliation.md`](reconciliation.md) — forcing a reconcile after
  you fix the underlying state.
- [`annotations.md`](annotations.md) — the `cloudflare.io/adopt`
  annotation for Gateway-API-emitted DNSRecord CRs (inherits to every
  attached Route unless overridden).
- [`txt-registry.md`](txt-registry.md) *(future)* — the companion
  format, AES-GCM encryption, and the engineering migration procedure.
