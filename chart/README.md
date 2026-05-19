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

## Renovate tracking

cloudflared image bumps are Renovate-tracked and land as `fix(cloudflared)` conventional commits.
A cloudflared update produces at least a patch release of the operator.
