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

## Renovate tracking

cloudflared image bumps are Renovate-tracked and land as `fix(cloudflared)` conventional commits.
A cloudflared update produces at least a patch release of the operator.
