# Cloudflare Tunnels

A `CloudflareTunnel` CR is the operator's atomic unit for an
operator-managed Cloudflare Tunnel: a Cloudflare-side tunnel (with
remote-config ingress rules), a cloudflared `Deployment` that connects
it, a metrics `Service`, and the connector-token `Secret`. This page
covers the standalone tunnel — the CR shape, what the operator
materializes, the cloudflared dataplane sizing knobs, and the cascade-GC
rules.

If you came here from the Quick Start's "(Optional) Declare a Cloudflare
Tunnel" step, you're in the right place. For the
annotation-driven attachment path (Gateway → tunnel auto-creation), read
[`gateway-api.md`](gateway-api.md) — the two pages share a lot of
ground, but this one is the CRD-first view.

## Two creation paths

The operator manages tunnels via two paths. They produce the same
runtime outcome (a tunnel + dataplane + ingress rules) but differ in
who owns the CR's lifecycle:

### Direct-create

You write a `CloudflareTunnel` CR yourself and `kubectl apply` it.
You own the CR's lifecycle — the operator never deletes it
automatically, even when no sources reference it.

```yaml
apiVersion: cloudflare.io/v2alpha1
kind: CloudflareTunnel
metadata:
  name: example-tunnel
spec:
  name: example-tunnel
  cloudflare:
    tokenSecretRef:
      name: cloudflare-credentials
      key: token
    accountIDSecretRef:
      name: cloudflare-credentials
      key: accountID
  connector:
    replicas: 2
    protocol: auto
```

Use when:

- You're not using Gateway API (or you want a tunnel that exists
  independent of any Gateway / Route).
- You want predictable lifecycle: the tunnel stays until you delete
  the CR.
- You're attaching arbitrary sources via the annotation flow but want
  the tunnel itself to be a first-class declared object (e.g. for
  GitOps tracking).

### Auto-created

The operator creates the CR for you when it observes a source object
(Gateway / Service) annotated with `cloudflare.io/tunnel: "true"`.
The CR carries `cloudflare.io/auto-created` to mark its provenance.

Use when:

- Gateway-API is your routing layer and you want the tunnel to follow
  the Gateway's lifecycle.
- You don't want to manage the tunnel CR by hand.
- You're comfortable with cascade-GC removing the tunnel when its
  last source detaches.

The full Gateway-API path is documented in
[`gateway-api.md`](gateway-api.md). The rest of this page applies to
both creation paths.

## The 52-character naming budget

`spec.name` is immutable after create AND capped at **52 characters**.
The cap exists because the operator derives several Kubernetes
resource names from it:

```
cloudflared-<spec.name>          (the Deployment + Pod)
cloudflared-<spec.name>-metrics  (the metrics Service)
cloudflared-<spec.name>-token    (the connector-token Secret)
```

The Kubernetes DNS-1123 label limit is 63 characters. With the
`cloudflared-` prefix (12 chars), `spec.name` can be up to 51 chars
for the longest derived name; the CRD-level cap is 52 to leave a
1-char margin (and because the Cloudflare API itself accepts up to 52
gracefully).

For auto-created tunnels the operator derives `spec.name` from the
source's namespace (post-Slice-5: dropped the `cf-` prefix; auto-name
is now `<namespace>[-<tunnel-name-annotation>]`). For direct-create,
you choose. Stay under 52 characters and you're fine.

> **Renames are not supported.** `spec.name` is immutable via CEL
> validation. The Cloudflare API treats `config_src` as write-once;
> a rename would orphan the cloudflared credential Secret and every
> DNS target pointing at `<old-tunnel-id>.cfargotunnel.com`. If you
> need a different name, delete the CR and create a fresh one.

## What the operator materializes

For each `CloudflareTunnel` CR, the operator creates:

| Object | Owns it | Why |
|---|---|---|
| Cloudflare-side tunnel (UUID) | Cloudflare account | The tunnel itself; carries the credential cloudflared needs. |
| Cloudflare-side remote config | Cloudflare account | The ingress rules cloudflared uses to route to backends. PUT on every reconcile when the desired snap differs from `Status.ObservedIngress`. |
| `Deployment` (cloudflared) | `CloudflareTunnel` CR | The dataplane Pods. Owner-ref → CR. |
| `Service` (cloudflared metrics) | `CloudflareTunnel` CR | Exposes cloudflared's `/metrics` for Prometheus. Owner-ref → CR. |
| `Secret` (connector token) | `CloudflareTunnel` CR | The Cloudflare-issued token cloudflared authenticates with. Owner-ref → CR. |

