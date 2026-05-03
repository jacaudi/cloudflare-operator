# cloudflare-operator

A Kubernetes operator that manages Cloudflare resources declaratively via Custom Resources. Define DNS records, tunnels, security + transform rulesets, zone settings, and zone lifecycle as Kubernetes objects with drift detection and automatic reconciliation.

> **Unofficial — community project.** This is not an official Cloudflare product and is not endorsed by or affiliated with Cloudflare, Inc. The operator implements its Cloudflare API access on top of the official [`cloudflare/cloudflare-go`](https://github.com/cloudflare/cloudflare-go) Go SDK; the Cloudflare name and trademarks belong to Cloudflare, Inc. Use at your own discretion.

## Custom Resources

| CRD | Purpose |
|-----|---------|
| `CloudflareZone` | Onboard and manage domain lifecycle (create, adopt, activate, delete) |
| `CloudflareDNSRecord` | Manage DNS records (A, AAAA, CNAME, SRV, MX, TXT, NS) with dynamic IP support |
| `CloudflareTunnel` | Create tunnels, auto-generate `cloudflared` credentials Secrets, and (optionally) reconcile the operator-managed cloudflared Deployment + ingress config |
| `CloudflareTunnelRule` | Author a single hostname → backend ingress rule for a tunnel (also emitted automatically by the Gateway API / Service source controllers) |
| `CloudflareZoneConfig` | Declaratively configure zone settings (SSL, security, performance, network, DNS) |
| `CloudflareRuleset` | Manage a zone's phase entrypoint ruleset (security / custom rules, rate limiting, transforms, redirects, …) across 14+ Rulesets-Engine phases |

For end-to-end walkthroughs and topical guides, see [`docs/README.md`](docs/README.md). For field-by-field specs, see [`docs/crd-reference.md`](docs/crd-reference.md).

## Quickstart

New to cloudflare-operator? Start here: **[docs/domain-onboarding.md](docs/domain-onboarding.md)** — an end-to-end walkthrough from creating an API token to a first workload served via tunnel.

Already familiar with the CRDs? See:
- [Gateway API + Service annotations](docs/gateway-api-source.md) — the primary user interface in v1.
- [Tunnels](docs/tunnels.md) — tunnel CRDs and the operator-managed cloudflared runtime.
- [CRD reference](docs/crd-reference.md) — every field on every CRD.
- [Migrating from external-dns](docs/external-dns-migration.md).
- [Troubleshooting](docs/troubleshooting.md).

## Installation

### Prerequisites

- Kubernetes 1.28+
- Helm 3.8+ (for OCI chart support)
- A [Cloudflare API token](https://dash.cloudflare.com/profile/api-tokens) with permissions for the resources you plan to manage (see [authentication](docs/crd-reference.md#authentication))

### 1. Install the operator

The Helm chart is published as an OCI artifact to GHCR. It installs the CRDs, the controller Deployment, and RBAC.

```sh
helm install cloudflare-operator \
  oci://ghcr.io/jacaudi/charts/cloudflare-operator \
  --version 0.8.0 \
  --namespace cloudflare-operator \
  --create-namespace
```

Override defaults with `--set` or `-f values.yaml`. Common values (see [`chart/values.yaml`](chart/values.yaml)):

```yaml
image:
  tag: ""               # defaults to chart appVersion
controller:
  replicas: 1
leaderElection:
  enabled: true         # required if replicas > 1
metrics:
  serviceMonitor:
    enabled: false      # set true if you run the Prometheus Operator
```

### 2. Create the credentials Secret

```sh
kubectl create secret generic cloudflare-api-token \
  --namespace cloudflare-operator \
  --from-literal=apiToken=<your-cloudflare-api-token> \
  --from-literal=accountID=<your-cloudflare-account-id>

kubectl label secret cloudflare-api-token \
  --namespace cloudflare-operator \
  cloudflare.io/managed=true
```

Every CR references this Secret via `secretRef.name`. Place the Secret in the same namespace as the CRs that use it. `accountID` is required for `CloudflareZone` and `CloudflareTunnel`; other CRs only read `apiToken`.

The `cloudflare.io/managed=true` label is required: the operator's manager cache filters Secrets by this label so it only loads Secrets you've explicitly opted in. A Secret without the label produces `Ready=False` with `Reason=SecretNotLabeled` on any CR that references it. To stage a migration across many existing Secrets, set the chart value `secretCacheLabelSelector: ""` (or the env `SECRET_CACHE_LABEL_SELECTOR=""`) to disable the filter, label your Secrets, then restore the default. The operator-owned tunnel credentials Secret is auto-labeled.

### 3. Onboard your zone

`CloudflareZone` both creates new zones and adopts existing ones, so this works whether the domain is already in Cloudflare or not. Other CRs reference it via `zoneRef` instead of a raw zone ID.

```yaml
apiVersion: cloudflare.io/v1alpha1
kind: CloudflareZone
metadata:
  name: example-com
  namespace: cloudflare-operator
spec:
  name: "example.com"
  deletionPolicy: Retain   # leaves the zone in Cloudflare on CR delete
  secretRef:
    name: cloudflare-api-token
```

```sh
kubectl apply -f zone.yaml
kubectl get cloudflarezone -n cloudflare-operator
```

For new zones, `status.nameServers` lists the nameservers to configure at your registrar. `Ready=True` once the zone is active.

### 4. Create a DNS record

```yaml
apiVersion: cloudflare.io/v1alpha1
kind: CloudflareDNSRecord
metadata:
  name: homelab
  namespace: cloudflare-operator
spec:
  zoneRef:
    name: example-com      # the CloudflareZone above (same namespace)
  name: "home.example.com"
  type: A
  dynamicIP: true          # auto-resolves and tracks your external IP
  proxied: true
  ttl: 1                   # automatic
  interval: 5m             # drift-check cadence
  secretRef:
    name: cloudflare-api-token
```

```sh
kubectl apply -f dns-record.yaml
kubectl describe cloudflarednsrecord homelab -n cloudflare-operator
```

`Ready=True` means the record is in sync with Cloudflare. Prefer `zoneRef` — the controller resolves the zone ID from status and waits for the zone to be ready. `zoneID: "<id>"` is still supported for standalone cases.

More examples — CNAME, SRV, tunnels, rulesets, zone settings — live in [`config/samples/`](config/samples) and [`docs/crd-reference.md`](docs/crd-reference.md).

## Upgrading

```sh
helm upgrade cloudflare-operator \
  oci://ghcr.io/jacaudi/charts/cloudflare-operator \
  --version <new-version> \
  --namespace cloudflare-operator
```

Helm does not upgrade CRDs on `helm upgrade`. When a release changes CRD schemas, reapply them first:

```sh
helm pull oci://ghcr.io/jacaudi/charts/cloudflare-operator --version <new-version> --untar
kubectl apply -f cloudflare-operator/crds/
```

Check [`CHANGELOG.md`](CHANGELOG.md) for breaking changes before upgrading.

## Uninstall

```sh
kubectl delete cloudflarednsrecord,cloudflarezone,cloudflaretunnel,cloudflarezoneconfig,cloudflareruleset --all -A
helm uninstall cloudflare-operator --namespace cloudflare-operator
kubectl delete crd \
  cloudflarednsrecords.cloudflare.io \
  cloudflarezones.cloudflare.io \
  cloudflaretunnels.cloudflare.io \
  cloudflarezoneconfigs.cloudflare.io \
  cloudflarerulesets.cloudflare.io
```

Delete the CRs **before** uninstalling the chart so finalizers can run. `CloudflareZone` defaults to `deletionPolicy: Retain`, which leaves zones intact in Cloudflare.

## Development

Clone the repo and run the controller against your current kube context:

```sh
make install        # apply CRDs
make run            # run controller locally
make test           # unit tests (fake-client based)
make lint           # golangci-lint
```

Build and deploy a local image to a Kind cluster:

```sh
make docker-build IMG=cloudflare-operator:dev
kind load docker-image cloudflare-operator:dev
make deploy IMG=cloudflare-operator:dev
```

See [`AGENTS.md`](AGENTS.md) for project conventions (kubebuilder layout, generated files, controller patterns).

## Documentation

- [`docs/README.md`](docs/README.md) — topical-doc index (onboarding, Gateway API, tunnels, migration, troubleshooting)
- [`docs/crd-reference.md`](docs/crd-reference.md) — field-by-field CRD reference
- [`config/samples/`](config/samples) — runnable sample manifests for each CRD
- [`chart/values.yaml`](chart/values.yaml) — all Helm chart configuration options
- [`CHANGELOG.md`](CHANGELOG.md) — release notes

## License

Copyright 2026. Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE).

"Cloudflare" and the Cloudflare logo are trademarks of Cloudflare, Inc. This project is not endorsed by or affiliated with Cloudflare, Inc. Cloudflare API access is implemented via the official [`cloudflare/cloudflare-go`](https://github.com/cloudflare/cloudflare-go) Go SDK.
