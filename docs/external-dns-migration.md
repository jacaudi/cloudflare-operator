# Migrating from external-dns

cloudflare-operator v1 is a drop-in replacement for external-dns on the Cloudflare side. It uses the same plaintext TXT ownership record format as external-dns, so migration is a configuration change rather than a DNS cutover.

This document describes three migration paths:

- **Path A** — Drop-in: same `txtOwnerID`, zero DNS churn. Cleanest when external-dns manages all zones under one owner.
- **Path B** — Parallel run: different `txtOwnerID` with adoption of external-dns records in batches. Use when you want to pilot before committing.
- **Path C** — Greenfield: no existing external-dns setup to migrate.

---

## Prerequisites

Before migrating, install the Gateway API CRDs if you plan to use `HTTPRoute` sources:

```bash
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.1/standard-install.yaml
```

If you only use `Service` annotations, this step is optional.

Upgrade to cloudflare-operator v1:

```bash
helm upgrade cloudflare-operator \
  oci://ghcr.io/jacaudi/charts/cloudflare-operator \
  --version 1.0.0 \
  --namespace cloudflare-operator \
  --reuse-values
```

Apply the updated CRDs before upgrading (Helm does not upgrade CRDs automatically on `helm upgrade`):

```bash
helm pull oci://ghcr.io/jacaudi/charts/cloudflare-operator --version 1.0.0 --untar
kubectl apply -f cloudflare-operator/crds/
```

---

## Path A: Drop-In Replacement (Same Owner ID)

Use this path when external-dns has managed all your records under a single owner ID and you want to switch over cleanly.

### Steps

**1. Find external-dns's `txt-owner-id`.** It is set via the `--txt-owner-id` flag in external-dns's Deployment args or configuration. Make a note of the value (e.g., `external-dns-home`).

**2. Scale external-dns to zero.**

```bash
kubectl scale deployment external-dns -n external-dns --replicas=0
```

Wait for the pod to terminate before proceeding.

**3. Deploy cloudflare-operator v1 with the same `txtOwnerID`.**

```yaml
# values.yaml
registry:
  txtOwnerID: "external-dns-home"   # same value external-dns was using
```

```bash
helm upgrade cloudflare-operator \
  oci://ghcr.io/jacaudi/charts/cloudflare-operator \
  --version 1.0.0 \
  --namespace cloudflare-operator \
  -f values.yaml
```

**4. Annotate your workloads.** The operator only manages records for annotated resources (or hand-authored `CloudflareDNSRecord` CRs). Add annotations to your `HTTPRoute` or `Service` objects:

For an HTTPRoute:
```yaml
metadata:
  annotations:
    cloudflare.io/target: "tunnel:prod"           # or cname:<fqdn> or address
    cloudflare.io/zone-ref: "example-com"
```

For a Service:
```yaml
metadata:
  annotations:
    cloudflare.io/target: "tunnel:prod"
    cloudflare.io/hostnames: "app.example.com"
    cloudflare.io/zone-ref: "example-com"
```

**5. Verify DNS records.** The operator reads the existing TXT records (same owner ID), recognizes them as its own, and reconciles normally. No TXT rewrite, no DNS record change.

```bash
# Check events on annotated resources
kubectl describe httproute myapp -n apps
kubectl describe svc myapp -n apps

# Check emitted DNS records
kubectl get cloudflarednsrecord -l cloudflare.io/source-name=myapp -A
```

Look for `DNSReconciled` events and `Ready=True` conditions on emitted CRs.

**6. Remove external-dns.** Once you have verified all records are reconciled:

```bash
helm uninstall external-dns -n external-dns
# or: kubectl delete deployment external-dns -n external-dns
```

---

## Path B: Parallel Run with Adoption

Use this path when you want to pilot cloudflare-operator on a subset of workloads before fully replacing external-dns. Both systems run simultaneously; cloudflare-operator takes over records in batches.

### Steps

**1. Deploy cloudflare-operator v1 with a distinct `txtOwnerID` and `txtImportOwners`.**

```yaml
# values.yaml
registry:
  txtOwnerID: "cloudflare-operator-prod"       # new, distinct owner ID
  txtImportOwners:
    - "external-dns-home"                       # the external-dns owner ID(s)
```