All four operator-side objects are GC'd by Kubernetes when the
`CloudflareTunnel` CR is deleted (owner-references). The Cloudflare-side
tunnel is deleted by the operator's finalizer.

## The cloudflared dataplane (`spec.connector`)

The `Connector` block configures the `Deployment` the operator creates.
All fields have sensible defaults; most installs only need to set
`replicas` and accept everything else.

| Field | Default | Range / Enum | What it does |
|---|---|---|---|
| `replicas` | `2` | 1–25 (no HPA) | Pod count. Two for HA; bump for additional throughput / connector-redundancy. |
| `protocol` | `auto` | `auto` / `http2` / `quic` | cloudflared's transport. `auto` picks based on network behavior. `quic` is fastest where it works; `http2` is the conservative fallback. |
| `logLevel` | `info` | `debug` / `info` / `warn` / `error` | cloudflared's `--loglevel`. Bump to `debug` to investigate, then revert. |
| `gracePeriodSeconds` | `30` | ≥0 | cloudflared's `--grace-period`. Pod `terminationGracePeriodSeconds` is set to this + 15. |
| `resources` | `50m/64Mi` requests, `200m/256Mi` limits | standard PodSpec.ResourceRequirements | Container resource bounds. Observed defaults; raise for high-throughput. |
| `nodeSelector` / `tolerations` / `affinity` / `topologySpreadConstraints` | unset | standard PodSpec | Scheduling controls; pass-through. |
| `originCASecretRef` | unset | `SecretReference` | When set, mounts the referenced Secret as a CA bundle cloudflared uses to validate origin certs. |
| `image` | compile-time pinned `cloudflare/cloudflared:<tag>` | `ConnectorImage` (repo + tag, both optional) | Cloudflared image override. See below. |

### Cloudflared image overrides

The operator ships with a compile-time pinned cloudflared tag
(Renovate-tracked; bumps land as `fix(cloudflared)` commits). You can
override at three levels — most specific wins:

1. **Per-CR**: `CloudflareTunnel.spec.connector.image` (per-axis).
2. **Chart-level default**: `controllers.tunnel.connector.image`
   (per-axis; the operator-managed default for auto-created tunnels).
3. **Operator compile-time pin** (default).

Per-axis means `repository` and `tag` override independently. Setting
only `repository: my-mirror.example.com/cloudflare/cloudflared` keeps
the compile-time pinned tag (repository-only mirror — Docker Hub
rate-limit mitigation pattern).

See the `cloudflared connector image` section in
[`chart/README.md`](../chart/README.md) for chart-side details.

## Routing defaults (`spec.routing`)

Two pieces:

### Fallback (the catch-all)

What cloudflared returns when no specific ingress rule matches the
incoming hostname. The operator auto-appends a catch-all at the end of
the ingress list; this field lets you override what that catch-all
does. Two mutually exclusive forms:

```yaml
spec:
  routing:
    fallback:
      httpStatus: 404      # synthetic status response
```

```yaml
spec:
  routing:
    fallback:
      url: "https://default.example.com"   # forward to a real backend
```

Default behavior (when `routing.fallback` is unset): cloudflared returns
HTTP 404 for unmatched hostnames.

### Origin-request defaults

Tunnel-wide defaults for the cloudflared `originRequest` block. Source
objects (Routes / Services) can override these via annotations
(`cloudflare.io/no-tls-verify`, `cloudflare.io/origin-server-name`).

```yaml
spec:
  routing:
    originRequest:
      noTLSVerify: false        # default: verify origin TLS
      # originServerName: "expected SAN on origin cert"
```

> **Heads up:** the CRD's `TunnelOriginRequest` shape currently
> models only `noTLSVerify` + `originServerName`. The full
> Cloudflared origin-request surface (`connectTimeout`,
> `keepAliveTimeout`, `tlsTimeout`, `http2Origin`, etc.) is not yet
> exposed via this CR. Backlog item for a future API revision.

## The Status surface

Watch these fields when something isn't behaving:

