# Gateway API integration

The operator's headline pattern: opt a Gateway-API Gateway into
tunneled exposure, then let upstream routing (HTTPRoute, TLSRoute)
drive the per-hostname records and the cloudflared dataplane.

For a quick reference of the annotations involved, see
[`annotations.md`](annotations.md). This page is the end-to-end story:
how the pieces fit, what the operator creates on your behalf, and how
to drive overrides.

## What the operator does

When you annotate a Gateway with `cloudflare.io/tunnel: "true"`, the
operator's source-controllers walk the cluster every reconcile and:

1. **Ensure a `CloudflareTunnel` CR** exists in the same namespace as
   the Gateway. Auto-created tunnel CRs are named
   `<namespace>[-<tunnel-name-annotation>]` and carry the
   `cloudflare.io/auto-created` annotation (which gates cascade-GC —
   see below).
2. **Spawn the cloudflared dataplane** — a Deployment + Service in the
   same namespace, configured from the tunnel CR's
   `spec.connector.*` block (replicas, protocol, log level, resources).
   The Deployment's image is the operator's compile-time pinned
   cloudflared release; the chart's `controllers.tunnel.connector.image`
   value (or the CR's `spec.connector.image`) overrides.
3. **Discover every HTTPRoute / TLSRoute** attached to the Gateway via
   `parentRefs`. For each, walk the Route's hostnames.
4. **Emit a `CloudflareDNSRecord` CR per hostname**, pointing at the
   tunnel's CNAME (`<tunnel-id>.cfargotunnel.com`). The emitted CR
   carries owner labels so it's GC'd when the source Route/Gateway is
   deleted.
5. **Write the cloudflared remote config** (the ingress rules) to
   Cloudflare so the tunnel routes incoming HTTP / TLS traffic to the
   correct Kubernetes Service.

The operator owns the CloudflareTunnel CR, the cloudflared Deployment +
Service, the connector-token Secret, the emitted DNSRecord CRs, and
the remote tunnel config. You own the Gateway, the Routes, and the
backend Services they point at.

## The opt-in: Gateway only

**`cloudflare.io/tunnel: "true"` lives ONLY on a Gateway.** Routes
(HTTPRoute, TLSRoute) do NOT set this on themselves. The Slice 1
correctness fix (2026-05) confirmed this: the operator's
`tunnelToHTTPRoutes` / `tunnelToTLSRoutes` watch maps DO NOT filter on
the annotation at the Route level. Attachment is purely via the
Gateway-API `parentRefs` relationship.

This matters because:

- **Routes inherit the tunnel decision from their Gateway.** Adding the
  annotation to a Gateway lights up every Route attached to it; there's
  no per-Route opt-in plumbing.
- **You can't "opt out" of tunneling on a specific Route by setting
  `cloudflare.io/tunnel: "false"` on the Route.** The opt-in / opt-out
  decision belongs to the Gateway. To exclude a Route from the tunnel,
  detach it from the annotated Gateway (use a separate Gateway, or
  use `allowedRoutes` to scope which namespaces can attach).

## Required annotations on the Gateway

| Annotation | Why it's required |
|---|---|
| `cloudflare.io/tunnel: "true"` | The opt-in. |
| `cloudflare.io/gateway-service: <ns>/<name>[:<port>]` | Names the Kubernetes Service cloudflared forwards to. No label-based fallback (Gateway implementations expose their listener Service differently — explicit annotation is the only reliable contract). Missing → `GatewayServiceUnspecified` on the emitted tunnel CR. |
| `cloudflare.io/zone-ref: <crname>` | References a `CloudflareZone` CR. Without it, emitted DNSRecord CRs have no zone to resolve against. |

## Optional inheritance annotations on the Gateway

These flow through to every emitted `CloudflareDNSRecord` CR for routes
attached to the Gateway:

- `cloudflare.io/zone-ref-namespace` — cross-namespace zone ref (Gateway
  in namespace A, zone in namespace B).
- `cloudflare.io/proxied` — orange-cloud default (operator default:
  `true`).
- `cloudflare.io/ttl` — DNS TTL (operator default: `1` / automatic).
- `cloudflare.io/no-tls-verify` — skip origin TLS verification.
- `cloudflare.io/origin-server-name` — expected SAN on origin cert.
- `cloudflare.io/gateway-apex` — apex hostname for wildcard-only
  Gateways. The gateway-source publishes
  `<apex> CNAME → <tunnel-id>.cfargotunnel.com` and per-route chain
  records CNAME to `<apex>` instead of to the tunnel CNAME directly.

## Route attachment

### HTTPRoute

The standard pattern: HTTPRoute with `parentRefs` pointing at the
tunnel-enabled Gateway.

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: app-route
spec:
  parentRefs:
    - name: example-gateway
  hostnames:
    - app.example.com
    - api.example.com
  rules:
    - matches:
        - path: { type: PathPrefix, value: / }
      backendRefs:
        - name: app-backend
          port: 8080
```

Each hostname (`app.example.com`, `api.example.com`) becomes an emitted
`CloudflareDNSRecord` CR. The scheme inherited by the emitted record is
derived from the parent listener's protocol (HTTPS → `https`,
HTTP → `http`) unless `cloudflare.io/scheme` is set on the Route or
Gateway to override.

See [`examples/annotated_httproute.yaml`](../examples/annotated_httproute.yaml)
for the self-contained Gateway + HTTPRoute pair.

### TLSRoute (TCP-mode)

For passthrough TLS workloads (databases, SSH, raw TCP), use TLSRoute.
The Gateway must have a listener with `protocol: TLS` and
`tls.mode: Passthrough`. The emitted DNSRecord points at the tunnel
CNAME; cloudflared TCP-mode handles the passthrough.

```yaml
apiVersion: gateway.networking.k8s.io/v1alpha2
kind: TLSRoute
metadata:
  name: db-route
spec:
  parentRefs:
    - name: example-gateway
      sectionName: passthrough-tls
  hostnames:
    - db.example.com
  rules:
    - backendRefs:
        - name: postgres
          port: 5432
```

## Per-Route overrides

A Route can override individual inherited annotations for its specific
hostnames. The precedence is:

```
Route annotation > Gateway annotation > operator default
```

Each annotation overrides independently — a Route can flip `proxied`
without affecting the inherited `zone-ref` or `ttl`.

Common override cases:

- **Grey-cloud one hostname** (Cloudflare proxy off — origin IP exposed
  to clients):
  ```yaml
  metadata:
    annotations:
      cloudflare.io/proxied: "false"
  ```
- **Self-signed backend cert** (skip origin TLS verification):
  ```yaml
  metadata:
    annotations:
      cloudflare.io/no-tls-verify: "true"
  ```
- **Bypass the Gateway's apex hostname** (chain directly to the tunnel
  CNAME instead of through the apex CNAME):
  Set `cloudflare.io/gateway-apex` empty on the Route. *(Rarely needed;
  the default apex routing is correct for almost every case.)*

See [`examples/annotated_httproute_override.yaml`](../examples/annotated_httproute_override.yaml)
for a worked example.

## Generated objects

When the operator processes an annotated Gateway + N attached Routes,
the following materialize:

```
1 × CloudflareTunnel                    (the tunnel CR, owns the next 3)
1 × Deployment (cloudflared)            (the dataplane)
1 × Service (cloudflared metrics)       (observability)
1 × Secret  (cloudflared connector token)
N × CloudflareDNSRecord                 (one per hostname across all attached Routes)
```

Plus a separate set of objects in Cloudflare itself:

```
1 × tunnel                              (in the Cloudflare account)
1 × tunnel remote config                (cloudflared ingress rules)
N × DNS records                         (per hostname; orange or grey per inheritance)
N × TXT companion records               (ownership markers, paired with each DNS record)
```

You can inspect the operator-side objects with the usual `kubectl get`
commands; the auto-created tunnel CR carries `cloudflare.io/auto-created`
as a marker.

## Cascade GC

Auto-created tunnel CRs are subject to **cascade-GC**: when the last
source (Gateway / Route / Service) that references the tunnel is
deleted, the operator garbage-collects the tunnel CR, its cloudflared
dataplane, and the Cloudflare-side tunnel.

The opposite is true for **direct-create** tunnel CRs (you applied them
yourself; they lack `cloudflare.io/auto-created`). Those are never
auto-GC'd; you own their lifecycle.

Concrete sequence when you delete the Gateway:

1. Source-controllers observe the Gateway deletion.
2. Routes that were attached lose their `parentRef` target.
3. Emitted DNSRecord CRs lose their owner reference and are GC'd.
4. Cloudflared remote config is updated to remove the ingress rules.
5. If the tunnel CR's set of remaining sources is empty AND it carries
   `cloudflare.io/auto-created`, the tunnel CR self-deletes.
6. The cloudflared Deployment + Service + Secret are GC'd via
   owner-references on the tunnel CR.
7. The tunnel in Cloudflare is deleted via the API.

The operator's TXT-companion registry ensures DNS records are only
deleted when this operator created them — hand-authored DNS records
in the same zone are not touched.

## Common Gateway API gotchas

### `GatewayServiceUnspecified`

You see this condition on the auto-created tunnel CR. Cause: the
Gateway doesn't carry `cloudflare.io/gateway-service`. Fix: add the
annotation.

```yaml
metadata:
  annotations:
    cloudflare.io/gateway-service: envoy-gateway-system/envoy-default-default
```

The value is `<namespace>/<service-name>[:<port>]` — the Kubernetes
Service that the Gateway implementation (e.g. Envoy Gateway, Contour,
NGINX Gateway Fabric) exposes for the Gateway's listeners.

### Wildcard-only Gateway

If your Gateway's listener hostname is `*.example.com` (no concrete
listener), you need `cloudflare.io/gateway-apex: example.com` (or
similar). Without it, the operator can't pick a single canonical
hostname to publish the tunnel CNAME under, and reconciliation blocks.

### Route attaches but no DNS record appears

Three common causes:

1. **The Gateway lacks `cloudflare.io/zone-ref`.** Without it, the
   operator can't resolve which zone the Route's hostnames belong to.
2. **The Route's hostnames don't match the Gateway's listener
   `hostname`.** Gateway-API filters routes by hostname compatibility;
   if no listener accepts the Route's hostname, the parentRef is
   logically detached even though declared.
3. **The Route uses `allowedRoutes.namespaces.from: Same`** and the
   Route is in a different namespace from the Gateway. Use
   `from: Selector` with an explicit label selector, or `from: All`.

Look at the Route's `Status.parents[].conditions` for the
`Accepted` / `ResolvedRefs` conditions — Gateway-API surfaces detachment
reasons there.

### Records appear but the tunnel doesn't serve

Three common causes:

1. **`cloudflare.io/gateway-service`** points at a Service that doesn't
   exist, or exists but doesn't expose the right port. Check
   `kubectl get svc -n <ns>`.
2. **The cloudflared Deployment is `0/N` ready.** Inspect
   `kubectl get deploy -n <ns> cloudflared-<tunnel-name>` and the
   pod logs. Usually a connector-token issue or an outbound-network
   issue (cloudflared needs egress to `*.argotunnel.com`).
3. **Cloudflared can't reach the backend Service.** The tunnel's remote
   config is correct but the Service is unreachable from the cloudflared
   pod's network namespace. Apply a NetworkPolicy lift if you're running
   with a restrictive default-deny.

For more troubleshooting, see [`troubleshooting.md`](troubleshooting.md)
*(future)*.

## Related

- [`annotations.md`](annotations.md) — every `cloudflare.io/*` annotation in one table.
- [`reconciliation.md`](reconciliation.md) — forcing a reconcile after a change.
- [`credentials.md`](credentials.md) — credential resolution for the tunnel CR.
- [`examples/annotated_gateway.yaml`](../examples/annotated_gateway.yaml) — minimal opt-in Gateway.
- [`examples/annotated_httproute.yaml`](../examples/annotated_httproute.yaml) — self-contained Gateway + HTTPRoute pair.
- [`examples/annotated_httproute_override.yaml`](../examples/annotated_httproute_override.yaml) — per-Route override.
