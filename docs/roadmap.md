# Roadmap

Services on the radar for future controllers. Nothing here is committed scope —
this page exists to track which Cloudflare surfaces we expect to grow into and
to capture the open design questions before any of them turn into a plan.

Each section lists what the API looks like today, the rough shape a controller
might take, and the open questions that need answering before design work
starts. CRD sketches were cross-checked against the Cloudflare Go SDK
(`github.com/cloudflare/cloudflare-go/v6`) and the public API reference; API
paths quoted below come from those sources.

## Contents

- [R2](#r2)
- [Email Service](#email-service)
- [Workers](#workers)
- [Containers](#containers)
- [What this page is not](#what-this-page-is-not)

---

## R2

Cloudflare's S3-compatible object storage. A controller would let teams
declare buckets, lifecycle rules, CORS policy, and custom-domain attachments
as CRDs alongside the workloads that read/write them.

**API surface (verified):** `client.R2.Buckets.*` in cloudflare-go covers
`/accounts/{account_id}/r2/buckets` for CRUD, plus sub-resources
`.Lifecycle.Update`, `.CORS.Update`, and `.Domains.Custom` (path:
`/accounts/{account_id}/r2/buckets/{bucket_name}/domains/custom`). Buckets
take a `LocationHint` (e.g. `eu`) and `StorageClass` at creation time.

### R2 — likely CRDs

- `R2Bucket` — bucket name, `locationHint`, `storageClass`. Status surfaces
  the bucket ID + S3 endpoint URL. Maps 1:1 to `R2.Buckets.New`.
- `R2BucketLifecycle` — rule list (each rule: ID, prefix conditions, actions
  like `deleteObjectsAfterDays`, enabled flag). Maps to
  `R2.Buckets.Lifecycle.Update`. Folding into `R2Bucket.spec.lifecycle` is
  viable — the surface is small.
- `R2BucketCORS` — allowed origins, methods (`GET`/`PUT`/`POST`/`DELETE`/
  `HEAD`), headers, max-age. Maps to `R2.Buckets.CORS.Update`. Same folding
  question as lifecycle.
- `R2CustomDomain` (optional) — bucket ref + hostname + zone ref. Maps to
  `R2.Buckets.Domains.Custom.*`. Worth its own CRD if we want a clean
  attachment surface with status conditions.

### R2 — open questions

- Credentials model: do we mint per-bucket API tokens (and expose them via a
  generated Secret), or require an existing account-scoped token like
  today's zone controller?
- Bundle placement: third `controllers.r2` bundle, or fold into the zone
  bundle since both are account-scoped?
- Deletion semantics: refuse to delete a non-empty bucket by default, or
  require an explicit `spec.forceDelete: true`? (Mirroring the safe-adopt
  pattern from `CloudflareDNSRecord`.)
- Lifecycle/CORS shape: separate CRDs or sub-fields of `R2Bucket`? The API
  is "replace the whole rule set," so a single CRD per bucket keeps the
  reconcile model simple.

---

## Email Service

Cloudflare's per-zone email surface: forwarding rules, catch-all behavior,
verified destination addresses, and (going forward) the broader Email
Service product. A controller would let teams declare this configuration as
CRDs that ride alongside the `Zone` they belong to.

**API surface (verified):** Settings live under `/zones/{zone_id}/email/routing`,
rules under `/zones/{zone_id}/email/routing/rules`, and email-routing DNS
records under `/zones/{zone_id}/email/routing/dns`. **Addresses are
account-scoped**, not zone-scoped: `/accounts/{account_id}/email/routing/addresses`
— important because it shapes namespacing and ownership.

### Email Service — likely CRDs

- `EmailServiceConfig` — per-zone enable/disable, catch-all action,
  reference to the owning `Zone`. One per zone. Maps to the
  `/zones/{zone_id}/email/routing` settings endpoint.
- `EmailRoutingRule` — matchers (specific address, regex, catch-all), action
  (`forward` / `drop` / `worker`), destination address ref, priority,
  enabled flag. Maps to `/zones/{zone_id}/email/routing/rules`.
- `EmailDestinationAddress` — verified destination address. Status surfaces
  the verification state; the controller can't bypass Cloudflare's
  out-of-band verification email, so the CR will sit in
  `Phase=PendingVerification` until the operator confirms via the
  account-scoped addresses endpoint.

### Email Service — open questions

- Verification UX: the destination-address verification email goes to a
  human inbox. Do we expose a `kubectl` flow to resend the verification, or
  just document the dashboard path?
- Address scoping mismatch: addresses are **account-scoped** but rules are
  **zone-scoped**. Does `EmailDestinationAddress` live in a single
  operator-defined namespace, or do we allow it per-namespace and let
  multiple zones reference the same CR? Affects RBAC.
- Interaction with DNS: enabling email routing on a zone provisions MX +
  SPF + TXT records via `/zones/{zone_id}/email/routing/dns`. Does the
  zone controller need to know about these so it doesn't fight them, or do
  we treat them as out-of-band like the dashboard does?
- Scope: do we cover only inbound routing first, or include the Email
  Workers / send-API surfaces in the same controller from the start?

---

## Workers

Cloudflare's serverless runtime. A controller would let teams ship Worker
scripts, bindings, routes, and Cron Triggers as CRDs — closing the loop
between the workload that produces a Worker and the operator that publishes
it. This is the largest surface of the three and the most likely to want
sub-controllers per concern.

**API surface (verified):** Scripts live under
`/accounts/{account_id}/workers/scripts/{script_name}` (cloudflare-go:
`client.Workers.Scripts.*` and the newer `client.Workers.Beta.Workers.*`).
Cloudflare's model is **version → deployment**: you upload code as a
**version** (immutable), then create a **deployment** that maps a
percentage of traffic to one or two versions (gradual rollouts). Routes
live under `/zones/{zone_id}/workers/routes` (zone-scoped, not account).
KV namespaces are a separate top-level resource at
`/accounts/{account_id}/storage/kv/namespaces` (cloudflare-go:
`client.KV.Namespaces.*`).

### Workers — likely CRDs

- `Worker` — script source ref (ConfigMap / OCI artifact / inline), runtime
  flags, compatibility date, observability config. Status surfaces
  `lastVersionID` + the active `deploymentID`. Maps to
  `Workers.Scripts.*` (script-level metadata).
- `WorkerDeployment` (optional) — explicit deployment object exposing
  Cloudflare's percentage-based traffic split (e.g. 90% v1, 10% v2).
  Without it, the operator picks "100% latest version"; with it, gradual
  rollouts become declarative. Maps to
  `/accounts/{account_id}/workers/scripts/{script_name}/deployments`.
- `WorkerBinding` — KV namespaces, Durable Object namespaces, R2 bucket
  refs (cross-CRD ref to `R2Bucket`), Queues, environment variables,
  Secrets (sourced from k8s Secrets via envFrom-style refs). Could also
  collapse into `Worker.spec.bindings`.
- `WorkerRoute` — pattern + zone ref + Worker ref. Maps to
  `Workers.Routes.New` at `/zones/{zone_id}/workers/routes`.
- `WorkerCronTrigger` — cron expression + Worker ref. Maps to the
  `/scripts/{script_name}/schedules` sub-resource.
- `KVNamespace` (optional, lives alongside `Worker`) — declarative KV
  namespace creation so bindings can reference it by k8s name. The
  alternative is "BYO KV ID."

### Workers — open questions

- Script delivery: how does the script get from the developer's repo into
  the cluster? OCI artifact pulled by the controller? ConfigMap built by a
  CI step? Inline in the CR (only viable for tiny scripts)?
- Binding sourcing: do we resolve k8s Secret refs at apply time and push
  the values to Cloudflare, or use the Workers Secrets API and avoid
  exposing the values to the operator at all?
- Versioning + rollback: Cloudflare keeps every version. Do we expose
  `WorkerDeployment` from day one (declarative gradual rollouts) or default
  to "always 100% latest" and add the CRD later?
- Scope: do we cover Pages too (same runtime, different surface), or keep
  this strictly to Workers and treat Pages as a separate future track?

---

## Containers

Cloudflare Containers run OCI images on Cloudflare's compute, typically
fronted by a Worker that boots / routes to container instances on demand.
A controller would let teams declare container applications + their
deployment shape as CRDs that pair cleanly with `Worker` CRs.

**API surface (less certain):** Containers didn't surface in the
cloudflare-go v6 doc index returned by context7, which means the typed Go
client either doesn't expose Containers yet or it lives under a namespace
the doc query missed (Cloudflare's underlying scheduler has historically
been called "cloudchamber" internally — paths may show up under
`/accounts/{account_id}/cloudchamber/` or `/accounts/{account_id}/containers/`).
**Design work should re-confirm the actual paths and SDK surface against
the live API reference before any CRD is locked in.** What follows is a
sketch based on the public product shape — treat it as a starting point.

### Containers — likely CRDs

- `ContainerApplication` — application name, OCI image ref (registry +
  digest), instance type / size, region(s) or "placement: anywhere",
  scaling config (min/max instances, idle timeout), Worker binding ref so
  a `Worker` can wake / route to it. Status surfaces the active
  deployment + per-instance state.
- `ContainerDeployment` (optional) — explicit deployment object so image
  rolls can be expressed declaratively (same model as `WorkerDeployment`
  if Cloudflare exposes percentage-based container rollouts). Without
  it, the operator does "always latest image."
- `ContainerRegistryCredential` (maybe) — Secret ref for private OCI
  registries. Skip if Cloudflare Containers handles registry auth via
  the existing account-level credential pool.

### Containers — open questions

- SDK surface: does cloudflare-go expose Containers in v6 (under a path
  the doc index didn't return), or do we need to call the REST API
  directly until it lands? This is the gating question before any design.
- Worker coupling: should `Worker.spec.containers` reference
  `ContainerApplication` CRs (tight pairing, single source of truth), or
  should they stay independent and bind by name (looser, but lets a
  container be referenced by multiple Workers)?
- Image-pull model: do we let users reference any OCI registry, or
  restrict to Cloudflare's hosted registry + a small allowlist of known
  external ones?
- Scaling shape: does the CRD expose per-region replica counts, or treat
  Cloudflare's placement as a black box (`replicas` + `regions: ["auto"]`)?
- Deletion semantics: same question as R2 — refuse to delete an
  application with active instances, or require `spec.forceDelete: true`?

---

## What this page is not

- A commitment. Anything here may be reshaped or dropped after design work.
- A complete list. Other Cloudflare surfaces (Access, Stream, Images,
  Waiting Room, Turnstile, …) may land here later as the operator's scope
  becomes clearer.
