# Gateway API Source and Service Annotations

cloudflare-operator v1 introduces two source controllers that watch your workloads and automatically manage DNS records and tunnel ingress rules:

- **`httproute_source`** — watches `HTTPRoute` and parent `Gateway` objects.
- **`service_source`** — watches `Service` objects.

Both controllers read from the same annotation vocabulary (`cloudflare.io/*`) and emit `CloudflareDNSRecord` and `CloudflareTunnelRule` CRs as needed. These emitted CRs carry `ownerReference` to the annotated object, so they are cleaned up automatically when the source object is deleted.

Sources never call the Cloudflare API directly. They only emit primitive CRs that the existing DNS and tunnel controllers reconcile.

**Prerequisite:** Set `registry.txtOwnerID` in your Helm values (or the `TXT_OWNER_ID` environment variable). Without it, source controllers are inert.

---

## Annotation Reference

All annotations use the `cloudflare.io/` prefix. Apply them to `HTTPRoute`, `Gateway`, or `Service` objects.

| Annotation | Applies to | Purpose | Required? |
|---|---|---|---|
| `cloudflare.io/target` | HTTPRoute, Gateway, Service | Opts into reconciliation; names the target shape | Yes (on the resource or inherited from parent Gateway) |
| `cloudflare.io/zone-ref` | HTTPRoute, Gateway, Service | Names the `CloudflareZone` CR that owns the hostnames | No — auto-resolved via longest-suffix match if absent |
| `cloudflare.io/zone-ref-namespace` | HTTPRoute, Gateway, Service | Namespace of the `CloudflareZone` CR | No — defaults to source's namespace |
| `cloudflare.io/tunnel-ref-namespace` | HTTPRoute, Gateway, Service | Namespace of the `CloudflareTunnel` (when `target: tunnel:<name>`) | No — defaults to source's namespace |
| `cloudflare.io/tunnel-upstream` | HTTPRoute | Explicit backend URL; triggers per-Route `CloudflareTunnelRule` emission | No — opt-in escape hatch |
| `cloudflare.io/hostnames` | Service only | Comma-separated FQDN list (Services have no `spec.hostnames`) | Yes — for Service sources |
| `cloudflare.io/port` | Service only | Named port or integer; selects which Service port to forward through the tunnel | No — defaults to first port in `spec.ports` |
| `cloudflare.io/scheme` | Service only | `http` / `https` / `h2c`; overrides port-name inference for tunnel backend URL | No |
| `cloudflare.io/proxied` | HTTPRoute, Gateway, Service | `true` / `false`; proxy toggle on the DNS record | No — forced `true` for tunnel targets; defaults `true` otherwise |
| `cloudflare.io/ttl` | HTTPRoute, Gateway, Service | DNS TTL in seconds | No — defaults to `1` (automatic) |
| `cloudflare.io/adopt` | HTTPRoute, Gateway, Service | `true` to claim existing Cloudflare records that have no ownership TXT | No — conflicts fail by default |

### `cloudflare.io/target` value forms

`target` accepts one of three forms:

- **`tunnel:<name>`** — DNS becomes a CNAME to the tunnel's `<tunnel-id>.cfargotunnel.com` CNAME (read from `status.tunnelCNAME`). `proxied` is forced `true`. Use `cloudflare.io/tunnel-ref-namespace` when the tunnel CR is in a different namespace.
- **`cname:<fqdn>`** — DNS becomes a CNAME to the literal FQDN. No tunnel involvement. Useful for stable external endpoints.
- **`address`** — (HTTPRoute and Gateway only) — DNS is derived from the parent `Gateway`'s `status.addresses`: `A` for IPv4, `AAAA` for IPv6, `CNAME` for hostname-typed addresses. Rejected with a `Warning InvalidAnnotation` event on a `Service` (visible in `kubectl describe service <name>`).

---

## Annotation Inheritance (HTTPRoute)

Annotations on a `Gateway` are **defaults** for every `HTTPRoute` attached to it via `parentRef`. Route-level annotations **override** the Gateway's annotations for the same key.

Precedence order (per annotation key):

