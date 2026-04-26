# Tunnels

`CloudflareTunnel` provisions a Cloudflare Tunnel and optionally manages the `cloudflared` connector Deployment that keeps the tunnel connected. It also aggregates `CloudflareTunnelRule` CRs — whether hand-authored or emitted by source controllers — into the cloudflared `config.yaml` that governs ingress routing.

---

## Quickstart: Operator-Managed Connector

The simplest fully-managed setup: one tunnel, the operator owns cloudflared.

```yaml
apiVersion: cloudflare.io/v1alpha1
kind: CloudflareTunnel
metadata:
  name: prod
  namespace: network
spec:
  name: prod                               # tunnel name in Cloudflare
  secretRef:
    name: cloudflare-api-token             # Secret with apiToken + accountID
  generatedSecretName: prod-tunnel-credentials  # operator creates this Secret
  connector:
    enabled: true
    replicas: 2
  routing:
    defaultBackend:
      url: https://my-gateway.network.svc.cluster.local
```

The operator:
1. Creates the tunnel in Cloudflare (if it doesn't exist) and writes credentials to `prod-tunnel-credentials`.
2. Reconciles a `ServiceAccount`, `ConfigMap` (cloudflared `config.yaml`), and `Deployment` named after the tunnel, all in the same namespace.
3. Aggregates any `CloudflareTunnelRule` CRs referencing this tunnel into the ConfigMap.
4. When the ConfigMap changes, patches the Deployment's pod template annotation to trigger a rolling restart.

---

## `spec.connector` — Managing cloudflared

```yaml
spec:
  connector:
    enabled: true           # default: false. Must be true to deploy cloudflared.
    replicas: 2             # default: 2. Minimum: 1.
    image:
      repository: docker.io/cloudflare/cloudflared
      tag: "2026.3.0"       # omit to use the operator's compile-time default
    resources:
      requests:
        cpu: 10m
        memory: 128Mi
      limits:
        memory: 256Mi
    nodeSelector: {}
    tolerations: []
    affinity: {}
    topologySpreadConstraints: []
```

All fields in `connector` except `enabled` are optional. Omitting `connector` entirely (or setting `enabled: false`) means you are responsible for running cloudflared yourself.

### Upgrading the cloudflared image

Update `spec.connector.image.tag` and apply:

```bash
kubectl patch cloudflaretunnel prod -n network \
  --type=merge \
  -p '{"spec":{"connector":{"image":{"tag":"2026.5.0"}}}}'
```

The operator detects the spec change, updates the Deployment's container image, and Kubernetes performs a rolling update. `status.connector.image` reflects the image actually running.

### Checking connector health

```bash
kubectl describe cloudflaretunnel prod -n network
```

Look for:
- `ConnectorReady=True` — at least one cloudflared pod is running.
- `IngressConfigured=True` — the aggregated config was applied without conflicts.
- `status.connector.readyReplicas` — number of ready pods.

```bash
# Check the connector Deployment directly
kubectl get deploy -n network -l cloudflare.io/tunnel=prod

# Check connector pod logs
kubectl logs -n network -l cloudflare.io/tunnel=prod
```

### Disabling the connector (reverting to self-managed cloudflared)

Set `connector.enabled: false` (or remove the `connector` block):

```bash
kubectl patch cloudflaretunnel prod -n network \
  --type=merge \
  -p '{"spec":{"connector":{"enabled":false}}}'
```

The operator deletes the managed `Deployment`, `ConfigMap`, and `ServiceAccount`. Your existing self-managed cloudflared Deployment is not affected. The operator continues to track `CloudflareTunnelRule` CRs but no longer renders a ConfigMap.

---

## `spec.routing` — Tunnel-Wide Routing Defaults

```yaml
spec:
  routing:
    defaultBackend:
      url: https://my-gateway.network.svc.cluster.local
      # OR
      serviceRef:
        name: my-gateway
        namespace: network
        port: 443
        scheme: https
    originRequest:
      noTLSVerify: false
```

`routing.defaultBackend` handles traffic that no `CloudflareTunnelRule` matches. It is rendered as the second-to-last entry in the cloudflared ingress list (before the auto-appended `http_status:404` catch-all that the operator always writes at the end).

`routing.originRequest` sets defaults applied to all rules unless overridden by an individual rule's `originRequest`.

If you have no `routing.defaultBackend`, unmatched traffic returns a 404 from cloudflared.

### Rendered cloudflared config.yaml order

```
1. All CloudflareTunnelRule entries, sorted by priority descending, then name ascending
2. spec.routing.defaultBackend (if set)
3. service: http_status:404    ← always auto-appended; do not write it manually
```

The operator owns the final `http_status:404`. Any catch-all intent goes through `routing.defaultBackend`.

---

## Hand-Authored `CloudflareTunnelRule`

Source controllers emit `CloudflareTunnelRule` CRs automatically. You can also write them by hand for cases that the annotation sources don't cover — for example, explicit `http_status` rejections, custom `originRequest` settings, or backends not reachable from a Service reference.

```yaml
apiVersion: cloudflare.io/v1alpha1
kind: CloudflareTunnelRule
metadata:
  name: myapp-rule
  namespace: apps
spec:
  tunnelRef:
    name: prod
    namespace: network        # omit if rule is in the same namespace as the tunnel
  hostnames:
    - "app.example.com"
    - "api.example.com"
  backend:
    serviceRef:
      name: myapp
      namespace: apps
      port: 8080
      scheme: http            # http | https | h2c | tcp
  originRequest:
    noTLSVerify: false
    connectTimeout: 30s
  priority: 100               # higher = evaluated earlier; default 100
```

### Backend forms

`backend` is a discriminated union; exactly one of the three must be set:

- **`serviceRef`** — routes to a Kubernetes Service. The operator resolves `http://myapp.apps.svc.cluster.local:8080` at render time from the Service's cluster DNS name.
- **`url`** — raw backend URL. Use for any backend not expressible as a Service reference.
- **`httpStatus`** — produces a cloudflared `http_status:<code>` entry. Use for explicit rejection rules.

```yaml
# Reject at a specific hostname
spec:
  backend:
    httpStatus: 404

# Route to an arbitrary URL
spec:
  backend:
    url: "https://upstream.internal.example.com:8443"
```

### Port and scheme inference

If `backend.serviceRef.scheme` is omitted, the operator infers it from the Service's port name:
- Port named `http` → `http`
- Port named `https` → `https`
- Port named `grpc` → `h2c`
- Anything else → `http`

---

## Aggregation Semantics

The tunnel controller aggregates all `CloudflareTunnelRule` CRs referencing a tunnel cluster-wide on every reconcile. Aggregation is deterministic.

### Sorting

Rules are sorted: **priority descending**, then **name ascending**. Higher-priority rules appear earlier in the cloudflared ingress list, so cloudflared matches them first.

### Wildcard-specificity tiebreak

When two rules have the same priority and similar hostnames (e.g., `*.example.com` vs. `app.example.com`), the more-specific hostname (`app.example.com`) wins the sort, appearing before the wildcard. This mirrors cloudflared's own matching semantics (first-match wins for exact before wildcard).

### Duplicate hostname detection

If two rules claim the same hostname at the same aggregation rank, the first rule (by sort order) wins and gets `TunnelAccepted=True`. The losing rule gets `TunnelAccepted=False, reason=DuplicateHostname`. The tunnel controller is the only writer of `CloudflareTunnelRule.status`.

### Hand-authored vs. annotation-sourced

Hand-authored rules and annotation-sourced rules are peers in aggregation. Priority determines order. If you want a hand-authored rule to take precedence over an annotation-sourced rule on the same hostname, set a higher `priority` value.

---

## `CloudflareTunnelRule` Status

After aggregation, each rule's status reflects the tunnel controller's decision:

```bash
kubectl get cloudflaretunnelrule -n apps -o wide

kubectl describe cloudflaretunnelrule myapp-rule -n apps
```

Conditions:

| Condition | Meaning |
|---|---|
| `Valid=True` | Spec passed validation |
| `TunnelAccepted=True` | Included in the last aggregation render |
| `TunnelAccepted=False, reason=DuplicateHostname` | A higher-priority rule claimed one of this rule's hostnames |
| `Conflict=True` | This rule was excluded due to hostname collision |

`status.resolvedBackend` shows the URL cloudflared was configured with.
`status.appliedToConfigHash` shows the config hash at the time this rule was included (useful for debugging drift).

---

## Migrating from Self-Managed cloudflared

If you currently run your own cloudflared Deployment:

1. Keep `connector.enabled: false` (or omit it).
2. Author `CloudflareTunnelRule` CRs (or annotate sources) to mirror your current cloudflared `config.yaml`.
3. Apply the `CloudflareTunnel` CR. The tunnel controller renders a ConfigMap and writes `status.connector.configHash`.
4. Manually compare the rendered ConfigMap against your hand-rolled config:

   ```bash
   kubectl get configmap prod-cloudflared-config -n network -o yaml
   ```

5. Once satisfied, set `connector.enabled: true`. The operator creates a managed Deployment.
6. Delete your hand-rolled Deployment. Brief overlap is safe — cloudflared supports multiple connectors per tunnel.

There is no in-place takeover of an existing Deployment. If the operator detects a Deployment with the expected name that it does not own (no ownerRef to the `CloudflareTunnel`), it sets `ConnectorReady=False, reason=DeploymentConflict` and refuses to proceed until you delete or rename the conflicting Deployment.
