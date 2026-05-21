# Cloudflare Kubernetes Operator

A Kubernetes operator for managing Cloudflare DNS records, Zones, Rulesets, and
Cloudflare Tunnels declaratively using Custom Resources.

## Disclaimer

**Unofficial — community project.** This is not an official Cloudflare
product and is not endorsed by or affiliated with Cloudflare, Inc.
The operator implements its Cloudflare API access on top of the
official [cloudflare/cloudflare-go](https://github.com/cloudflare/cloudflare-go)
Go SDK; the Cloudflare name and trademarks belong to Cloudflare, Inc.
Use at your own discretion.

## Features

- Manage Cloudflare DNS, Zones, and Rulesets as Kubernetes resources
- Spin up Cloudflare Tunnels (tunnel + cloudflared pods + remote routing) from a single CR
- Attach tunnels to Gateway API objects and Services by adding one annotation
- Each CR can use its own Cloudflare credentials
- Adopt existing Cloudflare records safely (Observe mode reads, Managed mode writes)

## Custom Resources

| CRD | Description |
|-----|-------------|
| `CloudflareZone` | Cloudflare zone settings + zone-level metadata |
| `CloudflareZoneConfig` | Per-zone configuration knobs (security, performance, TLS) |
| `CloudflareRuleset` | WAF / transform / redirect / origin rulesets |
| `CloudflareDNSRecord` | DNS records (A / AAAA / CNAME / TXT / …) with adopt + observe |
| `CloudflareTunnel` | Cloudflare Tunnel + cloudflared dataplane Deployment |

## Installation

### Helm (recommended)

```bash
# Install from OCI registry.
helm install cloudflare-operator oci://ghcr.io/jacaudi/charts/cloudflare-operator \
  --namespace cloudflare-system \
  --create-namespace
```

The chart ships the meta-operator (which manages zone + tunnel controllers as
sub-operators) and the v2alpha1 CRDs. See [`chart/README.md`](chart/README.md)
for the full value reference + behavior-change notes.

### Local development

```bash
# Install CRDs into the current kube context.
make install

# Deploy the operator into the current context.
make deploy
```

## Quick Start

After the operator is installed:

### 1. Create a Cloudflare credentials Secret

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: cloudflare-credentials
  namespace: cloudflare-system
  # Required from v0.x onward: the operator's Secret cache is label-scoped.
  labels:
    app.kubernetes.io/part-of: cloudflare-operator
type: Opaque
stringData:
  token: "your-cloudflare-api-token"
  accountID: "your-cloudflare-account-id"
```

> **Why the label?** Slice 2 (finding C) scoped the operator's Secret cache to
> objects carrying `app.kubernetes.io/part-of: cloudflare-operator`. Unlabeled
> credential Secrets become invisible to the operator and credential resolution
> fails with `ErrSecretNotFound`. See
> [`chart/README.md`](chart/README.md#cost-reductions-simplify-slice-2--c-e-f-g-h-2026-05).

### 2. Declare a DNS record

```yaml
apiVersion: cloudflare.io/v2alpha1
kind: CloudflareDNSRecord
metadata:
  name: example
  namespace: default
spec:
  name: app.example.com
  type: CNAME
  content: origin.example.net
  proxied: true
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

### 3. (Optional) Declare a Cloudflare Tunnel

```yaml
apiVersion: cloudflare.io/v2alpha1
kind: CloudflareTunnel
metadata:
  name: example-tunnel
  namespace: default
spec:
  name: example-tunnel
  cloudflare:
    tokenSecretRef:
      name: cloudflare-credentials
      namespace: cloudflare-system
      key: token
    accountIDSecretRef:
      name: cloudflare-credentials
      namespace: cloudflare-system
      key: accountID
  connector:
    replicas: 2
    protocol: auto
  routing:
    fallback:
      httpStatus: 404
```

### 4. Apply + check

```bash
kubectl apply -f credentials-secret.yaml -f dnsrecord.yaml
kubectl get cloudflarednsrecord example -o yaml
kubectl get cloudflaretunnel example-tunnel -o yaml
```

The status block carries `Ready` / reconciliation timestamps / the
Cloudflare resource IDs once the loop converges.

### Forcing an immediate reconcile

If a CR is sitting in `Phase=Error` and you don't want to wait the default
30-minute interval, annotate it:

```bash
TOKEN=$(date -u +%Y-%m-%dT%H:%M:%SZ)
kubectl annotate cloudflaretunnel/example-tunnel \
  "cloudflare.io/reconcile-at=$TOKEN" --overwrite
```

See [docs/reconciliation.md](docs/reconciliation.md) for the full design
and the other levers available.

## Examples

See the [`config/samples/`](config/samples/) directory for complete CR
examples covering each CRD's full surface.

---

## Documentation

| Page | Covers |
|------|--------|
| [chart/README.md](chart/README.md) | Helm chart value reference + chronological behavior-change notes from every shipped slice |
| [docs/reconciliation.md](docs/reconciliation.md) | Reconcile cadence, `Phase=Error` retry semantics, the `cloudflare.io/reconcile-at` force-reconcile annotation |

## Acknowledgements

This project stands on the shoulders of giants:

- **[bjw-s](https://github.com/bjw-s)** — for the [helm-charts](https://github.com/bjw-s-labs/helm-charts) common library that powers this operator's Helm chart. The common library pattern keeps the chart small and consistent with the rest of the ecosystem.
- **[cloudflare/cloudflare-go](https://github.com/cloudflare/cloudflare-go)** — the official Cloudflare Go SDK that backs every API call this operator makes.
- **[kubernetes-sigs/controller-runtime](https://github.com/kubernetes-sigs/controller-runtime)** — the operator framework: managers, caches, watches, leader election, and the reconcile loop semantics this operator builds on.
- **[Kubernetes Gateway API](https://gateway-api.sigs.k8s.io/)** — the HTTPRoute / TLSRoute / Gateway primitives the tunnel-source controllers attach to.

## License

Apache 2.0