```bash
helm install cloudflare-operator \
  oci://ghcr.io/jacaudi/charts/cloudflare-operator \
  --version 1.0.0 \
  --namespace cloudflare-operator \
  -f values.yaml
```

Both external-dns and cloudflare-operator are now running. external-dns continues to manage all records.

**2. Annotate a pilot set of workloads.** Choose a small, low-risk set of `HTTPRoute` or `Service` objects. Add the `cloudflare.io/target` and other required annotations.

**3. Verify adoption.** For each piloted workload, check for `RecordAdopted` events and `DNSAdopted=True` conditions:

```bash
kubectl describe httproute myapp-pilot -n apps
```

Expected events:
```
Normal  RecordAdopted  cloudflare-operator: adopted record from external-dns-home
Normal  DNSReconciled  cloudflare-operator: DNS record in sync
```

Check that the underlying A/AAAA/CNAME records in Cloudflare are unchanged (no DNS churn). The operator rewrites the TXT from `external-dns-home` to `cloudflare-operator-prod` on adoption, but does not modify the actual data record on first touch.

Verify the condition on the emitted DNS CR:

```bash
kubectl get cloudflarednsrecord -l cloudflare.io/source-name=myapp-pilot -A -o yaml | grep -A5 conditions
```

Look for `OwnershipVerified=True` and `DNSAdopted=True`.

**4. Prevent external-dns from managing the migrated records.** Depending on your external-dns configuration, you can scope it off the migrated workloads via:

- Annotation filter (`--annotation-filter` on external-dns): add a label or annotation to migrated resources that external-dns is configured to exclude.
- Label selector (`--label-filter`): use a label to mark migrated resources.
- Domain filter (`--domain-filter`): if the pilot is a subdomain, scope external-dns off that subdomain.

Verify that external-dns no longer reconciles the migrated records (watch its logs for a few minutes).

**5. Expand in batches.** Repeat steps 2–4 for additional workloads. Move in batches you can verify quickly.

**6. Scale down external-dns once fully migrated.**

```bash
kubectl scale deployment external-dns -n external-dns --replicas=0
```

Verify all records are still reconciled by cloudflare-operator, then remove external-dns entirely.

---

## Path C: Greenfield

No external-dns to migrate from. This is the minimal setup.

**1. Set `txtOwnerID` and deploy.**

```yaml
# values.yaml
registry:
  txtOwnerID: "cloudflare-operator-prod"
```

```bash
helm install cloudflare-operator \
  oci://ghcr.io/jacaudi/charts/cloudflare-operator \
  --version 1.0.0 \
  --namespace cloudflare-operator \
  -f values.yaml
```

**2. Apply your `CloudflareZone` and credentials Secret.** See [domain-onboarding.md](domain-onboarding.md).

**3. Annotate workloads or author `CloudflareDNSRecord` CRs.** The operator creates records from scratch with no adoption needed.

---

## Verifying the Migration

After completing any migration path, run the following to confirm the operator is fully in control:

```bash
# All CloudflareZones should be Ready
kubectl get cloudflarezone -A

# All emitted DNS records should be Ready
kubectl get cloudflarednsrecord -A

# No source objects should have Warning events
kubectl get events -A --field-selector reason=RecordConflict
kubectl get events -A --field-selector reason=RecordOwnershipConflict
kubectl get events -A --field-selector reason=TxtRegistryGap
```

If any `RecordOwnershipConflict` events remain after migration, a record's TXT is owned by an ID not in your `txtImportOwners` list. Add that ID and the operator retries on next reconcile.

---

## Rollback

cloudflare-operator v1 does not remove records on uninstall unless you delete the CRs first. To roll back to external-dns:

1. Scale cloudflare-operator to zero: `kubectl scale deployment -n cloudflare-operator --replicas=0 -l app.kubernetes.io/name=cloudflare-operator`
2. Scale external-dns back up. Because the TXT ownership may have been rewritten to `cloudflare-operator-prod`, external-dns may not recognize its own records if you used Path B. To restore, either set external-dns's `txt-owner-id` to `cloudflare-operator-prod`, or manually delete the TXT records and let external-dns recreate them.

For Path A (same owner ID), rollback is transparent — scaling external-dns back up picks up the records immediately.
