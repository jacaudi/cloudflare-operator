# Domain Onboarding

This walkthrough takes you from a fresh Cloudflare account credential to a working workload served through the operator. Read it top-to-bottom the first time; use the section headings as a reference afterward.

---

## 1. Create a Cloudflare API Token

cloudflare-operator uses a scoped API token, not your global API key. Create one at **Cloudflare Dashboard → My Profile → API Tokens → Create Token**.

Use "Create Custom Token" and grant the following permissions:

| Resource | Permission |
|---|---|
| Zone — Zone | Read |
| Zone — DNS | Edit |
| Zone — Zone Settings | Edit |
| Zone — Firewall Services | Edit |
| Account — Cloudflare Tunnel | Edit |

**Zone Resources:** select "All zones" or restrict to specific zones if you prefer least-privilege.

**Account Resources:** select your account.

Copy the token value immediately — Cloudflare shows it only once.

If you use multiple namespaces, place a copy of the Secret in each namespace that contains operator CRs. The operator resolves credentials from the Secret referenced by each CR, so the Secret must be co-located.

---

## 2. Provision the Credentials Secret

The Secret must carry two keys:

- `apiToken` — the Cloudflare API token from step 1.
- `accountID` — your Cloudflare Account ID (shown in the Cloudflare dashboard sidebar). Required by `CloudflareZone` and `CloudflareTunnel`; other CRs use only `apiToken`.

### Using External Secrets Operator (ESO)

If you use ESO, store both values in your secrets manager and create an `ExternalSecret`:

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: cloudflare-api-token
  namespace: cloudflare-operator
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault          # replace with your SecretStore name
    kind: SecretStore
  target:
    name: cloudflare-api-token
    creationPolicy: Owner
  data:
    - secretKey: apiToken
      remoteRef:
        key: cloudflare/operator
        property: apiToken
    - secretKey: accountID
      remoteRef:
        key: cloudflare/operator
        property: accountID
```

### Using kubectl

```bash
kubectl create secret generic cloudflare-api-token \
  --namespace cloudflare-operator \
  --from-literal=apiToken=<your-token> \
  --from-literal=accountID=<your-account-id>
```

### Same-namespace note

Each CRD field `secretRef.name` resolves the Secret from the **same namespace as the CR**. If you deploy CRs in multiple namespaces (e.g., `apps`, `network`), create the Secret in each namespace or use a namespace-crossing secrets distribution mechanism.

---

## 3. Zone Adoption

`CloudflareZone` onboards a domain into operator management. It works whether the zone already exists in Cloudflare or not.

```yaml
apiVersion: cloudflare.io/v1alpha1
kind: CloudflareZone
metadata:
  name: example-com          # CR name used by zoneRef in other CRs
  namespace: cloudflare-operator
spec:
  name: "example.com"        # must match the domain exactly
  type: full                 # "full" = Cloudflare is authoritative DNS
  deletionPolicy: Retain     # zone survives CR deletion
  secretRef:
    name: cloudflare-api-token
```

### `spec.name` matching

`spec.name` must exactly match the apex domain as it appears in Cloudflare (e.g., `example.com`, not `www.example.com`). The operator uses this value to look up the zone by name via the Cloudflare API; a mismatch results in a `ZoneNotFound` error.

### `deletionPolicy`

- `Retain` (default) — deleting the CR leaves the zone intact in Cloudflare. Strongly recommended.
- `Delete` — deleting the CR removes the zone from Cloudflare, including all DNS records. Use with care.

### `type`

- `full` — Cloudflare is the authoritative DNS for the domain. Standard setup.
- `partial` — CNAME setup; you keep your existing authoritative DNS and only proxy traffic through Cloudflare for specific records.

Apply the CR and check status:

```bash
kubectl apply -f zone.yaml
kubectl get cloudflarezone -n cloudflare-operator
```

For new zones, `status.nameServers` lists the Cloudflare nameservers to configure at your registrar. The operator waits for the zone to become `active` in Cloudflare before other CRs that reference it proceed.

```bash
kubectl describe cloudflarezone example-com -n cloudflare-operator
```

Look for `Ready=True` in the conditions output. Once ready, other CRs can reference this zone via `zoneRef.name: example-com`.

---

## 4. Apex A Record and the Dynamic IP Pattern

A common pattern: an apex `A` record pointing at your external IP, with a wildcard CNAME pointing at the apex. The operator's `dynamicIP: true` field keeps the `A` record in sync with your current external IP automatically.

```yaml
apiVersion: cloudflare.io/v1alpha1
kind: CloudflareDNSRecord
metadata:
  name: apex
  namespace: cloudflare-operator
