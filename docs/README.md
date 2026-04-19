# cloudflare-operator Documentation

A Kubernetes operator for managing Cloudflare resources declaratively. All resources use the API group `cloudflare.io/v1alpha1`.

## Table of Contents

- [Authentication](#authentication)
- [CloudflareZone](#cloudflarezone)
- [CloudflareDNSRecord](#cloudflarednsrecord)
- [CloudflareTunnel](#cloudflaretunnel)
- [CloudflareZoneConfig](#cloudflarezoneconfig)
- [CloudflareRuleset](#cloudflareruleset)
- [Common Patterns](#common-patterns)

---

## Authentication

All resources reference a Kubernetes Secret containing a Cloudflare API token:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: cloudflare-api-token
type: Opaque
stringData:
  apiToken: "<your-cloudflare-api-token>"
```

Reference this secret in any resource via `secretRef`:

```yaml
secretRef:
  name: cloudflare-api-token
```

### Required API Token Permissions

| Resource | Permissions |
|----------|-------------|
| `CloudflareZone` | Zone:Edit, Zone:Read |
| `CloudflareDNSRecord` | DNS:Edit, Zone:Read |
| `CloudflareTunnel` | Argo Tunnel:Edit, Account Settings:Read |
| `CloudflareZoneConfig` | Zone Settings:Edit, Zone:Read |
| `CloudflareRuleset` | Zone WAF:Edit, Zone:Read |

---

## CloudflareZone

Manages domain lifecycle in Cloudflare: onboarding new domains, adopting existing ones, tracking activation status, and optional deletion.

### Spec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | string | Yes | | Domain name (e.g., `example.com`) |
| `accountID` | string | Yes | | Cloudflare Account ID |
| `type` | enum | No | `full` | Zone type: `full`, `partial`, `secondary` |
| `paused` | bool | No | | Pause zone (stop serving traffic through Cloudflare) |
| `deletionPolicy` | enum | No | `Retain` | `Retain` leaves zone in CF on CR deletion; `Delete` removes it |
| `secretRef` | object | Yes | | Reference to API token Secret |
| `interval` | duration | No | `30m` | Reconciliation interval |

### Status

| Field | Description |
|-------|-------------|
| `zoneID` | Cloudflare Zone ID |
| `status` | Zone status: `initializing`, `pending`, `active`, `moved` |
| `nameServers` | Cloudflare-assigned nameservers |
| `originalNameServers` | Nameservers before migration |
| `originalRegistrar` | Registrar at onboarding time |
| `activatedOn` | Timestamp when zone became active |

### Behavior

- **Adoption**: If a zone with the same domain already exists in the account, the operator adopts it rather than creating a duplicate.
- **Activation polling**: While `pending`, the operator triggers activation checks and requeues every 5 minutes for faster detection.
- **Ready condition**: `Ready=True` only when zone status is `active`. While `pending`, the condition message includes the nameservers to configure at your registrar.
- **Deletion policy**: `Retain` (default) removes the finalizer without touching Cloudflare. `Delete` actively removes the zone.

### Example

```yaml
apiVersion: cloudflare.io/v1alpha1
kind: CloudflareZone
metadata:
  name: my-domain
spec:
  name: "example.com"
  accountID: "<account-id>"
  type: "full"
  deletionPolicy: Retain
  interval: 30m
  secretRef:
    name: cloudflare-api-token
```

### Print Columns

```
NAME        DOMAIN        ZONE ID       STATUS    READY   AGE
my-domain   example.com   abc123...     active    True    5d
```

---

## CloudflareDNSRecord

Manages DNS records with support for dynamic IP resolution, SRV records, and automatic drift detection.

### Spec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `zoneID` | string | No | | Cloudflare Zone ID. Mutually exclusive with `zoneRef` |
| `zoneRef` | object | No | | Reference to a `CloudflareZone` CR. Mutually exclusive with `zoneID` |
| `name` | string | Yes | | Record name (e.g., `sub.example.com`) |
| `type` | enum | Yes | | `A`, `AAAA`, `CNAME`, `SRV`, `MX`, `TXT`, `NS` |
| `content` | string | No | | Record content. Mutually exclusive with `dynamicIP` |
| `dynamicIP` | bool | No | `false` | Auto-resolve external IP (type A only). Mutually exclusive with `content` |
| `ttl` | int | No | `1` | TTL in seconds. `1` = automatic |
| `proxied` | bool | No | | Whether to proxy through Cloudflare |
| `priority` | int | No | | Record priority (MX and SRV) |
| `srvData` | object | No | | SRV-specific data (required when type is SRV) |
| `secretRef` | object | Yes | | Reference to API token Secret |
| `interval` | duration | No | `5m` | Reconciliation interval for drift detection |

### SRV Data

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `service` | string | Yes | Service name (e.g., `_satisfactory`) |
| `proto` | enum | Yes | Protocol: `_tcp`, `_udp`, `_tls` |
| `priority` | int | Yes | SRV priority (0-65535) |
| `weight` | int | Yes | SRV weight (0-65535) |
| `port` | int | Yes | Target port (0-65535) |
| `target` | string | Yes | Target hostname |

### Status

| Field | Description |
|-------|-------------|
| `recordID` | Cloudflare DNS Record ID |
| `currentContent` | Current value of the record in Cloudflare |

### Behavior

- **Dynamic IP**: When `dynamicIP: true`, the operator resolves your external IP automatically and keeps the A record updated. Useful for home labs or environments with changing public IPs.
- **Drift detection**: On each reconciliation interval, the operator compares the desired state with Cloudflare and corrects any drift.
- **Record matching**: Finds existing records by name and type to avoid duplicates.

### Examples

```yaml
# Dynamic IP A record
apiVersion: cloudflare.io/v1alpha1
kind: CloudflareDNSRecord
metadata:
  name: root-a-record
spec:
  zoneID: "<zone-id>"
  name: "example.com"
  type: A
  dynamicIP: true
  proxied: true
  ttl: 1
  interval: 5m
  secretRef:
    name: cloudflare-api-token
---
# CNAME record
apiVersion: cloudflare.io/v1alpha1
kind: CloudflareDNSRecord
metadata:
  name: app-cname
spec:
  zoneID: "<zone-id>"
  name: "app.example.com"
  type: CNAME
  content: "example.com"
  proxied: true
  ttl: 1
  secretRef:
    name: cloudflare-api-token
---
# SRV record for game server
apiVersion: cloudflare.io/v1alpha1
kind: CloudflareDNSRecord
metadata:
  name: game-srv
spec:
  zoneID: "<zone-id>"
  name: "_game._udp.example.com"
  type: SRV
  srvData:
    service: "_game"
    proto: "_udp"
    priority: 10
    weight: 1
    port: 7777
    target: "game.example.com"
  ttl: 1
  secretRef:
    name: cloudflare-api-token
```

### Print Columns

```
NAME           RECORD NAME       TYPE    CONTENT        PROXIED   READY   AGE
root-a-record  example.com       A       203.0.113.1    true      True    5d
app-cname      app.example.com   CNAME   example.com    true      True    5d
```

---

## CloudflareTunnel

Manages Cloudflare Tunnel lifecycle and auto-generates a credentials Secret for use with `cloudflared`.

### Spec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `name` | string | Yes | | Tunnel name in Cloudflare |
| `accountID` | string | Yes | | Cloudflare Account ID |
| `generatedSecretName` | string | Yes | | Name of the Secret to create with tunnel credentials |
| `secretRef` | object | Yes | | Reference to API token Secret |
| `interval` | duration | No | `30m` | Reconciliation interval |

### Status

| Field | Description |
|-------|-------------|
| `tunnelID` | Cloudflare Tunnel ID |
| `tunnelCNAME` | Tunnel CNAME (`<tunnelID>.cfargotunnel.com`) |
| `credentialsSecretName` | Name of the generated credentials Secret |

### Behavior

- **Credential generation**: Automatically creates a Kubernetes Secret containing `credentials.json` with the tunnel token. Use this Secret to configure `cloudflared` deployments.
- **Tunnel CNAME**: The status exposes the tunnel CNAME, which you can use in DNS CNAME records to route traffic through the tunnel.
- **Adoption**: If a tunnel with the same name exists, the operator adopts it.

### Example

```yaml
apiVersion: cloudflare.io/v1alpha1
kind: CloudflareTunnel
metadata:
  name: k8s-tunnel
spec:
  name: k8s-external-ingress
  accountID: "<account-id>"
  generatedSecretName: cloudflare-tunnel-credentials
  interval: 30m
  secretRef:
    name: cloudflare-api-token
```

### Print Columns

```
NAME         TUNNEL NAME           TUNNEL ID     CNAME                                   READY   AGE
k8s-tunnel   k8s-external-ingress  abc123...     abc123.cfargotunnel.com                 True    5d
```

---

## CloudflareZoneConfig

Declaratively manages zone-level settings: SSL/TLS, security, performance, network, and bot management.

### Spec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `zoneID` | string | No | | Cloudflare Zone ID. Mutually exclusive with `zoneRef` |
| `zoneRef` | object | No | | Reference to a `CloudflareZone` CR. Mutually exclusive with `zoneID` |
| `secretRef` | object | Yes | | Reference to API token Secret |
| `interval` | duration | No | `30m` | Reconciliation interval |
| `ssl` | object | No | | SSL/TLS settings |
| `security` | object | No | | Security settings |
| `performance` | object | No | | Performance settings |
| `network` | object | No | | Network settings |
| `botManagement` | object | No | | Bot management settings |

### SSL Settings

| Field | Values | Description |
|-------|--------|-------------|
| `mode` | `off`, `flexible`, `full`, `strict` | SSL mode |
| `minTLSVersion` | `1.0`, `1.1`, `1.2`, `1.3` | Minimum TLS version |
| `tls13` | `on`, `off`, `zrt` | TLS 1.3 setting |
| `alwaysUseHTTPS` | `on`, `off` | Redirect HTTP to HTTPS |
| `automaticHTTPSRewrites` | `on`, `off` | Rewrite HTTP URLs in content |
| `opportunisticEncryption` | `on`, `off` | Opportunistic encryption |

### Security Settings

| Field | Values | Description |
|-------|--------|-------------|
| `securityLevel` | `essentially_off`, `low`, `medium`, `high`, `under_attack` | Security level |
| `challengeTTL` | `300`-`86400` | Challenge TTL in seconds |
| `browserCheck` | `on`, `off` | Browser integrity check |
| `emailObfuscation` | `on`, `off` | Email address obfuscation |

### Performance Settings

| Field | Values | Description |
|-------|--------|-------------|
| `cacheLevel` | `aggressive`, `basic`, `simplified` | Cache level |
| `browserCacheTTL` | int (0 = respect headers) | Browser cache TTL in seconds |
| `brotli` | `on`, `off` | Brotli compression |
| `earlyHints` | `on`, `off` | Early hints |
| `http2` | `on`, `off` | HTTP/2 |
| `http3` | `on`, `off` | HTTP/3 |
| `polish` | `off`, `lossless`, `lossy` | Image optimization |
| `minify.css` | `on`, `off` | CSS minification |
| `minify.html` | `on`, `off` | HTML minification |
| `minify.js` | `on`, `off` | JavaScript minification |

### Network Settings

| Field | Values | Description |
|-------|--------|-------------|
| `ipv6` | `on`, `off` | IPv6 support |
| `websockets` | `on`, `off` | WebSocket support |
| `pseudoIPv4` | `off`, `add_header`, `overwrite_header` | Pseudo IPv4 |
| `ipGeolocation` | `on`, `off` | IP geolocation headers |
| `opportunisticOnion` | `on`, `off` | Onion routing |

### Bot Management Settings

| Field | Type | Description |
|-------|------|-------------|
| `enableJS` | bool | JavaScript-based detection |
| `fightMode` | bool | Bot fight mode |

### Status

| Field | Description |
|-------|-------------|
| `appliedSettings` | Count of settings applied |

### Example

```yaml
apiVersion: cloudflare.io/v1alpha1
kind: CloudflareZoneConfig
metadata:
  name: zone-settings
spec:
  zoneID: "<zone-id>"
  interval: 30m
  secretRef:
    name: cloudflare-api-token
  ssl:
    mode: "full"
    minTLSVersion: "1.2"
    alwaysUseHTTPS: "on"
  security:
    securityLevel: "medium"
    browserCheck: "on"
  performance:
    cacheLevel: "aggressive"
    brotli: "on"
    http2: "on"
    http3: "on"
  network:
    ipv6: "on"
    websockets: "on"
  botManagement:
    fightMode: true
```

### Print Columns

```
NAME            ZONE ID       SETTINGS   READY   AGE
zone-settings   abc123...     18         True    5d
```

---

## CloudflareRuleset

Manages Cloudflare WAF rulesets with support for 14+ phases and free-form action parameters.

### Spec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `zoneID` | string | No | | Cloudflare Zone ID. Mutually exclusive with `zoneRef` |
| `zoneRef` | object | No | | Reference to a `CloudflareZone` CR. Mutually exclusive with `zoneID` |
| `name` | string | Yes | | Human-readable ruleset name |
| `description` | string | No | | Ruleset description |
| `phase` | enum | Yes | | Ruleset phase (see below) |
| `rules` | array | Yes | | List of rules (min 1) |
| `secretRef` | object | Yes | | Reference to API token Secret |
| `interval` | duration | No | `30m` | Reconciliation interval |

### Phases

| Phase | Description |
|-------|-------------|
| `http_request_firewall_custom` | Custom firewall rules |
| `http_request_firewall_managed` | Managed firewall rules |
| `http_request_late_transform` | Late request transforms |
| `http_request_redirect` | Redirects |
| `http_request_transform` | Request transforms |
| `http_response_headers_transform` | Response header transforms |
| `http_response_firewall_managed` | Response firewall rules |
| `http_config_settings` | Config settings |
| `http_custom_errors` | Custom error pages |
| `http_ratelimit` | Rate limiting |
| `http_request_cache_settings` | Cache settings |
| `http_request_origin` | Origin rules |
| `http_request_dynamic_redirect` | Dynamic redirects |
| `http_response_compression` | Response compression |

### Rule Spec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `action` | enum | Yes | | Action: `block`, `challenge`, `js_challenge`, `managed_challenge`, `log`, `skip`, `execute`, `redirect`, `rewrite`, `route`, `score`, `serve_error`, `set_cache_settings`, `set_config`, `compress_response`, `force_connection_close` |
| `expression` | string | Yes | | Wirefilter expression |
| `description` | string | No | | Rule description |
| `enabled` | bool | No | `true` | Whether rule is active |
| `actionParameters` | object | No | | Free-form action parameters (JSON) |

### Status

| Field | Description |
|-------|-------------|
| `rulesetID` | Cloudflare Ruleset ID |
| `ruleCount` | Number of rules in the ruleset |

### Example

```yaml
apiVersion: cloudflare.io/v1alpha1
kind: CloudflareRuleset
metadata:
  name: waf-custom-rules
spec:
  zoneID: "<zone-id>"
  name: "Custom WAF Rules"
  description: "Custom WAF rules for zone protection"
  phase: "http_request_firewall_custom"
  interval: 30m
  secretRef:
    name: cloudflare-api-token
  rules:
    - action: block
      expression: '(cf.client.bot) or (cf.threat_score gt 14)'
      description: "Block bots and high threat scores"
      enabled: true
    - action: block
      expression: '(not ip.geoip.country in {"CA" "US" "GB"})'
      description: "Block non-allowed countries"
      enabled: true
```

### Print Columns

```
NAME              RULESET NAME       PHASE                             RULES   READY   AGE
waf-custom-rules  Custom WAF Rules   http_request_firewall_custom      2       True    5d
```

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

`zoneID` and `zoneRef` are mutually exclusive -- specify exactly one. `zoneID` takes precedence if both are provided.

Supported on: `CloudflareDNSRecord`, `CloudflareZoneConfig`, `CloudflareRuleset`.

### All Resources Share

- **`secretRef`**: Reference to a Kubernetes Secret with an `apiToken` key
- **`interval`**: Reconciliation interval for drift detection
- **`Ready` condition**: Standard Kubernetes condition indicating sync status
- **`lastSyncedAt`**: Timestamp of last successful reconciliation
- **`observedGeneration`**: Tracks spec changes for efficient reconciliation

### Drift Detection

All resources periodically compare desired state (spec) with actual Cloudflare state and correct any differences. The `interval` field controls how frequently this check occurs.

### Status Conditions

Every resource reports standard Kubernetes conditions:

| Condition | Meaning |
|-----------|---------|
| `Ready=True` | Resource is synced and healthy |
| `Ready=False` | Resource has an error, is pending external state, or is actively being deleted. Check the `Reason` field (e.g. `ZonePending`, `CloudflareAPIError`, `ZoneRefNotReady`) for specifics. |

### Events

The operator emits Kubernetes events for key actions: resource creation, updates, sync failures, and deletion. View with `kubectl describe <resource>`.
