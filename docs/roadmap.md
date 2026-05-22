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

Cloudflare's serverless runtime — and, as of late 2024, **the recommended
home for static sites, SPAs, and containers too**. Cloudflare's own docs
say new features and optimizations are landing on Workers Static Assets
instead of Pages, and Cloudflare Containers ship as a Worker binding (a
Durable-Object-style class wrapping an OCI image), not a standalone REST
product. So this controller covers four modes under one CRD:

1. **Pure static** — Hugo, Jekyll, MkDocs, plain HTML. `Worker.spec.assets`
   only, no script.
2. **SPA** — React, Vue, Svelte. Assets + `notFoundHandling:
   SinglePageApplication` so unmatched paths return `index.html` and the
   client-side router takes over.
3. **Full-stack** — Astro, Next-on-Workers, Hugo with API routes. Assets +
   Worker script + `ASSETS` binding; the script fetches static files via
   `env.ASSETS.fetch()` and handles dynamic routes itself.
4. **Container-backed** — long-running workloads or OCI images that don't
   fit the request/response model. The Worker script declares a
   `Container` binding (image ref, default port, idle-shutdown timeout,
   env vars, entrypoint, egress policy) and forwards requests to the
   container instance on demand. Container config travels with the
   Worker upload, not via a separate API.

A controller would let teams ship Worker scripts, static-asset bundles,
container bindings, KV/DO/R2 bindings, routes, and Cron Triggers as CRDs
— closing the loop between the workload (or static-site build, or
container image) that produces a Worker and the operator that publishes
it. This is the largest surface in the roadmap and the most likely to
want sub-controllers per concern.

**API surface (verified):** Scripts live under
`/accounts/{account_id}/workers/scripts/{script_name}` (cloudflare-go:
`client.Workers.Scripts.*` and the newer `client.Workers.Beta.Workers.*`).
Cloudflare's model is **version → deployment**: you upload code as a
**version** (immutable), then create a **deployment** that maps a
percentage of traffic to one or two versions (gradual rollouts). Routes
live under `/zones/{zone_id}/workers/routes` (zone-scoped, not account).
KV namespaces are a separate top-level resource at
`/accounts/{account_id}/storage/kv/namespaces` (cloudflare-go:
`client.KV.Namespaces.*`). Static assets travel with the script upload (the
`[assets]` block in `wrangler.toml` becomes part of the version payload),
and so do container bindings (`[[containers]]` block — image ref,
`default_port`, `sleep_after`, `enable_internet`, `entrypoint`, allowed/
denied hosts). The Cloudflare Containers Go management API isn't yet
surfaced in cloudflare-go v6's typed client; the management surface today
is the Worker upload itself.

### Workers — static assets (the Pages replacement)

This is the surface that absorbs what Cloudflare Pages used to handle.
The configuration lives entirely in `Worker.spec.assets`:

- `directory` — source ref to the built artifact tree (Hugo's `./public`,
  Vite's `./dist`, etc.). OCI artifact is the natural delivery vehicle;
  see the artifact-delivery open question below.
- `binding` — the env binding name (`ASSETS` by convention) the Worker
  script uses to fetch static files (`env.ASSETS.fetch(request)`).
- `notFoundHandling` — `SinglePageApplication` (return `index.html` for
  any unmatched path so the client-side router takes over) or `404`
  (return a 404). Pure static sites and SPAs differ only by this knob.
- `runWorkerFirst` — route patterns where the Worker script should run
  *before* the asset matcher (e.g. `["/*", "!/assets/*"]` means "Worker
  handles everything except `/assets/*`, which goes straight to static
  files"). Lets full-stack apps put API routes and static routes in the
  same Worker without conflict.

The bundle is uploaded as part of the script version, so static-asset
changes are versioned and rolled out via the same `version → deployment`
machinery as code changes (gradual rollouts apply to asset bundles too).

### Workers — containers

Cloudflare Containers ship as a **Worker binding**, not a standalone
product. The Worker code declares a `Container` class
(`extends Container`, Durable-Object-style) and the runtime spins up an
OCI image instance on demand when a request arrives. Configuration is
declarative on the class and travels with the Worker upload — there's no
separate "container application" REST object to reconcile.

`Worker.spec.containers[]` would carry one entry per binding:

- `name` — the env binding name the Worker uses (`MY_CONTAINER`).
- `image` — OCI image reference (registry + repo + digest or tag).
- `defaultPort` — the port the container process listens on.
- `sleepAfter` — idle-shutdown timeout (e.g. `10m`). Cloudflare stops the
  instance after this window of no traffic.
- `requiredPorts` — additional ports that must be healthy before the
  container is considered ready.
- `envVars` — env-var map; values from inline literals, Secret refs, or
  ConfigMap refs.
- `entrypoint` — override the image's default command.
- `enableInternet` — outbound-internet toggle (default false).
- `allowedHosts` / `deniedHosts` — egress allow/deny lists; supports glob
  patterns (`*.github.com`).
- `interceptHttps` — let Cloudflare intercept outbound HTTPS for
  inspection (requires the container to trust Cloudflare's CA).

Runtime egress controls (`setOutboundByHost`, `addAllowedHost`, etc.) are
*not* part of the CRD — they're called from inside the Worker code at
runtime and persist in Durable Object storage, which is outside the
operator's scope.

### Workers — likely CRDs

- `Worker` — script source ref (ConfigMap / OCI artifact / inline) **and/or**
  `spec.assets` (directory source ref + `binding` name +
  `notFoundHandling: SinglePageApplication|404` + `runWorkerFirst` route
  list) **and/or** `spec.containers[]` (per-binding: `name`, OCI `image`
  ref, `defaultPort`, `sleepAfter` duration, `enableInternet`,
  `requiredPorts`, `envVars` from inline / Secret / ConfigMap refs,
  `entrypoint`, `allowedHosts` / `deniedHosts` egress policy,
  `interceptHttps`), runtime flags, compatibility date, observability
  config. Pure static omits the script; SPA sets `notFoundHandling`;
  full-stack sets both; container-backed adds `containers[]`. Status
  surfaces `lastVersionID` + the active `deploymentID`. Maps to
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

- Artifact delivery: scripts are a single file, but static-asset bundles
  are *directory trees* (a Hugo build is hundreds of files). The
  same source-ref field has to cover both shapes. OCI artifact is the
  obvious fit (Hugo/Vite CI builds → pushes an OCI artifact → operator
  pulls + uploads to Cloudflare); ConfigMap doesn't scale past trivial
  scripts; PVCs are awkward; inline is hopeless for assets. Do we
  support **OCI artifact only** for v1 and add other sources later?
- Binding sourcing: do we resolve k8s Secret refs at apply time and push
  the values to Cloudflare, or use the Workers Secrets API and avoid
  exposing the values to the operator at all?
- Versioning + rollback: Cloudflare keeps every version. Do we expose
  `WorkerDeployment` from day one (declarative gradual rollouts) or default
  to "always 100% latest" and add the CRD later?
- Pages legacy: Cloudflare Pages still exists but is no longer the
  recommended path. Do we ignore it entirely (the static/SPA/full-stack
  modes above already cover the same ground via Workers + Assets), or
  ship a thin compatibility shim so existing Pages projects can be
  managed by the operator?
- Container image source: same artifact-delivery question as scripts,
  but for OCI images. Do we require the image already live in a registry
  the Worker upload can reference (Cloudflare's managed registry or an
  external one with stored credentials), or grow a "pull-through" mode
  where the operator pulls from a private registry and re-uploads? The
  former is simpler; the latter avoids holding registry credentials on
  the cluster side.
- Container egress policy ergonomics: `allowedHosts` / `deniedHosts` and
  the runtime `setOutboundByHost` API are powerful but easy to misuse.
  Do we expose them raw in `Worker.spec.containers[]`, or wrap them in a
  higher-level "egress profile" sub-CRD that can be shared across
  Workers?
- Container management API: as of cloudflare-go v6 there's no typed
  client for container-level operations beyond the Worker upload itself.
  If a standalone management API (`/accounts/{account_id}/containers/`
  or `/cloudchamber/`) lands later, we may want a `ContainerInstance`
  status sub-resource — punt until the API stabilizes.

---

## What this page is not

- A commitment. Anything here may be reshaped or dropped after design work.
- A complete list. Other Cloudflare surfaces (Access, Stream, Images,
  Waiting Room, Turnstile, …) may land here later as the operator's scope
  becomes clearer.
