# Credentials

Every Cloudflare API call the operator makes is scoped by a credential pair:
an **API token** + an **account ID**. This page covers how the operator
resolves credentials, the shapes you can use, and the failure modes.

If you're just installing the operator, the
[Quick Start](../README.md#quick-start) shows the canonical Secret + per-CR
override pattern. This page is the deep-dive.

## The credential pair

The operator treats `(API token, account ID)` as a **single unit**. You
can't supply the token from one source and the account ID from another at
the operator-default level — they have to travel together. At the per-CR
level you have one extra option (Secret-backed account ID), covered below.

A credential pair carries scope:

- The **API token** is the bearer credential. Every Cloudflare API call
  authorizes with it.
- The **account ID** scopes operations that aren't zone-scoped (creating
  tunnels, listing rulesets at account level, etc.). It's also a defensive
  check — the operator refuses operations against accounts other than the
  one it's configured for.

## Where the operator looks for credentials

In order of precedence (most specific wins):

```
CR's spec.cloudflare        — per-CR override
  ↓ (omitted)
Operator-level default      — chart values / env / mounted Secret
  ↓ (omitted)
ErrAccountIDUnset / ErrSecretNotFound — reconcile halts
```

### Per-CR override (`spec.cloudflare`)

Every CRD (`CloudflareZone`, `CloudflareZoneConfig`, `CloudflareRuleset`,
`CloudflareDNSRecord`, `CloudflareTunnel`) carries an optional
`spec.cloudflare` field of type `CloudflareCredentialRef`. When set, that
CR uses these credentials regardless of the operator-level default. Omit
the field to inherit the operator's default.

```yaml
spec:
  cloudflare:
    tokenSecretRef:
      name: cloudflare-credentials
      namespace: cloudflare-system
      key: token
    accountIDSecretRef:
      name: cloudflare-credentials
      namespace: cloudflare-system
      key: accountID
```

### Operator-level default

Set at install time via chart values that point at a Secret. The chart
materializes those into the operator's startup environment; the operator
resolves them at boot. CRs that omit `spec.cloudflare` inherit this default.

This is the right choice for single-account setups: one Secret, no
per-CR repetition. For multi-account or multi-tenant patterns, prefer
per-CR overrides.

## The Secret shape

### Token Secret

The Cloudflare API token Secret is referenced by a `SecretReference`:

```yaml
tokenSecretRef:
  name: cloudflare-credentials   # required
  namespace: cloudflare-system   # optional — defaults to the CR's namespace
  key: token                     # optional — defaults to "token"
```

The Secret itself is a standard `Opaque` Secret carrying the token under
the named key:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: cloudflare-credentials
  namespace: cloudflare-system
  labels:
    app.kubernetes.io/part-of: cloudflare-operator   # REQUIRED — see below
type: Opaque
stringData:
  token: "your-cloudflare-api-token"
```

### The `part-of` label requirement

> The operator's Kubernetes Secret cache is **label-scoped**. Only Secrets
> carrying `app.kubernetes.io/part-of: cloudflare-operator` are visible to
> the operator. Without the label, `client.Get(secret)` returns NotFound
> and credential resolution fails with `ErrSecretNotFound`.

The chart labels every Secret it creates (cloudflared connector tokens,
the operator's own bootstrap Secrets). **User-created credential Secrets
must also carry the label.** This is the most common first-time setup
failure: the Secret exists, but the operator behaves as if it doesn't.

To label an existing Secret:

```sh
kubectl label secret -n <ns> <name> app.kubernetes.io/part-of=cloudflare-operator
```

#### Why label-scope it?

Without scoping, controller-runtime maintains a cluster-wide LIST/WATCH on
every `Secret` in the cluster — most of which the operator has no use
for. On clusters with hundreds-to-thousands of Secrets, the resulting
cache memory + watch traffic is substantial. Label-scoping cuts both to
the order of operator-relevant Secrets only.

### Account ID — two forms

Exactly one of `accountID` (inline) or `accountIDSecretRef` (from a
Secret) must be set. The CRD enforces this via CEL validation; setting
both or neither is rejected at admission time.

**Inline** — simplest, account ID in the CR manifest:

```yaml
spec:
  cloudflare:
    tokenSecretRef: { name: cloudflare-credentials, key: token }
    accountID: "abc123def456abc123def456abc123de"
