# Troubleshooting

Each section covers one symptom with exact `kubectl` commands and expected outputs.

---

## 1. DNS Not Appearing for a Route or Service

**Symptom:** You annotated an `HTTPRoute` or `Service` with `cloudflare.io/target`, but no DNS record appears in Cloudflare.

### Check 1: Does the operator see the annotation?

```bash
kubectl describe httproute <name> -n <namespace>
# or
kubectl describe service <name> -n <namespace>
```

In the **Events** section, look for any of these reasons:

| Reason | Meaning |
|---|---|
| `InvalidAnnotation` | `cloudflare.io/target` value is malformed (e.g., `tunnel:` with no name) |
| `NoMatchingZone` | No `CloudflareZone` CR matched the hostname by suffix |
| `AmbiguousZone` | Multiple zones matched; add `cloudflare.io/zone-ref` to disambiguate |
| `TunnelNotFound` | The named `CloudflareTunnel` CR does not exist |
| `TunnelNotReady` | The tunnel exists but `status.tunnelCNAME` is not yet populated |
| `DNSReconciled` | Success — DNS was created or updated |

If there are **no events at all**, the operator has not observed the annotation. Check:
- Is `TXT_OWNER_ID` / `registry.txtOwnerID` set? Source controllers are inert without it.
- Is the operator running and healthy?

```bash
kubectl get pods -n cloudflare-operator
kubectl logs -n cloudflare-operator -l app.kubernetes.io/name=cloudflare-operator --tail=50
```

### Check 2: Inspect emitted CRs

```bash
# Find emitted DNS records
kubectl get cloudflarednsrecord \
  -l cloudflare.io/source-name=<name>,cloudflare.io/source-kind=HTTPRoute \
  -A

# Check status of an emitted record
kubectl describe cloudflarednsrecord <cr-name> -n <namespace>
```

If no emitted CRs exist, the source controller did not emit them (check events on the source). If CRs exist but `Ready=False`, the DNS controller has a problem — check its conditions for `CloudflareAPIError` or `ZoneRefNotReady`.

### Check 3: Is the `CloudflareZone` ready?

```bash
kubectl get cloudflarezone -A
```

All referenced zones must show `Ready=True`. If a zone is `Ready=False`, the DNS controller waits for it before creating records.

---

## 2. DNS Record Has Wrong Content

**Symptom:** A DNS record exists in Cloudflare but points at the wrong value (wrong IP, wrong CNAME target, wrong proxy status).

### Check the emitted `CloudflareDNSRecord` spec

```bash
kubectl get cloudflarednsrecord \
  -l cloudflare.io/source-name=<name> \
  -A -o yaml
```

Compare `spec.content` (or `spec.dynamicIP`) with what you expect. The operator reconciles the Cloudflare-side record to match the spec.

### Check for annotation typos

```bash
kubectl get httproute <name> -n <namespace> -o jsonpath='{.metadata.annotations}'
```

Common mistakes:
- `cloudflare.io/proxied: "false"` when you want the record proxied (or vice versa).
- `cloudflare.io/target: "tunnel:<wrong-name>"` pointing at the wrong tunnel.
- Using `cname:` as target instead of `tunnel:` — this creates a literal CNAME, not a tunnel CNAME.

### Check the tunnel's CNAME

If using `tunnel:<name>`, verify the tunnel's status:

```bash
kubectl get cloudflaretunnel <name> -n <namespace> \
  -o jsonpath='{.status.tunnelCNAME}'
```

The emitted `CloudflareDNSRecord` content must match this value. If it doesn't, delete and re-create the emitted record (it will be re-emitted correctly on the next reconcile).

### Force a reconcile

Delete the emitted `CloudflareDNSRecord` CR. The source controller re-emits it on the next reconcile cycle (within the `interval` cadence, default 5m):

```bash
kubectl delete cloudflarednsrecord <cr-name> -n <namespace>
```

---

## 3. Tunnel Not Serving a Hostname

**Symptom:** DNS points at the tunnel correctly, but requests to the hostname return a 404 or connection error from cloudflared (not from your backend).

### Check the `CloudflareTunnelRule`