| Field | What it means |
|---|---|
| `Phase` | Pending / Reconciling / Ready / Error — coarse summary. See [`troubleshooting.md`](troubleshooting.md). |
| `Conditions[Ready]` | The detailed Ready condition; `reason` + `message` carry the why. |
| `Conditions[ConnectorReady]` | Cloudflared connector pods are actively connected to Cloudflare's edge. False can mean pods crashed, egress is blocked, or the connector token is invalid. |
| `Conditions[RemoteConfigApplied]` | The last `/configurations` PUT to Cloudflare succeeded (matches `ObservedIngress`). |
| `TunnelID` | The Cloudflare-assigned UUID. Populated after first successful create. |
| `TunnelCNAME` | `<tunnelID>.cfargotunnel.com`. The DNS target that records point at. |
| `ConnectionsHealthy` | Count of active connectors observed via Cloudflare's API. Zero means no healthy connectors — usually pairs with `ConnectorReady=False`. |
| `ObservedIngress` | The last successfully applied ingress list. Used for the G optimization in Slice 2 (skip GetConfiguration when `wantSnap == ObservedIngress`). |
| `ObservedDataplaneDeploymentHash` / `ObservedDataplaneServiceHash` | sha256 of the last successfully applied Deployment / Service. Used for the H optimization (skip Apply when nothing changed). |
| `AttachedSources` | Lexicographically-sorted list of sources (Gateways / Routes / Services) currently contributing ingress rules to this tunnel. Informational. |
| `LastSyncedAt` | Wall-clock of the last successful reconcile. |
| `LastReconcileToken` | Acked `cloudflare.io/reconcile-at` annotation value. See [`reconciliation.md`](reconciliation.md). |

## Cascade-GC

Auto-created tunnel CRs are eligible for **cascade-GC**: when their
last source detaches and the CR carries
`cloudflare.io/auto-created: "true"` (OR the standard source-owner
labels the operator stamps), the operator self-deletes the CR. The
deletion cascades to the Deployment / Service / Secret via
owner-references; the Cloudflare-side tunnel is removed by the
operator's finalizer.

Direct-create CRs (lacking `cloudflare.io/auto-created`) are NEVER
cascade-GC'd. Even when `Status.AttachedSources` is empty, the
operator keeps the tunnel up. You own its lifecycle.

To check whether a given tunnel is cascade-GC-eligible:

```sh
kubectl get cloudflaretunnel/<name> -n <ns> \
  -o jsonpath='{.metadata.annotations.cloudflare\.io/auto-created}{"\n"}'
```

If the output is `true`, cascade-GC is in play. Empty / absent means
direct-create and you delete it yourself.

## Common patterns + gotchas

### "The tunnel says Ready=True but no traffic flows"

`Ready=True` only requires `RemoteConfigApplied=True` and (typically)
`ConnectorReady=True`. It does NOT verify that cloudflared can reach
your backends. Check:

- `kubectl get pods -n <ns> -l app=cloudflared-<name>` — pods running?
- `kubectl logs -n <ns> -l app=cloudflared-<name>` — any backend
  errors?
- DNS-side: `dig <hostname>` from outside the cluster — does it
  resolve to the tunnel CNAME?

### "ConnectorReady stays False"

Most likely cloudflared can't reach Cloudflare's edge. Check:

- Pod logs for "Unable to connect to Cloudflare edge" or similar.
- NetworkPolicy egress to `*.argotunnel.com`, `*.cloudflareclient.com`.
- The connector-token Secret — `kubectl get secret -n <ns>
  cloudflared-<name>-token` — should exist with a non-empty `token`
  key.

### "Two CRs want the same tunnel"

Don't. The Cloudflare API treats tunnel names as unique within an
account. Two CRs (or two operators) trying to create the same name
will collide; one wins, the other surfaces `Error`. Pick distinct
names.

### "I want to scale connector pods up"

Set `spec.connector.replicas`. There's no HPA — the operator doesn't
auto-scale based on traffic. If you need that, layer a separate HPA
manually on the Deployment the operator creates (the Deployment's
selector labels are stable, so a manual HPA is durable across
reconciles).

### "Cloudflared is OOM-killed"

Bump `spec.connector.resources.limits.memory`. The default 256Mi is
fine for low-traffic; high-throughput tunnels (QUIC, many backends,
many concurrent connections) can need more.

## Related

- [`gateway-api.md`](gateway-api.md) — annotation-driven attachment +
  the auto-created lifecycle.
- [`annotations.md`](annotations.md) — `cloudflare.io/tunnel`,
  `cloudflare.io/tunnel-name`, `cloudflare.io/auto-created` reference.
- [`reconciliation.md`](reconciliation.md) — when the next reconcile
  fires + the force-reconcile annotation.
- [`troubleshooting.md`](troubleshooting.md) — `Phase=Error` and
  `ConnectorReady=False` diagnostic flow.
- [`credentials.md`](credentials.md) — credential resolution.
- [`examples/cloudflare_v2alpha1_cloudflaretunnel.yaml`](../examples/cloudflare_v2alpha1_cloudflaretunnel.yaml) — direct-create sample.
