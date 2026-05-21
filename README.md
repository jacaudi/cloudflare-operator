# Cloudflare Operator

A Kubernetes operator for managing Cloudflare DNS records, Zones, Rulesets, and
Cloudflare Tunnels declaratively using Custom Resources.

## Disclaimer

**Unofficial — community project.** This is not an official Cloudflare
product and is not endorsed by or affiliated with Cloudflare, Inc.
The operator implements its Cloudflare API access on top of the
official [cloudflare/cloudflare-go](https://github.com/cloudflare/cloudflare-go)
Go SDK; the Cloudflare name and trademarks belong to Cloudflare, Inc.
Use at your own discretion.

**Agentically generated.** This codebase was produced through agentic,
spec-driven development: each feature began as a written design and
implementation spec, then a coding agent executed the plan under human
review. Tests, code review, and CI gates apply as they would for any
project, but the authorship pattern is not a single human contributor —
keep that in mind when evaluating fit for your environment.

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
| `CloudflareDNSRecord` | DNS records (A / AAAA / CNAME / TXT / …) with Observe + Managed modes |
| `CloudflareTunnel` | Cloudflare Tunnel + cloudflared dataplane Deployment |

## Installation

### Helm (recommended)

```bash
# Replace <version> with a tagged chart release (e.g. 0.1.0).
helm install cloudflare-operator oci://ghcr.io/jacaudi/charts/cloudflare-operator \
  --version <version> \
  --namespace cloudflare-system \
  --create-namespace
```

The chart ships the operator and the v2alpha1 CRDs. See
[`chart/README.md`](chart/README.md) for the full value reference.

### Local development

```bash
# Regenerate CRD bundles from the Go types under api/v2alpha1/.
# Outputs to bin/crd-staging/ (gitignored) and copies into chart/templates/.
make generate

# Apply the regenerated CRDs into the current kube context.
kubectl apply -f bin/crd-staging/

# Run the operator locally against the current kube context.
go run ./cmd/manager
```

To install the chart from a local checkout instead of OCI:

```bash
helm install cloudflare-operator ./chart \
  --namespace cloudflare-system \
  --create-namespace
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
  # Required: the operator's Secret cache is label-scoped.
  labels:
    app.kubernetes.io/part-of: cloudflare-operator
type: Opaque
stringData:
  token: "your-cloudflare-api-token"
  accountID: "your-cloudflare-account-id"
```

> **Why the label?** The operator's Secret cache is label-scoped to objects
> carrying `app.kubernetes.io/part-of: cloudflare-operator` so it avoids a
> cluster-wide LIST/WATCH on every Secret. Unlabeled credential Secrets are
> invisible to the operator and credential resolution fails with
> `ErrSecretNotFound`.

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
      httpStatus: 404      # served when no source matches
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

See the [`examples/`](examples/) directory for ready-to-apply CR manifests
covering every CRD plus the annotation-driven attachment patterns
(Gateway, HTTPRoute, Service).

---

## Documentation

| Page | Covers |
|------|--------|
| [chart/README.md](chart/README.md) | Helm chart values reference (auto-generated from `chart/values.yaml`). |
| [docs/adopting-existing-records.md](docs/adopting-existing-records.md) | Safe-adopt flow with TXT-companion verification: Observe-mode reconnaissance, the `AdoptRefusedNoTXT` / `AdoptRefusedForeign` safety net, the migration path for pre-companion records |
| [docs/crd-reference.md](docs/crd-reference.md) | Field-by-field reference for all 5 CRDs and their sub-types (auto-generated from the `api/v2alpha1` Go types) |
| [docs/annotations.md](docs/annotations.md) | Full operator-read annotation reference: every `cloudflare.io/*` annotation, where it's settable, the inheritance precedence chain, truthy-value vocabulary |
| [docs/credentials.md](docs/credentials.md) | The `(API token, account ID)` model end-to-end: token Secret shape, the `part-of` label requirement, inline vs Secret-backed account ID, rotation semantics, common errors |
| [docs/gateway-api.md](docs/gateway-api.md) | End-to-end Gateway-API integration: Gateway opt-in, HTTPRoute / TLSRoute attachment, per-Route overrides, cascade-GC, generated-object inventory, common gotchas |
| [docs/reconciliation.md](docs/reconciliation.md) | Reconcile cadence, `Phase=Error` retry semantics, the `cloudflare.io/reconcile-at` force-reconcile annotation |
| [docs/troubleshooting.md](docs/troubleshooting.md) | Field guide for diagnosing the operator: Status → Conditions → Logs → Events flow, the `Ready=False` reason vocabulary mapped to fixes, the verify-reconcile-actually-ran annotation-ack trick |
| [docs/tunnels.md](docs/tunnels.md) | `CloudflareTunnel` deep-dive: direct-create vs auto-created, the 52-char naming budget, the cloudflared dataplane sizing knobs, the cloudflared image override precedence chain, the Status surface, cascade-GC rules |
| [docs/txt-registry.md](docs/txt-registry.md) | TXT companion ownership-marking: the `cf-txt.<hostname>` companion shape, the JSON payload format (or AES-256-GCM `v1:nonce:ciphertext` envelope), enabling encryption via `TxtRegistryKeySecretRef`, rolling between plaintext + AES, the engineering migration procedure |

## Acknowledgements

This project stands on the shoulders of giants:

- **[bjw-s](https://github.com/bjw-s)** — for the [helm-charts](https://github.com/bjw-s-labs/helm-charts) common library that powers this operator's Helm chart. The common library pattern keeps the chart small and consistent with the rest of the ecosystem.
- **[cloudflare/cloudflare-go](https://github.com/cloudflare/cloudflare-go)** — the official Cloudflare Go SDK that backs every API call this operator makes.
- **[kubernetes-sigs/controller-runtime](https://github.com/kubernetes-sigs/controller-runtime)** — the operator framework: managers, caches, watches, leader election, and the reconcile loop semantics this operator builds on.
- **[Kubernetes Gateway API](https://gateway-api.sigs.k8s.io/)** — the HTTPRoute / TLSRoute / Gateway primitives the tunnel-source controllers attach to.

## License

Licensed under the [MIT License](LICENSE).