1. Route-level value (if set)
2. Parent Gateway value (first matching `parentRef`)
3. Built-in default from this section

This means you can annotate a Gateway once with `cloudflare.io/target` and `cloudflare.io/zone-ref` and every attached HTTPRoute inherits those settings without per-route annotation.

`Service` objects have no inheritance. Each Service must carry all required annotations directly.

---

## Multi-Zone Resolution

When `cloudflare.io/zone-ref` is absent, the controller resolves the zone automatically by longest-suffix match:

- Lists all `CloudflareZone` CRs cluster-wide.
- For each hostname in the source, finds the zone whose `spec.name` is the longest suffix of the hostname.
- If zero zones match, sets `Ready=False, reason=NoMatchingZone` and waits for a user edit (no requeue).
- If multiple zones match with equal suffix length and no explicit `zone-ref` is given, sets `Ready=False, reason=AmbiguousZone`.

Use `cloudflare.io/zone-ref` to disambiguate or when your zone naming does not follow the hostname suffix.

---

## Worked Example: Gateway API Case

This example shows how annotations on an HTTPRoute drive DNS for a workload served through a tunnel, with routing handled by the Gateway.

```yaml
apiVersion: cloudflare.io/v1alpha1
kind: CloudflareTunnel
metadata:
  name: prod
  namespace: network
spec:
  name: prod
  secretRef:
    name: cloudflare-api-token
  generatedSecretName: prod-tunnel-credentials
  connector:
    enabled: true
    replicas: 2
  routing:
    defaultBackend:
      url: https://envoy-gateway-internal.network.svc.cluster.local
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: internal
  namespace: network
spec:
  gatewayClassName: envoy-gateway
  listeners:
    - name: https
      port: 443
      protocol: HTTPS
      hostname: "*.example.com"
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: myapp
  namespace: apps
  annotations:
    cloudflare.io/target: "tunnel:prod"
    cloudflare.io/tunnel-ref-namespace: "network"
    cloudflare.io/zone-ref: "example-com"
    cloudflare.io/zone-ref-namespace: "network"
spec:
  parentRefs:
    - name: internal
      namespace: network
  hostnames:
    - "app.example.com"
    - "api.example.com"
  rules:
    - backendRefs:
        - name: myapp
          port: 8080
```

