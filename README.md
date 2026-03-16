# cloudflare-operator

A Kubernetes operator that manages Cloudflare resources declaratively using Custom Resources. Define DNS records, tunnels, WAF rulesets, zone settings, and zone lifecycle as Kubernetes objects with automatic drift detection and reconciliation.

## Custom Resources

| CRD | Description |
|-----|-------------|
| `CloudflareZone` | Onboard and manage domain lifecycle (create, adopt, activate, delete) |
| `CloudflareDNSRecord` | Manage DNS records (A, AAAA, CNAME, SRV, MX, TXT, NS) with dynamic IP support |
| `CloudflareTunnel` | Create and manage Cloudflare Tunnels with auto-generated credentials |
| `CloudflareZoneConfig` | Declaratively configure zone settings (SSL, security, performance, network) |
| `CloudflareRuleset` | Manage WAF rulesets and firewall rules across 14+ phases |

## Quick Start

### Prerequisites

- Kubernetes cluster v1.11.3+
- kubectl configured
- Cloudflare API token with appropriate permissions

### Install

```sh
# Install CRDs
make install

# Deploy the operator
make deploy IMG=<your-registry>/cloudflare-operator:latest
```

### Create a Secret

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: cloudflare-api-token
type: Opaque
stringData:
  apiToken: "<your-cloudflare-api-token>"
```

### Create a DNS Record

```yaml
apiVersion: cloudflare.io/v1alpha1
kind: CloudflareDNSRecord
metadata:
  name: my-record
spec:
  zoneID: "<zone-id>"
  name: "app.example.com"
  type: A
  dynamicIP: true
  proxied: true
  ttl: 1
  interval: 5m
  secretRef:
    name: cloudflare-api-token
```

### Verify

```sh
kubectl get cloudflarednsrecords
kubectl get cloudflarezones
kubectl get cloudflaretunnels
kubectl get cloudflarerulesets
kubectl get cloudflarezoneconfigs
```

## Uninstall

```sh
kubectl delete -k config/samples/   # Remove CRs
make uninstall                       # Remove CRDs
make undeploy                        # Remove operator
```

## Documentation

Full documentation including all CRD specifications, configuration options, and examples is available at [docs/README.md](docs/README.md).

## License

Copyright 2026. Licensed under the Apache License, Version 2.0. See [LICENSE](http://www.apache.org/licenses/LICENSE-2.0) for details.