spec:
  zoneRef:
    name: example-com
  name: "example.com"
  type: A
  dynamicIP: true        # operator resolves and tracks your external IP
  proxied: true
  ttl: 1                 # 1 = automatic TTL (Cloudflare-managed)
  secretRef:
    name: cloudflare-api-token
---
apiVersion: cloudflare.io/v1alpha1
kind: CloudflareDNSRecord
metadata:
  name: wildcard
  namespace: cloudflare-operator
spec:
  zoneRef:
    name: example-com
  name: "*.example.com"
  type: CNAME
  content: "example.com"  # points at the apex
  proxied: true
  ttl: 1
  secretRef:
    name: cloudflare-api-token
```

`dynamicIP: true` is only valid for `type: A`. The operator polls your external IP at the `interval` cadence (default `5m`) and updates the Cloudflare record on change. `content` and `dynamicIP` are mutually exclusive.

---

## 5. `CloudflareZoneConfig` — Declarative Zone Settings

`CloudflareZoneConfig` manages zone-level settings like SSL mode, security level, performance options, and caching. One CR per zone.

```yaml
apiVersion: cloudflare.io/v1alpha1
kind: CloudflareZoneConfig
metadata:
  name: example-com-config
  namespace: cloudflare-operator
spec:
  zoneRef:
    name: example-com
  secretRef:
    name: cloudflare-api-token
  ssl:
    mode: strict                    # off | flexible | full | strict
    minTLSVersion: "1.2"
    alwaysUseHTTPS: "on"
    automaticHTTPSRewrites: "on"
  security:
    securityLevel: medium           # essentially_off | low | medium | high | under_attack
    browserCheck: "on"
    emailObfuscation: "on"
```

The operator detects drift between the spec and the live zone settings and reconciles on the configured interval. Only fields present in the spec are managed; unspecified settings retain whatever is configured in Cloudflare.

A more complete `CloudflareZoneConfig` example with performance and network options:

```yaml
apiVersion: cloudflare.io/v1alpha1
kind: CloudflareZoneConfig
metadata:
  name: example-com-config
  namespace: cloudflare-operator
spec:
  zoneRef:
    name: example-com
  secretRef:
    name: cloudflare-api-token
  ssl:
    mode: strict
    minTLSVersion: "1.2"
    tls13: "on"
    alwaysUseHTTPS: "on"
    automaticHTTPSRewrites: "on"
    opportunisticEncryption: "on"
  security:
    securityLevel: medium
    browserCheck: "on"
    emailObfuscation: "on"
    challengeTTL: 1800
```

Check status:

```bash
kubectl get cloudflarezoneconfig example-com-config -n cloudflare-operator
kubectl describe cloudflarezoneconfig example-com-config -n cloudflare-operator
```

`Ready=True` indicates the live zone settings match the spec. If you change a setting in the Cloudflare dashboard, the operator detects the drift at the next reconcile and writes the spec value back.

### Dashboard-only settings

Several Cloudflare settings are not exposed via the API and therefore cannot be managed by the operator. These must be configured in the Cloudflare dashboard:

- **Bot Fight Mode** — basic bot protection toggle on the Security tab.
- **Block AI Bots** — AI crawler blocking.
- **Super Bot Fight Mode** — available on Pro plans and above.

The operator does not touch these settings and will not conflict with dashboard changes to them.

---

## 6. `CloudflareRuleset` — Security and Transform Rules

`CloudflareRuleset` manages a zone's phase entrypoint ruleset. Each CR corresponds to one ruleset phase (e.g., `http_request_firewall_custom`, `http_request_transform`, `http_ratelimit`).

```yaml
apiVersion: cloudflare.io/v1alpha1
kind: CloudflareRuleset
metadata:
  name: example-com-custom-rules
  namespace: cloudflare-operator
spec:
  zoneRef:
    name: example-com
  phase: http_request_firewall_custom
  secretRef:
    name: cloudflare-api-token
  rules:
    - description: "Block requests from known bad ASNs"
      expression: 'ip.geoip.asnum in {12345 67890}'
      action: block
      enabled: true
    - description: "Challenge non-browser traffic on /api"
      expression: 'http.request.uri.path matches "^/api" and not http.user_agent contains "Mozilla"'
      action: managed_challenge
      enabled: true