The operator emits for each hostname:
- One `CloudflareDNSRecord` (CNAME → tunnel's `status.tunnelCNAME`) in the `apps` namespace, with `ownerReference` to the HTTPRoute.
- One companion TXT `CloudflareDNSRecord` for ownership tracking.

No `CloudflareTunnelRule` is emitted — the tunnel's `spec.routing.defaultBackend` handles routing from cloudflared to the Gateway, which then routes to `myapp`.

Traffic flow: `app.example.com` → CNAME → `<tunnel-id>.cfargotunnel.com` → cloudflared connector → Envoy Gateway → HTTPRoute rules → `myapp` Service → pod.

---

## Worked Example: Direct-to-Service Case

This example shows annotation-driven DNS and tunnel ingress without a Gateway. cloudflared routes directly to the Service.

```yaml
apiVersion: cloudflare.io/v1alpha1
kind: CloudflareTunnel
metadata:
  name: prod
  namespace: network
spec:
  name: prod
  secretRef:
    name: cloudflare-api-token
  generatedSecretName: prod-tunnel-credentials
  connector:
    enabled: true
    replicas: 2
---
apiVersion: v1
kind: Service
metadata:
  name: myapp
  namespace: apps
  annotations:
    cloudflare.io/target: "tunnel:prod"
    cloudflare.io/tunnel-ref-namespace: "network"
    cloudflare.io/hostnames: "app.example.com,api.example.com"
    cloudflare.io/zone-ref: "example-com"
    cloudflare.io/zone-ref-namespace: "network"
    cloudflare.io/port: "http"
spec:
  selector:
    app: myapp
  ports:
    - name: http
      port: 8080
      targetPort: 8080
```

The operator emits:
- One `CloudflareDNSRecord` per hostname (CNAME → tunnel), both in the `apps` namespace with `ownerReference` to the Service.
- One companion TXT per hostname.
- **One `CloudflareTunnelRule`** carrying both hostnames with `backend.serviceRef` pointing at the `myapp` Service. The operator resolves the backend URL as `http://myapp.apps.svc.cluster.local:8080`.

Traffic flow: `app.example.com` → CNAME → tunnel → cloudflared → `http://myapp.apps.svc.cluster.local:8080` → pod. No Gateway involved.

---

## Conflict Detection and Resolution

The operator never silently overwrites a record it did not create. When a conflict is detected, it surfaces as:

- A `Warning` **Event** on the HTTPRoute or Service (visible via `kubectl describe`).
- A `Conflict=True` condition on the emitted `CloudflareDNSRecord`.

### Hand-authored `CloudflareDNSRecord` wins

If you have a hand-authored `CloudflareDNSRecord` CR for the same FQDN, the TXT registry mediates ownership. Hand-authored CRs are not emitted by a source controller and therefore use a different name pattern, so both CRs coexist. If the hand-authored CR's companion TXT uses a different `txtOwnerID`, the source controller's emitted CR gets `Ready=False, reason=RecordOwnershipConflict` (foreign TXT owner). If no companion TXT exists and `cloudflare.io/adopt` is not set, the emitted CR gets `Ready=False, reason=TxtRegistryGap`.

Resolution: delete the hand-authored CR and its companion TXT and let the source controller take ownership, or remove the annotation from the source.

### Two annotation sources conflict on the same hostname

If two `HTTPRoute` or `Service` objects both claim the same FQDN, each source controller emits a separately named `CloudflareDNSRecord` CR. Both CRs share the same `txtOwnerID` and both treat the companion TXT as their own, so no hard error is raised — instead both reconcile and the last writer wins, causing DNS content churn. There is no `RecordConflict` event emitted in this case.

Resolution: remove the `cloudflare.io/target` annotation from one of the conflicting sources or change one of the hostnames so they no longer overlap.

### External record with no ownership TXT

If a record already exists in Cloudflare with no ownership TXT, the operator refuses by default. Add `cloudflare.io/adopt: "true"` to the source to claim the record.

### External record with a foreign ownership TXT

If a record exists with a TXT owned by a different `txtOwnerID` that is not in `txtImportOwners`, the operator refuses with `RecordOwnershipConflict`. Add the foreign owner ID to `txtImportOwners` in your Helm values to allow adoption.

See [external-dns-migration.md](external-dns-migration.md) for the full adoption workflow.

---

## Checking Source Status

Sources are not ours to add status fields to. Diagnostics surface through Events and labeled emitted CRs.

```bash
# Check events on an HTTPRoute
kubectl describe httproute myapp -n apps

# Find all DNS records emitted by a specific HTTPRoute
kubectl get cloudflarednsrecord \
  -l cloudflare.io/source-name=myapp,cloudflare.io/source-kind=HTTPRoute \
  -A

# Find all tunnel rules emitted by a specific Service
kubectl get cloudflaretunnelrule \
  -l cloudflare.io/source-name=myapp,cloudflare.io/source-kind=Service \
  -A

# Check adoption conditions on an emitted DNS record
kubectl describe cloudflarednsrecord <name> -n apps
```

Events use the following reason strings:

| Reason | Meaning |
|---|---|
| `InvalidAnnotation` | Malformed `cloudflare.io/target` value |
| `NoMatchingZone` | No `CloudflareZone` CR matched the hostname |
| `AmbiguousZone` | Multiple zones matched with equal suffix length |
| `TunnelNotFound` | `tunnel:<name>` points at a non-existent `CloudflareTunnel` |
| `TunnelNotReady` | The tunnel exists but `status.tunnelCNAME` is not yet set (transient) |
| `RecordConflict` | Another CR or hand-authored record owns this FQDN |
| `RecordOwnershipConflict` | TXT registry shows a foreign owner not in `txtImportOwners` |
| `TxtRegistryGap` | Record exists in Cloudflare with no ownership TXT |
| `RecordAdopted` | Successful adoption of a record from an import owner |
| `DNSReconciled` | DNS record created or updated successfully |