```bash
# Find rules referencing the tunnel
kubectl get cloudflaretunnelrule \
  -l cloudflare.io/source-name=<source-name> \
  -A

# Describe the rule for conditions
kubectl describe cloudflaretunnelrule <rule-name> -n <namespace>
```

Look at conditions:

| Condition | Meaning |
|---|---|
| `TunnelAccepted=True` | This rule is included in the current config |
| `TunnelAccepted=False, reason=DuplicateHostname` | Another rule claimed this hostname first |
| `Valid=False` | The rule spec failed validation |

If no `CloudflareTunnelRule` exists for the hostname, either:
- The source controller did not emit one (Services always emit a rule; HTTPRoutes only emit a rule when `cloudflare.io/tunnel-upstream` is set — otherwise the `defaultBackend` handles routing).
- You are relying on `spec.routing.defaultBackend` for routing (the Gateway case). Check the tunnel has a `defaultBackend` configured.

### Check the rendered config

```bash
kubectl get configmap -n <tunnel-namespace> \
  -l cloudflare.io/tunnel=<tunnel-name> \
  -o yaml
```

Inspect the `config.yaml` key to confirm the hostname appears in the rendered ingress list.

### Check for hostname conflicts

The operator does not emit an event for hostname conflicts; check the rule's conditions instead.

```bash
# Inspect a specific rule for conflict status
kubectl describe cloudflaretunnelrule -n <namespace> <name>
# Look for: Conditions: type=Conflict, status=True
#           Conditions: type=TunnelAccepted, status=False, reason=DuplicateHostname
```

```bash
# Find all tunnel rules whose hostname lost a conflict across the cluster
kubectl get cloudflaretunnelrule -A -o json | \
  jq -r '.items[] | select(.status.conditions[]? | select(.type=="Conflict" and .status=="True")) | "\(.metadata.namespace)/\(.metadata.name): \(.status.conditions[] | select(.type=="TunnelAccepted") | .message)"'
```

If two rules claim the same hostname, one wins and the other gets `TunnelAccepted=False, reason=DuplicateHostname` with `Conflict=True`. Resolve by adjusting rule priorities or removing the duplicate.

---

## 4. Connector Pod Not Ready

**Symptom:** `kubectl describe cloudflaretunnel <name>` shows `ConnectorReady=False`.

### Check the Deployment

```bash
# Find the connector Deployment
kubectl get deploy -n <namespace> -l cloudflare.io/tunnel=<name>

# Describe it for events
kubectl describe deploy <tunnel-name>-cloudflared -n <namespace>
```

Common causes:

- **Image pull failure** — wrong image tag or registry unreachable. Check `Events` for `Failed to pull image`.
- **OOMKilled** — memory limits too low. Increase `spec.connector.resources.limits.memory`.
- **CrashLoopBackOff** — cloudflared failing to start. Check logs:

  ```bash
  kubectl logs -n <namespace> -l cloudflare.io/tunnel=<name> --previous
  ```

- **`DeploymentConflict`** — a Deployment with the expected name exists but was not created by the operator. Check:

  ```bash
  kubectl get deploy <tunnel-name>-cloudflared -n <namespace> \
    -o jsonpath='{.metadata.ownerReferences}'
  ```

  If empty or pointing at something other than the `CloudflareTunnel`, delete or rename the conflicting Deployment.

### Check the credentials Secret

cloudflared needs the tunnel credentials Secret to connect:

```bash
kubectl get secret <generated-secret-name> -n <namespace>
```

The Secret name is `spec.generatedSecretName`. If it does not exist, the tunnel controller has not yet provisioned the tunnel in Cloudflare. Check the `Ready` condition on the `CloudflareTunnel`:

```bash
kubectl describe cloudflaretunnel <name> -n <namespace>
```

`Ready=False` means the Cloudflare API call failed. Look for `CloudflareAPIError` events.

---

## 5. Adoption Refused

**Symptom:** A record exists in Cloudflare but the operator refuses to manage it, emitting `RecordOwnershipConflict` or `TxtRegistryGap` events.

### 5.1 No TXT (TxtRegistryGap)

The record exists in Cloudflare with no companion TXT ownership record. The operator refuses by default.

