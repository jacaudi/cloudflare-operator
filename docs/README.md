# Cloudflare Operator CRD Reference

This document describes the Custom Resource Definitions (CRDs) provided by the cloudflare-operator.

## CRDs

### CloudflareZone

Manages a Cloudflare DNS zone (domain). Creates, monitors, and optionally deletes zones in Cloudflare.

#### Spec Fields

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | string | Yes | | Domain name to onboard (e.g., `example.com`) |
| `accountID` | string | Yes | | Cloudflare Account ID |
| `type` | string | No | `full` | Zone type: `full`, `partial`, or `secondary` |
| `paused` | bool | No | | Whether the zone is paused |
| `deletionPolicy` | string | No | `Retain` | `Retain` leaves the zone in Cloudflare on CR deletion; `Delete` removes it |
| `secretRef` | object | Yes | | Reference to a Secret containing Cloudflare API credentials |
| `interval` | duration | No | `30m` | Reconciliation interval |

#### Status Fields

| Field | Type | Description |
|-------|------|-------------|
| `zoneID` | string | Cloudflare Zone ID |
| `status` | string | Zone status (`initializing`, `pending`, `active`, `moved`) |
| `nameServers` | []string | Cloudflare-assigned nameservers |
| `originalNameServers` | []string | Nameservers before migration |
| `originalRegistrar` | string | Registrar at time of onboarding |
| `activatedOn` | time | When the zone became active |
| `lastSyncedAt` | time | Last successful sync |

---

### CloudflareDNSRecord

Manages individual DNS records within a Cloudflare zone.

#### Spec Fields

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `zoneID` | string | No | | Cloudflare Zone ID. Mutually exclusive with `zoneRef` |
| `zoneRef` | object | No | | Reference to a `CloudflareZone` CR. Mutually exclusive with `zoneID` |
| `name` | string | Yes | | DNS record name (e.g., `example.com`, `sub.example.com`) |
| `type` | string | Yes | | Record type: `A`, `AAAA`, `CNAME`, `SRV`, `MX`, `TXT`, `NS` |
| `content` | string | No | | Record content (IP, hostname, etc.). Mutually exclusive with `dynamicIP` |
| `dynamicIP` | bool | No | `false` | Enable automatic external IP resolution (type A only). Mutually exclusive with `content` |
| `ttl` | int | No | `1` | Time-to-live in seconds. `1` = automatic |
| `proxied` | bool | No | | Whether the record is proxied through Cloudflare |
| `srvData` | object | No | | SRV-specific record data. Required when type is `SRV` |
| `priority` | int | No | | Record priority (for MX and SRV records) |
| `secretRef` | object | Yes | | Reference to a Secret containing Cloudflare API credentials |
| `interval` | duration | No | `5m` | Reconciliation interval for drift detection |

---

### CloudflareZoneConfig

Manages zone-level settings (SSL, security, performance, network, bot management) for a Cloudflare zone.

#### Spec Fields

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `zoneID` | string | No | | Cloudflare Zone ID. Mutually exclusive with `zoneRef` |
| `zoneRef` | object | No | | Reference to a `CloudflareZone` CR. Mutually exclusive with `zoneID` |
| `secretRef` | object | Yes | | Reference to a Secret containing Cloudflare API credentials |
| `interval` | duration | No | `30m` | Reconciliation interval |
| `ssl` | object | No | | SSL/TLS settings (mode, minTLSVersion, tls13, alwaysUseHTTPS, etc.) |
| `security` | object | No | | Security settings (securityLevel, challengeTTL, browserCheck, etc.) |
| `performance` | object | No | | Performance settings (cacheLevel, browserCacheTTL, minify, polish, etc.) |
| `network` | object | No | | Network settings (ipv6, websockets, pseudoIPv4, etc.) |
| `botManagement` | object | No | | Bot management settings (enableJS, fightMode) |

---

### CloudflareRuleset

Manages Cloudflare Rulesets (WAF custom rules, redirects, transforms, etc.) for a zone.

#### Spec Fields

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `zoneID` | string | No | | Cloudflare Zone ID. Mutually exclusive with `zoneRef` |
| `zoneRef` | object | No | | Reference to a `CloudflareZone` CR. Mutually exclusive with `zoneID` |
| `name` | string | Yes | | Human-readable name for the ruleset |
| `description` | string | No | | Informative description of the ruleset |
| `phase` | string | Yes | | Ruleset phase (e.g., `http_request_firewall_custom`, `http_request_redirect`) |
| `rules` | []object | Yes | | List of rules (action, expression, description, enabled, actionParameters) |
| `secretRef` | object | Yes | | Reference to a Secret containing Cloudflare API credentials |
| `interval` | duration | No | `30m` | Reconciliation interval |

---

## Common Patterns

### Zone Reference

Instead of hardcoding a `zoneID`, you can reference a `CloudflareZone` CR:

```yaml
# Instead of:
spec:
  zoneID: "<zone-id>"

# Use:
spec:
  zoneRef:
    name: my-zone
```

The controller resolves the zone ID from the `CloudflareZone` resource's `status.zoneID`. If the referenced zone doesn't exist or isn't ready yet, the dependent resource sets `Ready=False` and retries every 30 seconds.

`zoneID` and `zoneRef` are mutually exclusive -- specify exactly one.

### Secret Reference

All CRDs require a `secretRef` pointing to a Kubernetes Secret containing Cloudflare API credentials:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: cloudflare-api-token
type: Opaque
stringData:
  CLOUDFLARE_API_TOKEN: "<your-api-token>"
```