```

Use when the account ID isn't sensitive in your environment and you prefer
flat manifests.

**Secret-backed** — same Secret as the token (recommended):

```yaml
spec:
  cloudflare:
    tokenSecretRef:
      name: cloudflare-credentials
      key: token
    accountIDSecretRef:
      name: cloudflare-credentials
      key: accountID                 # NOT the default — must be set explicitly
```

> **Footgun:** `SecretReference.Key` defaults to `"token"`. If you omit
> `key:` on the `accountIDSecretRef`, the operator looks for the account
> ID under the `token` key (i.e. it reads the API token as an account
> ID) and credential resolution fails with `ErrSecretKeyMissing`. Always
> set `key: accountID` explicitly.

Use Secret-backed when account IDs are treated as sensitive metadata
(common in regulated environments) or when you want to rotate the pair
together with a single Secret update.

## Credential rotation

When you rotate the token in Cloudflare:

1. Update the Secret's `token` (and optionally `accountID`) keys with
   the new value.
2. The operator's `*cfgo.Client` cache holds the OLD token for up to
   **30 minutes** (absolute TTL from cache-insertion). New CR reconciles
   that hit the same `(token, accountID)` key get the cached client until
   the TTL elapses.

To force immediate adoption of the new token without waiting:

- **Easiest:** annotate any affected CR with `cloudflare.io/reconcile-at`
  (see [`reconciliation.md`](reconciliation.md)). The cache key is
  `sha256(token || accountID)`, so a Secret update with a different token
  produces a different key — the next reconcile rebuilds the client from
  scratch.
- **Heavy-handed:** restart the operator pod. The cache is in-process
  memory; a restart empties it.

If the new token has fewer permissions than the old one, expect 403s on
the next reconcile after rotation lands; the operator surfaces them as
`Phase=Error` with a `Ready=False` condition.

## Common errors

| Error | Cause | Fix |
|---|---|---|
| `ErrSecretNotFound: <ns>/<name>` | Secret doesn't exist at the referenced path, or exists but isn't labeled `app.kubernetes.io/part-of: cloudflare-operator`. | Create the Secret, or `kubectl label secret -n <ns> <name> app.kubernetes.io/part-of=cloudflare-operator`. |
| `ErrSecretKeyMissing: <ns>/<name> missing key "<key>"` | Secret exists but doesn't have the named key, OR the key is empty. | Add the key to the Secret. Common when `accountIDSecretRef` omits `key: accountID` (it defaults to `token` then reads the wrong field). |
| `ErrAccountIDUnset` | Neither `accountID` nor `accountIDSecretRef` set. | Set one. The CRD's CEL rule normally rejects this at admission, but reconcile guards against the field being cleared post-create. |
| CEL: `exactly one of accountID or accountIDSecretRef must be set` | Both fields populated. | Drop one. |
| Cloudflare API 403 | Token lacks permission for the resource. | Issue a token with the right zone-level / account-level scopes. |
| Cloudflare API 401 | Token revoked or wrong. | Update the Secret. |

## Multiple accounts

For multi-account or multi-tenant setups, leave the operator-level default
unset (or set it to one "control plane" account) and set
`spec.cloudflare` on every CR explicitly. The operator's internal
`*cfgo.Client` cache keys on `sha256(token || accountID)`, so each
distinct credential pair gets its own client + connection pool — no
cross-talk.

See [`multiple-accounts.md`](multiple-accounts.md) *(future)* for
deployment patterns.

## Related

- [`reconciliation.md`](reconciliation.md) — forcing a reconcile after
  rotating credentials.
- [`txt-registry.md`](txt-registry.md) *(future)* — the optional
  `TxtRegistryKeySecretRef` field on `CloudflareCredentialRef` for
  AES-encrypted ownership markers.