```

The operator reconciles the entire phase entrypoint ruleset on each sync. Rules are applied in the order listed in `spec.rules`. Drift detection compares a hash of the desired rules against the live state; only changed rulesets trigger an API call.

Supported phases include:
- `http_request_firewall_custom` — custom firewall rules
- `http_request_firewall_managed` — managed WAF rules (execute action)
- `http_ratelimit` — rate limiting
- `http_request_transform` — request header/URL transforms
- `http_response_headers_transform` — response header transforms
- `http_request_redirect` — URL redirects
- `http_request_cache_settings` — cache behavior overrides

Apply and check status:

```bash
kubectl apply -f ruleset.yaml
kubectl get cloudflareruleset -n cloudflare-operator
kubectl describe cloudflareruleset example-com-custom-rules -n cloudflare-operator
```

`Ready=True` indicates the live ruleset matches the spec. `Ready=False` with `reason=CloudflareAPIError` means the Cloudflare API rejected a rule — check the condition message for the specific error.

---

## 7. Resource Sizing Guidance

The operator itself is lightweight. A reasonable starting point for the operator Deployment:

```yaml
# In your Helm values file
controller:
  resources:
    requests:
      cpu: 50m
      memory: 128Mi
    limits:
      memory: 512Mi
```

If you manage a large number of zones or DNS records (hundreds of CRs), increase `requests.memory` to `256Mi` and watch actual usage. CPU limits are generally not beneficial for controllers — the controller processes events in bursts. If you set a CPU limit, avoid values below `50m`.

For the operator-managed cloudflared connector (when `spec.connector.enabled: true` on a `CloudflareTunnel`), cloudflared itself needs:

```yaml
spec:
  connector:
    resources:
      requests:
        cpu: 10m
        memory: 128Mi
      limits:
        memory: 256Mi
```

Scale the memory limit up if you route high-throughput traffic through the tunnel.

---

## 8. Flux and ArgoCD: Namespace and Dependency Ordering

### The namespace gotcha

`CloudflareZone` and `CloudflareTunnel` must reach `Ready=True` before other CRs that reference them can reconcile successfully. In GitOps setups, the ordering dependency is:

1. Namespace + credentials Secret
2. `CloudflareZone` (wait for `Ready=True`)
3. `CloudflareDNSRecord`, `CloudflareZoneConfig`, `CloudflareRuleset`, `CloudflareTunnel`

If CRs that reference a zone are applied before the zone is ready, they enter a wait/requeue loop. This is safe but produces `ZoneRefNotReady` events until the zone becomes active.

### Flux `Kustomization` ordering

Use `dependsOn` to enforce ordering:

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: cloudflare-zones
  namespace: flux-system
spec:
  path: ./clusters/my-cluster/cloudflare/zones
  interval: 10m
  prune: true
  sourceRef:
    kind: GitRepository
    name: flux-system
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: cloudflare-dns
  namespace: flux-system
spec:
  path: ./clusters/my-cluster/cloudflare/dns
  interval: 10m
  prune: true
  dependsOn:
    - name: cloudflare-zones
  sourceRef:
    kind: GitRepository
    name: flux-system
```

The `dependsOn` field causes Flux to wait for the `cloudflare-zones` Kustomization to reach `Ready=True` before applying `cloudflare-dns`. This prevents a flurry of requeue events on startup and makes status easier to read.

### ArgoCD `sync-wave`

Use sync waves to enforce ordering in ArgoCD:

```yaml
# On the CloudflareZone manifest
metadata:
  annotations:
    argocd.argoproj.io/sync-wave: "10"

# On dependent CRs
metadata:
  annotations:
    argocd.argoproj.io/sync-wave: "20"
```

ArgoCD applies resources with lower wave numbers first and waits for health before advancing waves.

---

## 9. Activating Annotation-Driven Sources (v1)

v1 adds two source controllers that automatically emit DNS records and tunnel ingress rules from annotations on `HTTPRoute` and `Service` objects. To activate them, set `TXT_OWNER_ID` in the operator configuration (via the `registry.txtOwnerID` Helm value).

```yaml
# Helm values
registry:
  txtOwnerID: "cloudflare-operator-prod"   # unique per operator instance
```

Without `TXT_OWNER_ID` set, the source controllers remain inactive and all existing behavior is unchanged. You can continue using hand-authored `CloudflareDNSRecord` CRs without setting this value.

When activated, the operator writes external-dns-compatible plaintext TXT records alongside each managed A/AAAA/CNAME to track ownership. These TXT records are visible in Cloudflare DNS and follow the format used by external-dns, enabling smooth migration.

See [gateway-api-source.md](gateway-api-source.md) for the annotation reference and [external-dns-migration.md](external-dns-migration.md) for migration paths.
