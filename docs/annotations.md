# Annotation reference

Every annotation the operator reads or writes lives under the
`cloudflare.io/` namespace. The namespace is reserved — the operator
ignores any prefix outside it, and refuses to be confused by other
operators' annotations.

This page is the canonical reference for every annotation in that
namespace: where you set it, what it controls, what overrides what,
and what happens on a malformed value.

The behavior-by-pattern docs (annotation-driven Gateway attachment,
adopt flow, etc.) link back here; if you're learning the system,
start with [`gateway-api.md`](gateway-api.md) *(future)* or the
[Quick Start](../README.md#quick-start) and consult this page as
the cross-reference.

## Truthy values

Annotations that take a boolean accept any of (case-insensitive,
whitespace-trimmed):

| Value | Meaning |
|---|---|
| `true`, `yes`, `enable`, `enabled` | true |
| `false`, `no`, `disable`, `disabled` | false |

Anything else is rejected (`ErrUnrecognizedTruthy`) and the annotation
is treated as unset; the operator falls back to the default for that
field. Stick to `"true"` / `"false"` in fresh manifests.

## Tunnel attachment

Annotations that opt an object into operator-managed tunneling. Set on
the **source** object (the Service or Gateway).

| Annotation | Settable on | Required? | Effect |
|---|---|---|---|
| `cloudflare.io/tunnel` | Service, Gateway | **Yes** for opt-in | Sentinel: `"true"` opts the object into auto-tunnel creation. Anything else is a no-op. Routes (HTTPRoute / TLSRoute) DO NOT set this themselves — they're picked up via Gateway-API `parentRefs` to an opted-in Gateway. |
| `cloudflare.io/tunnel-name` | Service, Gateway | No | Override the auto-derived `CloudflareTunnel` name. Default: `cf-<ns>[-<tunnel-name-value>]`. Capped at 52 chars so derived names (`cloudflared-<name>`) fit the 63-char DNS-label limit. |
| `cloudflare.io/gateway-service` | Gateway | **Yes** when `cloudflare.io/tunnel: "true"` is set | Names the Kubernetes Service cloudflared forwards to for this Gateway's hostnames. Format: `<namespace>/<name>` or `<namespace>/<name>:<port>`. There is no label-based fallback — missing this annotation surfaces `GatewayServiceUnspecified` on the emitted tunnel CR. |
| `cloudflare.io/gateway-apex` | Gateway | No (required for wildcard-only Gateways) | Sets the public apex hostname for the tunnel. Per-route chain records CNAME to it; the gateway-source publishes `<apex> CNAME → <tunnel-id>.cfargotunnel.com`. Empty / invalid value → Warning Event + listener-derived fallback OR (wildcard-only) blocked. |
| `cloudflare.io/hostnames` | Service | No | Comma-separated explicit hostname list. When set, emitted DNSRecord CRs use these instead of being derived from the Service name. |

## Inherited by emitted DNSRecord (Gateway-API path)

These annotations live on a Gateway or a Route and flow through to the
operator-emitted `CloudflareDNSRecord` CR. Set them on the **Gateway**
for inheritance defaults; set them on a **Route** to override the
Gateway for that specific hostname.

**Precedence (most specific wins):**

```
Route annotation > Gateway annotation > operator default
```

| Annotation | Effect on emitted DNSRecord | Default |
|---|---|---|
| `cloudflare.io/zone-ref` | Sets `spec.zoneRef.name` — references a `CloudflareZone` CR. | none — required for emission |
| `cloudflare.io/zone-ref-namespace` | Cross-namespace zone ref: sets the namespace `spec.zoneRef` resolves in. Lets a Gateway in namespace A reference a `CloudflareZone` in namespace B. | source object's own namespace (back-compat) |
| `cloudflare.io/proxied` | Sets `spec.proxied` (orange-cloud bit). Truthy values per the table above. | `true` (tunnel-emitted records are proxied by default — Cloudflare-Tunnel CNAMEs need it to route) |
| `cloudflare.io/ttl` | Sets `spec.ttl`. Integer seconds, or `1` for automatic. | `1` (automatic) |
| `cloudflare.io/no-tls-verify` | Sets the emitted DNSRecord's `originRequest.noTLSVerify`. Skip origin TLS verification (self-signed certs, etc.). | `false` (verify) |
| `cloudflare.io/origin-server-name` | Sets the emitted DNSRecord's `originRequest.originServerName`. The expected SAN on the origin certificate. | unset |
| `cloudflare.io/scheme` | HTTPRoute-only. Sets the emitted DNSRecord scheme. `http` / `https`. | inherited from the parent listener's protocol |
| `cloudflare.io/adopt` | Sets `spec.adopt: true` on the emitted DNSRecord. Triggers the TXT-verified adopt flow on a pre-existing Cloudflare record. | `false` |

### How inheritance works

The operator's source-controllers walk:

1. The **Route**'s `metadata.annotations` (HTTPRoute / TLSRoute).
2. If the Route doesn't set the annotation, fall back to the
   **Gateway**'s annotations.
3. If neither sets it, fall back to the operator's default.

This applies per-annotation independently. A Route can override
`proxied` while still inheriting `zone-ref` and `ttl` from the Gateway.
A Route does NOT need to set `cloudflare.io/tunnel: "true"` itself —
that opt-in lives on the Gateway and routes attach via `parentRefs`.

See [`gateway-api.md`](gateway-api.md) *(future)* for end-to-end examples.

## DNS-only (Service → emitted DNSRecord)

Annotations that don't depend on a tunnel — useful for Services that
want a public DNS name without proxying through cloudflared.

| Annotation | Settable on | Effect |
|---|---|---|
| `cloudflare.io/dns-record` | Service | Explicit emitted-DNSRecord hostname. Without it, the operator derives the name from the Service. |
| `cloudflare.io/dns-target` | Service | Explicit DNS target (the value of the CNAME). Without it, the operator routes through the tunnel's CNAME. Setting this only makes sense when bypassing the tunnel for this specific record. |
| `cloudflare.io/port` | Service | Port cloudflared forwards to. Without it, the operator uses the Service's first port. |

The standard `zone-ref` / `proxied` / `ttl` annotations also apply to
the Service path; they live alongside the DNS-only ones.

## Force-reconcile

Apply to **any** of the 5 CRDs (Zone, ZoneConfig, DNSRecord, Ruleset,
Tunnel). The operator's prelude observes this annotation on every
reconcile and stamps the value into `Status.LastReconcileToken` when
the value differs from the prior token.

| Annotation | Settable on | Effect |
|---|---|---|
| `cloudflare.io/reconcile-at` | All 5 CRDs | Opaque token. Setting a new value forces a fresh full reconcile. Restart-immune — the ack lives in Status, not in operator memory. |

The token is **opaque** — the operator never parses it as a time.
Any value works; RFC 3339 timestamps are conventional because they're
sortable and human-readable.

```sh
kubectl annotate cloudflaretunnel/example-tunnel -n network \
  cloudflare.io/reconcile-at=$(date -u +%Y-%m-%dT%H:%M:%SZ) --overwrite
```

See [`reconciliation.md`](reconciliation.md) for the full design,
caller contract, and verification pattern.

## Operator-managed (don't set manually)

The operator writes these annotations on objects it creates. Reading
them is fine; setting or removing them manually breaks invariants.

| Annotation | Set on | Set when |
|---|---|---|
| `cloudflare.io/auto-created` | `CloudflareTunnel` CR | Stamped by `ensureTunnelCR` when the operator auto-creates a tunnel from a Service / Gateway / HTTPRoute / TLSRoute opt-in. Direct-create CRs (you `kubectl apply` them yourself) never carry this. The annotation gates auto-GC: an auto-created tunnel that loses all its sources is garbage-collected; a direct-create CR is never auto-GC'd. **Immutable after create** — do not patch. |

## What's NOT in the operator's namespace

The operator ignores annotations outside `cloudflare.io/`. Some external
projects use overlapping naming (e.g. `external-dns.alpha.kubernetes.io/`,
`nginx.ingress.kubernetes.io/`) — those are not read or written by this
operator. The two namespaces co-exist; you can run external-dns and this
operator side-by-side on the same Service without collision (other than
both wanting to manage the same Cloudflare record, which is a separate
discussion — see [`adopting-existing-records.md`](adopting-existing-records.md)
*(future)*).

## Examples

Every annotation in this reference is demonstrated in
[`examples/`](../examples/):

- [`examples/annotated_gateway.yaml`](../examples/annotated_gateway.yaml) — Gateway opt-in + inheritance defaults + commented cross-ns + apex
- [`examples/annotated_httproute.yaml`](../examples/annotated_httproute.yaml) — HTTPRoute attached via parentRef (inheritance)
- [`examples/annotated_httproute_override.yaml`](../examples/annotated_httproute_override.yaml) — per-Route override
- [`examples/annotated_service.yaml`](../examples/annotated_service.yaml) — Service path

## Related

- [`reconciliation.md`](reconciliation.md) — the `cloudflare.io/reconcile-at` deep-dive.
- [`credentials.md`](credentials.md) — credentials don't use annotations (Secrets only); cross-linked for completeness.
