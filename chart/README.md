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

## Renovate tracking

cloudflared image bumps are Renovate-tracked and land as `fix(cloudflared)` conventional commits.
A cloudflared update produces at least a patch release of the operator.