```bash
kubectl describe httproute <name> -n <namespace>
# or
kubectl describe service <name> -n <namespace>
```

Expected event:
```
Warning  TxtRegistryGap  cloudflare-operator: record exists with no ownership TXT; add cloudflare.io/adopt=true to claim
```

**Resolution:** Add `cloudflare.io/adopt: "true"` to the source annotation:

```yaml
metadata:
  annotations:
    cloudflare.io/adopt: "true"
    cloudflare.io/target: "tunnel:prod"
    # ... other annotations
```

The operator creates the TXT record on the next reconcile and takes ownership of the existing data record.

### 5.2 Foreign TXT (RecordOwnershipConflict)

The record has a companion TXT, but the owner ID in the TXT is not your `txtOwnerID` and is not in `txtImportOwners`.

```bash
kubectl describe httproute <name> -n <namespace>
```

Expected event:
```
Warning  RecordOwnershipConflict  cloudflare-operator: TXT owner "external-dns-home" not in txtImportOwners
```

**Resolution:** Add the foreign owner ID to `txtImportOwners` in your Helm values:

```yaml
registry:
  txtOwnerID: "cloudflare-operator-prod"
  txtImportOwners:
    - "external-dns-home"
```

```bash
helm upgrade cloudflare-operator \
  oci://ghcr.io/jacaudi/charts/cloudflare-operator \
  --reuse-values \
  --set registry.txtImportOwners[0]=external-dns-home \
  --namespace cloudflare-operator
```

The operator retries on the next reconcile and adopts the record, rewriting the TXT to your owner ID.

---

## 6. Two Routes or Services Conflict on the Same FQDN

**Symptom:** Two `HTTPRoute` or `Service` objects both claim the same hostname. Because each source controller names its emitted `CloudflareDNSRecord` after itself (e.g. `httproute-<ns>-<name>-<fqdn>` vs. `svc-<ns>-<name>-<fqdn>`), two CRs for the same FQDN can coexist and both attempt to reconcile the Cloudflare record. The TXT registry mediates ownership: the first CR to write the companion TXT claims ownership (`owner = txtOwnerID`). The second CR finds a TXT whose owner matches `txtOwnerID` and treats the record as already-owned, so it overwrites the DNS content on every reconcile cycle. The result is last-write-wins churn, not a hard error.

```bash
# Find all CloudflareDNSRecord CRs for a hostname to identify competing sources
kubectl get cloudflarednsrecord -A \
  -o custom-columns=NAME:.metadata.name,NS:.metadata.namespace,HOSTNAME:.spec.name,SOURCE:.metadata.labels."cloudflare\.io/source-name"
```

**Resolution options:**

1. Remove the `cloudflare.io/target` annotation from one of the conflicting sources so only one emits a record for that FQDN.
2. Change one of the hostnames so they no longer overlap.
3. Delete the emitted `CloudflareDNSRecord` CR from the source you are retiring; the remaining source's CR takes sole ownership on the next reconcile.

---

## 7. Hand-Authored CR Conflicts with Annotation Source

**Symptom:** You have a hand-authored `CloudflareDNSRecord` for a hostname, and you also added `cloudflare.io/target` to an `HTTPRoute` or `Service` for the same hostname. The annotation source gets `RecordConflict`.

Hand-authored `CloudflareDNSRecord` CRs always win over annotation sources.

```bash
# Find the winning hand-authored CR
kubectl get cloudflarednsrecord -A | grep <hostname>

# Check the event on the annotation source
kubectl describe httproute <name> -n <namespace>
```

Expected event on the source:
```
Warning  RecordConflict  cloudflare-operator: FQDN "app.example.com" already owned by CloudflareDNSRecord apps/myapp-manual
```

**Resolution options:**

1. **Delete the hand-authored CR.** The annotation source becomes the owner on the next reconcile.
2. **Remove the annotation from the source.** Keep the hand-authored CR in place.
3. **Hand-author a `CloudflareTunnelRule` instead.** If the conflict is on the tunnel side (not DNS), you can hand-author just the tunnel rule without managing DNS from the annotation.

Hand-authored CRs are authoritative by design. This is intentional — it prevents annotation sources from accidentally overwriting explicit operator-managed records.
