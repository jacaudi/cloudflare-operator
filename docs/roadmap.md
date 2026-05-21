# Roadmap

Services on the radar for future controllers. Nothing here is committed scope —
this page exists to track which Cloudflare surfaces we expect to grow into and
to capture the open design questions before any of them turn into a plan.

Each section lists what the API looks like today, the rough shape a controller
might take, and the open questions that need answering before design work
starts.

## Contents

- [R2](#r2)
- [Email Service](#email-service)
- [Workers](#workers)
- [What this page is not](#what-this-page-is-not)

---

## R2

Cloudflare's S3-compatible object storage. A controller would let teams declare
buckets, lifecycle rules, CORS policy, and public-access settings as CRDs
alongside the workloads that read/write them.

### R2 — likely CRDs

- `R2Bucket` — bucket name, location hint, jurisdiction, default storage
  class. Status surfaces the bucket ID + endpoint URL.
- `R2BucketPolicy` (maybe) — lifecycle rules, CORS, public-access toggle.
  Could also fold into `R2Bucket.spec` if the surface stays small.

### R2 — open questions

- Credentials model: do we mint per-bucket API tokens (and expose them via a
  generated Secret), or require an existing account-scoped token like today's
  zone controller?
- Bundle placement: third `controllers.r2` bundle, or fold into the zone
  bundle since both are account-scoped?
- Deletion semantics: refuse to delete a non-empty bucket by default, or
  require an explicit `spec.forceDelete: true`? (Mirroring the safe-adopt
  pattern from `CloudflareDNSRecord`.)

---

## Email Service

Cloudflare's per-zone email surface: forwarding rules, catch-all behavior,
verified destination addresses, and the broader Email Service product
(Workers-driven processing, send API). A controller would let teams declare
this configuration as CRDs that ride alongside the `Zone` they belong to.

### Email Service — likely CRDs

- `EmailServiceConfig` — per-zone enable/disable, catch-all action,
  reference to the owning `Zone`. One per zone.
- `EmailRoutingRule` — match expression (specific address, regex, catch-all),
  action (forward / drop / worker), destination address ref.
- `EmailDestinationAddress` — verified destination address. Status surfaces
  the verification state; the controller can't bypass Cloudflare's
  out-of-band verification email, so the CR will sit in
  `Phase=PendingVerification` until the operator confirms via the API.

### Email Service — open questions

- Verification UX: the destination-address verification email goes to a human
  inbox. Do we expose a `kubectl` flow to resend the verification, or just
  document the dashboard path?
- Zone coupling: should `EmailRoutingRule` reference `Zone` by ref like
  `CloudflareDNSRecord` does, or live in a flat namespace and resolve by
  domain string?
- Interaction with DNS: enabling Email Service on a zone provisions MX +
  SPF + TXT records on Cloudflare's side. Does the zone controller need to
  know about these so it doesn't fight them, or do we treat them as
  out-of-band like the dashboard does?
- Scope: do we cover only inbound routing first, or include the Email
  Workers / send-API surfaces in the same controller from the start?

---

## Workers

Cloudflare's serverless runtime. A controller would let teams ship Worker
scripts, bindings, routes, and Cron Triggers as CRDs — closing the loop
between the workload that produces a Worker and the operator that publishes
it. This is the largest surface of the three and the most likely to want
sub-controllers per concern.

### Workers — likely CRDs

- `Worker` — script source ref (ConfigMap / OCI artifact / inline), runtime
  flags, compatibility date, observability config. Status surfaces the
  deployed version ID.
- `WorkerBinding` — KV namespaces, Durable Object namespaces, R2 bucket
  refs, environment variables, Secrets (sourced from k8s Secrets via
  envFrom-style refs). Could also be `spec.bindings` on `Worker`.
- `WorkerRoute` — pattern + zone ref + Worker ref. The route-to-worker
  glue.
- `WorkerCronTrigger` — cron expression + Worker ref.

### Workers — open questions

- Script delivery: how does the script get from the developer's repo into
  the cluster? OCI artifact pulled by the controller? ConfigMap built by a
  CI step? Inline in the CR (only viable for tiny scripts)?
- Binding sourcing: do we resolve k8s Secret refs at apply time and push the
  values to Cloudflare, or use the new Workers Secrets API and avoid
  exposing the values to the operator at all?
- Versioning + rollback: Cloudflare keeps every deployment. Do we surface
  `Status.LastTenVersions` and offer a `spec.rollbackTo` field, or leave
  rollback to the dashboard / API?
- Scope: do we cover Pages too (same runtime, different surface), or keep
  this strictly to Workers and treat Pages as a separate future track?

---

## What this page is not

- A commitment. Anything here may be reshaped or dropped after design work.
- A complete list. Other Cloudflare surfaces (Access, Stream, Images,
  Waiting Room, Turnstile, …) may land here later as the operator's scope
  becomes clearer.
