# Changelog

## [0.17.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.16.0...v0.17.0) (2026-05-08)

### Bug Fixes

* **api:** classify deletion-drain reasons as in-progress, not error ([c13d77a](https://github.com/jacaudi/cloudflare-operator/commit/c13d77a3b47e182ca328192d75d013d5ec2e8c58))
* **tunnel:** drain connector before DeleteTunnel ([#100](https://github.com/jacaudi/cloudflare-operator/issues/100)) ([964d4c5](https://github.com/jacaudi/cloudflare-operator/commit/964d4c51ec42f48060fcf3164e0cfec5570f461b))


### Features

* **cfclient:** add IsTunnelHasActiveConnections predicate ([#100](https://github.com/jacaudi/cloudflare-operator/issues/100)) ([fc37c2a](https://github.com/jacaudi/cloudflare-operator/commit/fc37c2a1dcc4c556371f11b877ab9ff3cd4db36c))
* **controller:** route 400/1022 to TunnelHasConnections ([#100](https://github.com/jacaudi/cloudflare-operator/issues/100)) ([96d83ae](https://github.com/jacaudi/cloudflare-operator/commit/96d83ae97804c22c545bd99aed61210d8c7fee19))

## [0.16.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.15.0...v0.16.0) (2026-05-08)

### Bug Fixes

* **dns:** preserve Status.RecordID on transient GetRecord errors ([#85](https://github.com/jacaudi/cloudflare-operator/issues/85)) ([907aa43](https://github.com/jacaudi/cloudflare-operator/commit/907aa43f6b809436d7853dfa0a5960f7141e5e6b))
* **tunnel:** preserve Status.TunnelID on transient GetTunnel errors ([#85](https://github.com/jacaudi/cloudflare-operator/issues/85)) ([ef9d3f3](https://github.com/jacaudi/cloudflare-operator/commit/ef9d3f3f60b599ab32a703d1f2880fb89ea60903))


### Features

* **connector:** add cleanup helper for disabled connector ([#52](https://github.com/jacaudi/cloudflare-operator/issues/52)) ([05c6474](https://github.com/jacaudi/cloudflare-operator/commit/05c6474b7624164188459633d49e5cf3d2cf516c))
* **connector:** delete connector resources on disable ([#52](https://github.com/jacaudi/cloudflare-operator/issues/52)) ([ed11da3](https://github.com/jacaudi/cloudflare-operator/commit/ed11da3104c67d0ccb73812ea592f24f4799edc9))

## [0.15.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.14.1...v0.15.0) (2026-05-08)

### Bug Fixes

* **connector:** defer legacy cleanup until new connector is Ready ([#93](https://github.com/jacaudi/cloudflare-operator/issues/93)) ([018cc1e](https://github.com/jacaudi/cloudflare-operator/commit/018cc1e595390647520a89f8faf82062c1541c99))
* **connector:** tighten cleanup unowned-test + soften godoc ([#93](https://github.com/jacaudi/cloudflare-operator/issues/93)) ([8c54bdb](https://github.com/jacaudi/cloudflare-operator/commit/8c54bdba25d7c3074829a20cf32ad1ed2e40327e))


### Features

* **connector:** add legacy-name cleanup helper for default rename ([#93](https://github.com/jacaudi/cloudflare-operator/issues/93)) ([c722989](https://github.com/jacaudi/cloudflare-operator/commit/c7229897c084502ea6cad686a380242a6f8ad4bf))
* **connector:** default base name to cloudflared-<tunnel> ([#93](https://github.com/jacaudi/cloudflare-operator/issues/93)) ([53648c4](https://github.com/jacaudi/cloudflare-operator/commit/53648c427710ec63ff2a2efe2835c98c848efee0))
* **connector:** run legacy-name cleanup after successful apply ([#93](https://github.com/jacaudi/cloudflare-operator/issues/93)) ([fbec355](https://github.com/jacaudi/cloudflare-operator/commit/fbec3557d9eccd5fd0f48cc121ec8530a0350d9a))

## [Unreleased]

### Added

- **`CloudflareTunnel.spec.apexHostname`**: opt-in operator-managed apex CNAME for a tunnel. When set, the operator reconciles a single `CloudflareDNSRecord` (named `<tunnel>-apex`) that CNAMEs to the tunnel's `.cfargotunnel.com` address; per-route records emitted by the HTTPRoute and Service source controllers CNAME to the apex instead of the tunnel UUID. Tunnel UUID rotation collapses to one record update — per-route records do not move. New `Status.ApexHostname` and `ApexHostnameReady` condition. Validation refuses to upsert when the apex name doesn't fall under the referenced zone or another `CloudflareDNSRecord` CR in the namespace already claims the same FQDN. Closes #101.

### Changed

- **CloudflareTunnel connector default rename.** The default base name for the operator-managed connector resources is now `cloudflared-<tunnel-name>` (was `<tunnel-name>-connector`). New resource names: Deployment / ServiceAccount `cloudflared-<tun>`, ConfigMap `cloudflared-<tun>-config`, PodDisruptionBudget `cloudflared-<tun>-pdb`. Pods now appear in `kubectl get pods` as `cloudflared-<tun>-...`, matching the Cloudflare upstream Helm chart. The connector reconciler automatically deletes the legacy `<tun>-connector` family of resources owned by your `CloudflareTunnel` on the next reconcile after upgrade, with no traffic gap (the new connector comes up first; both briefly coexist; legacy is then deleted). Users who set `spec.connector.nameOverride` are unaffected and the auto-cleanup is suppressed for them. Update any monitoring/alert filters that match the old pod-name prefix. (#93)
- **Active connector cleanup on `spec.connector.enabled: false`.** Previously, disabling the connector (or removing the `connector` block) left the Deployment, ServiceAccount, ConfigMap, and PodDisruptionBudget stranded until the parent `CloudflareTunnel` was deleted. The operator now deletes every operator-owned connector resource on the next reconcile after disable, discovered via the `app.kubernetes.io/name=cloudflared` + `cloudflare.io/tunnel=<name>` label set in the tunnel's namespace. Hand-applied resources matching the labels but lacking the owner-ref are left alone. As a side effect, resources from prior `spec.connector.nameOverride` values are also cleaned up by the same pass. Closes #52.

### Fixed

- **`CloudflareDNSRecord` rename now propagates to Cloudflare.** Editing `spec.name` on a CR with a populated `Status.RecordID` previously was a silent no-op — the controller's `needsUpdate` predicate did not compare `Name`, so the update branch never fired and the Cloudflare-side record stayed at the old name. The predicate now triggers on Name change and the existing `UpdateRecord` chain renames the record in place (Cloudflare's `PUT /zones/{zone_id}/dns_records/{id}` accepts `Name`, the SDK already passes it through). Removes the previously-documented "renaming the apex is not yet supported" caveat from `docs/tunnels.md` under `Primary apex hostname` (#101 follow-up). Closes #104.
- **Transient errors no longer wipe stored remote IDs.** The DNS and tunnel reconcilers previously cleared `Status.RecordID` / `Status.TunnelID` on any error from the in-flow Get-by-ID call, including transient 5xx responses or network blips. They now gate the ID-clear on `cfclient.IsNotFound(err)` (the same shape the zone reconciler uses); other errors propagate to the existing outer-`Reconcile` classifier, which preserves the stored ID and surfaces a `CloudflareAPIError` (or `PermissionDenied` / `BadRequest` / `PlanTierRequired`) condition `Reason` while the workqueue retries with backoff. Closes #85.
- **Connector deletion no longer deadlocks tunnel removal.** Deleting a `CloudflareTunnel` with `spec.connector.enabled: true` previously looped indefinitely with Cloudflare returning `400 code:1022` ("This tunnel has active connections"), because the operator's own `cloudflared` Deployment kept the tunnel busy and the operator never scaled it down. The deletion path now scales the managed Deployment to `replicas: 0` first, requeues until `Status.ReadyReplicas == 0`, then calls Cloudflare's DELETE. A transient `400 code:1022` returned even after local drain (the rare race where pods are gone but Cloudflare hasn't yet registered all connections closed) routes to a new `TunnelHasConnections` condition reason with a 30-second requeue. The status condition shows `DrainingConnector` while the drain is in progress. No manual `kubectl scale ... --replicas=0` workaround needed. Closes #100.

## [0.14.1](https://github.com/jacaudi/cloudflare-operator/compare/v0.14.0...v0.14.1) (2026-05-06)

### Bug Fixes

* tunnel adoption secret collision and chart rollout strategy default ([#92](https://github.com/jacaudi/cloudflare-operator/issues/92)) ([c8de609](https://github.com/jacaudi/cloudflare-operator/commit/c8de6099262439433a53381fd269a4d57c223ca1)), closes [#90](https://github.com/jacaudi/cloudflare-operator/issues/90)

## [0.14.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.13.0...v0.14.0) (2026-05-05)

### Features

* add Status.Phase enum field to all six CRDs ([#89](https://github.com/jacaudi/cloudflare-operator/issues/89)) ([028cfd2](https://github.com/jacaudi/cloudflare-operator/commit/028cfd2a497bca2c0eadb381b445d4bca6b69820))

## [0.13.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.12.0...v0.13.0) (2026-05-04)

### Features

* label-gated Secret cache filter and SecretNotLabeled disambiguation ([#87](https://github.com/jacaudi/cloudflare-operator/issues/87)) ([22eb749](https://github.com/jacaudi/cloudflare-operator/commit/22eb7496c2b32045789584977b00488f3688d85c))


### BREAKING CHANGES

* User-supplied credential Secrets referenced via
secretRef must carry the new label cloudflare.io/managed=true or the
operator surfaces Ready=False with Reason=SecretNotLabeled. Migration:

  kubectl label secret -n <namespace> <name> cloudflare.io/managed=true

Operators with many Secrets to label can stage the migration by setting
the chart value secretCacheLabelSelector="" (or env
SECRET_CACHE_LABEL_SELECTOR="") to disable the filter, label their
Secrets, then restore the default.

In addition: on the delete-reconcile path, credential-load failures no
longer carry the wrapDeleteErr 'remove the finalizer manually to force
deletion' guidance in Condition.Message; the guidance is preserved in
operator logs. Reason classification on credential-load is now finer-
grained (adds ReasonSecretNotLabeled). New Warning recorder events fire
on credential-load failure during delete only when the failure is
ErrSecretNotLabeled.

* fix(connector): keep cloudflare.io/managed off Deployment+PDB selectors

CRITICAL upgrade hazard caught by independent comprehensive review:
adding cloudflare.io/managed=true to connectorLabels() inadvertently
flowed into Deployment.Spec.Selector and PodDisruptionBudgetSpec.Selector.
Both Spec.Selector fields are immutable — every reconcile on an existing
cluster upgrading from v0.12.0 would have failed with 'field is immutable.'

Fix: split into connectorSelectorLabels (4-key immutable subset) used at
the two Spec.Selector sites, and connectorLabels (5-key superset, includes
cloudflare.io/managed=true) used everywhere else (ObjectMeta.Labels, pod
template Labels, TSC LabelSelector). Pod-template labels carry the full
superset, so Selector matches via subset-of-pod-labels — works on both
old and new pods during a rolling restart.

Adds two regression tests pinning the 4-key selector subset; updates two
pre-existing PDB tests that asserted the 5-key set on Spec.Selector.

* docs(readme): troubleshoot stuck-deleting CRs with SecretNotLabeled

Reviewer feedback on the secret-cache-scoping arc — explicitly point
operators at the operator-log 'Remove the finalizer manually' guidance
when credential-load fails on the delete reconcile path. The Condition
message no longer carries the guidance (failReconcile path doesn't wrap
with wrapDeleteErr), only the operator log does.

## [0.12.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.11.0...v0.12.0) (2026-05-03)

### Features

* distinguish Cloudflare API failures via error classification predicates ([#86](https://github.com/jacaudi/cloudflare-operator/issues/86)) ([80519e3](https://github.com/jacaudi/cloudflare-operator/commit/80519e32460dc11502eb30e992e4a1bc9fb0327b)), closes [#4](https://github.com/jacaudi/cloudflare-operator/issues/4) [#6](https://github.com/jacaudi/cloudflare-operator/issues/6) [#7](https://github.com/jacaudi/cloudflare-operator/issues/7)

## [0.11.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.10.2...v0.11.0) (2026-05-01)

### Bug Fixes

* **connector:** add ownership guard + tighten PDB test assertions ([#77](https://github.com/jacaudi/cloudflare-operator/issues/77)) ([7c5b931](https://github.com/jacaudi/cloudflare-operator/commit/7c5b93165c587b51ea66575afbc897fb69d14448))
* **connector:** address code review feedback for PDB build ([#77](https://github.com/jacaudi/cloudflare-operator/issues/77)) ([7f91fb3](https://github.com/jacaudi/cloudflare-operator/commit/7f91fb3df038a93ef0ea1400e25416bd7712bdf7))
* **connector:** re-fetch existing PDB inside retry-on-conflict closure ([#77](https://github.com/jacaudi/cloudflare-operator/issues/77)) ([f938499](https://github.com/jacaudi/cloudflare-operator/commit/f93849905205dfdff7a8ad10d8ac5942d236a486))


### Features

* **chart:** default PDB and topologySpreadConstraints at replicas>=2 ([#77](https://github.com/jacaudi/cloudflare-operator/issues/77)) ([c8ccbfb](https://github.com/jacaudi/cloudflare-operator/commit/c8ccbfb85f7cd5b11ff3fb62d01006a049c95851))
* **connector:** add PodDisruptionBudget build function ([#77](https://github.com/jacaudi/cloudflare-operator/issues/77)) ([d09e387](https://github.com/jacaudi/cloudflare-operator/commit/d09e3877455d531462cc82cc4c2094fee8f04d6c))
* **connector:** inject default per-hostname topologySpreadConstraint at replicas>=2 ([#77](https://github.com/jacaudi/cloudflare-operator/issues/77)) ([8481698](https://github.com/jacaudi/cloudflare-operator/commit/8481698e408a5aba2fbedddc286cf2afd8bff53e))
* **connector:** reconcile connector PDB and watch owned PDBs ([#77](https://github.com/jacaudi/cloudflare-operator/issues/77)) ([cd8d757](https://github.com/jacaudi/cloudflare-operator/commit/cd8d7578414097249c430a6ae1bb1cab7e087cab))

## [0.10.2](https://github.com/jacaudi/cloudflare-operator/compare/v0.10.0...v0.10.2) (2026-05-01)

### Bug Fixes

* **tunnel:** defaultBackend inherits routing.originRequest ([#81](https://github.com/jacaudi/cloudflare-operator/issues/81)) ([#82](https://github.com/jacaudi/cloudflare-operator/issues/82)) ([7e4ed94](https://github.com/jacaudi/cloudflare-operator/commit/7e4ed94d60f1b20562cdef1d7fb9767ea9ba4f2d))

## [0.10.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.9.2...v0.10.0) (2026-05-01)

* feat(sources)!: rename emitted CR names to <kind>-<source-name>-<hash>[-txt] ([#71](https://github.com/jacaudi/cloudflare-operator/issues/71)) ([2d74b23](https://github.com/jacaudi/cloudflare-operator/commit/2d74b23c3146d2d9c43d49395fb21bcea6b023ff))


### BREAKING CHANGES

* emitted CR names change. Existing CRs are not migrated
and become orphans until owner-ref GC removes them or operators clean up
manually.

## [0.9.2](https://github.com/jacaudi/cloudflare-operator/compare/v0.9.1...v0.9.2) (2026-05-01)

### Bug Fixes

* **connector:** httpGet readiness probe + safe rollout ([#75](https://github.com/jacaudi/cloudflare-operator/issues/75), [#76](https://github.com/jacaudi/cloudflare-operator/issues/76)) ([1dcbfcc](https://github.com/jacaudi/cloudflare-operator/commit/1dcbfcc2c6f716eb4bb5efb7b1f63928b6350b29))

## [0.9.1](https://github.com/jacaudi/cloudflare-operator/compare/v0.9.0...v0.9.1) (2026-05-01)

### Bug Fixes

* **sources:** propagate secret namespace into emitted SecretRef ([#70](https://github.com/jacaudi/cloudflare-operator/issues/70)) ([17b1909](https://github.com/jacaudi/cloudflare-operator/commit/17b19095d1d84b144625890d920cd66f4efb3e1d))

## [0.9.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.8.1...v0.9.0) (2026-05-01)

### Features

* **connector:** add spec.connector.nameOverride to customize resource names ([#68](https://github.com/jacaudi/cloudflare-operator/issues/68)) ([0f87275](https://github.com/jacaudi/cloudflare-operator/commit/0f87275f6867e30c493307a8912db6b98548f4ac))

## [0.8.1](https://github.com/jacaudi/cloudflare-operator/compare/v0.8.0...v0.8.1) (2026-05-01)

### Bug Fixes

* **connector:** drop redundant http_status:404 when defaultBackend is set ([#66](https://github.com/jacaudi/cloudflare-operator/issues/66)) ([2a10741](https://github.com/jacaudi/cloudflare-operator/commit/2a107417e3fe174f5d0bd3bc119c20789855ae72))
* **sources:** propagate zone namespace into emitted ZoneRef ([#65](https://github.com/jacaudi/cloudflare-operator/issues/65)) ([ed78538](https://github.com/jacaudi/cloudflare-operator/commit/ed7853878910fc13c185ba36d2d242746498435b))

## [0.8.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.7.4...v0.8.0) (2026-05-01)

### Bug Fixes

* **connector:** drop redundant --credentials-file Args ([#58](https://github.com/jacaudi/cloudflare-operator/issues/58)) ([a8d5833](https://github.com/jacaudi/cloudflare-operator/commit/a8d58336da88b49bf12a0c9b9f8ff005e1f66de9))
* **connector:** render tunnel and credentials-file in config.yaml ([#58](https://github.com/jacaudi/cloudflare-operator/issues/58)) ([e1055ef](https://github.com/jacaudi/cloudflare-operator/commit/e1055efa53ff980124cf6254900b166381e42a00))
* **connector:** retry on conflict in applyOwned (SA, ConfigMap) ([#59](https://github.com/jacaudi/cloudflare-operator/issues/59)) ([655e640](https://github.com/jacaudi/cloudflare-operator/commit/655e64097f0e7e22e6dcd06f1ecacbcef44efbcd))
* **connector:** retry on conflict when updating connector Deployment ([#59](https://github.com/jacaudi/cloudflare-operator/issues/59)) ([f6a1d72](https://github.com/jacaudi/cloudflare-operator/commit/f6a1d729622271db326f2c904d296098d87f2f58))


### Features

* **connector:** fail loud when Status.TunnelID is empty ([3ad01b2](https://github.com/jacaudi/cloudflare-operator/commit/3ad01b25c3907d9979d8a67130999040d8a9d97d))

## [0.7.4](https://github.com/jacaudi/cloudflare-operator/compare/v0.7.3...v0.7.4) (2026-05-01)

### Bug Fixes

* **deps:** update module sigs.k8s.io/gateway-api to v1.5.1 ([4fd3e67](https://github.com/jacaudi/cloudflare-operator/commit/4fd3e670ff99dcf481d7c933f70d207a78f0400b))

## [0.7.3](https://github.com/jacaudi/cloudflare-operator/compare/v0.7.2...v0.7.3) (2026-05-01)

### Bug Fixes

* **deps:** update kubernetes-client-libraries ([7f064c2](https://github.com/jacaudi/cloudflare-operator/commit/7f064c235541cdd373dca5fca2e01fef94d900c5))

## [0.7.2](https://github.com/jacaudi/cloudflare-operator/compare/v0.7.1...v0.7.2) (2026-05-01)

### Bug Fixes

* **deps:** update module github.com/cloudflare/cloudflare-go/v6 to v6.10.0 ([ee06089](https://github.com/jacaudi/cloudflare-operator/commit/ee06089394aa6b0383609c8059b68f8162e455ae))
* **deps:** update test-libraries ([722cd83](https://github.com/jacaudi/cloudflare-operator/commit/722cd832afa8e11913e9ac08ec5cee00e66f2230))

## [0.7.1](https://github.com/jacaudi/cloudflare-operator/compare/v0.7.0...v0.7.1) (2026-05-01)

### Bug Fixes

* **deps:** update module golang.org/x/sync to v0.20.0 ([7270f2c](https://github.com/jacaudi/cloudflare-operator/commit/7270f2c14b04e8fca1b1e54fedf4ba2c7b6af0e5))

## [0.7.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.6.2...v0.7.0) (2026-04-30)

### Features

* **crd:** close TF-parity gaps on CloudflareZoneConfig + Ruleset ([#57](https://github.com/jacaudi/cloudflare-operator/issues/57)) ([#61](https://github.com/jacaudi/cloudflare-operator/issues/61)) ([90f7e02](https://github.com/jacaudi/cloudflare-operator/commit/90f7e02e5f5632f4a88ab60c763698e50f261bfb))

## [0.6.2](https://github.com/jacaudi/cloudflare-operator/compare/v0.6.1...v0.6.2) (2026-04-28)

### Bug Fixes

* **zoneconfig:** populate status.zoneID and use it for printcolumn ([d4c4c9a](https://github.com/jacaudi/cloudflare-operator/commit/d4c4c9ab9799990f6c581864a56af06a4cc8d60d))

## [0.6.1](https://github.com/jacaudi/cloudflare-operator/compare/v0.6.0...v0.6.1) (2026-04-27)

### Bug Fixes

* **zoneconfig:** apply settings groups independently (closes [#51](https://github.com/jacaudi/cloudflare-operator/issues/51)) ([#56](https://github.com/jacaudi/cloudflare-operator/issues/56)) ([f846ae3](https://github.com/jacaudi/cloudflare-operator/commit/f846ae3a618ec2218461d9a7dd4b5ed7d7ed1c7e))

## Unreleased

### Added

- `CloudflareTunnel.spec.connector.nameOverride`: optional field to set the base name used for the operator-managed Deployment, ServiceAccount, and ConfigMap. When set, the Deployment and ServiceAccount are named exactly `<nameOverride>` and the ConfigMap is named `<nameOverride>-config`. When unset, the existing `<tunnel.metadata.name>-connector` family is preserved. ([#68](https://github.com/jacaudi/cloudflare-operator/issues/68))

### Fixed

- `CloudflareZoneConfig`: a permission/plan failure on one settings group (most commonly `bot_management` on Free zones, or a token without `Zone:Bot Management:Edit`) no longer blocks the rest of the spec from being applied. Each group now records its own `<Group>Applied` status condition with reason `Applied`, `NotConfigured`, `PermissionDenied`, or `CloudflareAPIError`. The resource's `Ready` condition is `False` with `Reason=PartialApply` until every configured group succeeds. ([#51](https://github.com/jacaudi/cloudflare-operator/issues/51))
- `CloudflareTunnel.spec.connector`: the operator-managed `cloudflared` Deployment now starts cleanly. Previously the rendered Args ended at `tunnel ... run` with no positional tunnel UUID and the rendered `config.yaml` contained only an `ingress:` block, so cloudflared exited with `"cloudflared tunnel run" requires the ID or name of the tunnel`. The aggregator now writes top-level `tunnel:` and `credentials-file:` keys into the rendered config so cloudflared can resolve the tunnel from the config alone; the Deployment Args drop `--credentials-file` accordingly so identity has a single source of truth. ([#58](https://github.com/jacaudi/cloudflare-operator/issues/58))
- `CloudflareTunnel`: deletion no longer wedges when the connector reconcile loop is hitting sustained optimistic-concurrency conflicts. The connector Deployment, ConfigMap, and ServiceAccount apply paths now retry conflicts in-process via `retry.RetryOnConflict`, so transient ResourceVersion churn does not propagate as reconcile errors that inflate the controller workqueue's exponential backoff. Finalizer cleanup runs promptly without an operator pod restart. ([#59](https://github.com/jacaudi/cloudflare-operator/issues/59))
- HTTPRoute and Service source controllers now propagate the `CloudflareZone` CR's namespace into the emitted `CloudflareDNSRecord.spec.zoneRef.namespace`. Previously, only `zoneRef.name` was set; the downstream `ResolveZoneID` helper defaulted the lookup to the dependent CR's own namespace, so reconciliation failed with `CloudflareZone … not found` whenever the zone CR lived in a different namespace from the source object. The `ZoneReference` API type gains an optional `namespace` field for this. ([#65](https://github.com/jacaudi/cloudflare-operator/issues/65))
- `CloudflareTunnel.spec.routing.defaultBackend`: cloudflared no longer crash-loops when a default backend is configured and zero `CloudflareTunnelRule` entries are included in aggregation. The aggregator previously emitted both the user's `defaultBackend` (a no-`hostname:` catch-all) AND the auto-appended `http_status:404`, which cloudflared correctly rejects as two consecutive catch-alls (`Rule #1 is matching the hostname '', but this will match every hostname`). The trailing `http_status:404` is now emitted only when no `defaultBackend` is set. ([#66](https://github.com/jacaudi/cloudflare-operator/issues/66))

## [0.6.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.5.1...v0.6.0) (2026-04-26)

### Features

* v1 Gateway API sources + tunnel runtime (closes [#44](https://github.com/jacaudi/cloudflare-operator/issues/44) [#47](https://github.com/jacaudi/cloudflare-operator/issues/47) [#48](https://github.com/jacaudi/cloudflare-operator/issues/48) [#49](https://github.com/jacaudi/cloudflare-operator/issues/49)) ([#53](https://github.com/jacaudi/cloudflare-operator/issues/53)) ([106f621](https://github.com/jacaudi/cloudflare-operator/commit/106f62156a208dfc7e8d6c516ea14ed90d4b002a)), closes [#46](https://github.com/jacaudi/cloudflare-operator/issues/46) [#52](https://github.com/jacaudi/cloudflare-operator/issues/52)

### Added
- New CRD `CloudflareTunnelRule` — primitive for cloudflared ingress rules, emitted by source controllers or hand-authored.
- `CloudflareTunnel.spec.connector` — operator-managed cloudflared Deployment + ConfigMap + ServiceAccount.
- `CloudflareTunnel.spec.routing.defaultBackend` — tunnel-wide default backend routing.
- New source controllers:
  - `httproute_source` — watches Gateway API `HTTPRoute` + `Gateway`, emits DNS from `cloudflare.io/*` annotations.
  - `service_source` — watches `Service`, emits DNS + TunnelRule from `cloudflare.io/*` annotations.
- External-dns-compatible plaintext TXT ownership registry in `internal/cloudflare/txt_registry.go` for record adoption and conflict detection during migration.
- New operator config: `TXT_OWNER_ID` (required to activate sources), `TXT_IMPORT_OWNERS`, `TXT_PREFIX`, `TXT_SUFFIX`, `TXT_WILDCARD_REPLACEMENT`.

### Changed
- RBAC cluster role now includes cluster-wide write on `deployments`/`configmaps`/`serviceaccounts` (needed because tunnel CRs can live in any namespace). Scoped in practice by ownerRef to `CloudflareTunnel`.
- Operator image requires `sigs.k8s.io/gateway-api` CRDs installed in the cluster.

### Migration
- v0.5.x → v0.6.0 is a pure add. Without `TXT_OWNER_ID` set, annotation-driven sources are inert and existing behavior is unchanged.
- To activate: install Gateway API CRDs → set `TXT_OWNER_ID` → annotate workloads. See [docs/external-dns-migration.md](docs/external-dns-migration.md) for drop-in paths from external-dns.

## [0.5.1](https://github.com/jacaudi/cloudflare-operator/compare/v0.5.0...v0.5.1) (2026-04-21)

### Bug Fixes

* **controller:** log zone-ref-not-ready at Info, not Error ([#41](https://github.com/jacaudi/cloudflare-operator/issues/41)) ([8e3e049](https://github.com/jacaudi/cloudflare-operator/commit/8e3e049ec2424fa02a8e142d5b0d12200d8a0410))
* **ruleset:** use phase entrypoint API instead of POSTing new custom ruleset ([#45](https://github.com/jacaudi/cloudflare-operator/issues/45)) ([68d7106](https://github.com/jacaudi/cloudflare-operator/commit/68d71068a1bd5cf0f7c132dfde4bcf333291a352)), closes [#43](https://github.com/jacaudi/cloudflare-operator/issues/43)

## [0.5.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.4.0...v0.5.0) (2026-04-21)

* feat!: account ID to secret + pipeline alignment with nextdns-operator ([#38](https://github.com/jacaudi/cloudflare-operator/issues/38)) ([32989f9](https://github.com/jacaudi/cloudflare-operator/commit/32989f98be742ecd928c633c2e39e105682104e6))


### BREAKING CHANGES

* spec.accountID has been removed from CloudflareZone
and CloudflareTunnel. Add an `accountID` key to the API token Secret
and remove the field from existing CRs before upgrading.

## [0.4.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.3.1...v0.4.0) (2026-04-19)

### Features

* **zoneconfig:** spec-hash drift detection, drop AppliedSettings ([da2a550](https://github.com/jacaudi/cloudflare-operator/commit/da2a55003a7e0d1d61c5cca2f4aa6240f6c2e3a5))

## [0.3.1](https://github.com/jacaudi/cloudflare-operator/compare/v0.3.0...v0.3.1) (2026-04-18)

### Bug Fixes

* **ci:** use v-prefixed tag for container and helm chart releases ([d3e6beb](https://github.com/jacaudi/cloudflare-operator/commit/d3e6beb31f5da18552958a9cd0c5e22e35d96de8))

## [0.3.0](https://github.com/jacaudi/cloudflare-operator/compare/v0.2.0...v0.3.0) (2026-04-18)

* feat(logging)!: replace zap with log/slog in main ([d34966f](https://github.com/jacaudi/cloudflare-operator/commit/d34966f2bbb2bf0035f6e11c143e755f73634ad9)), closes [#19](https://github.com/jacaudi/cloudflare-operator/issues/19)


### Bug Fixes

* **deps:** update kubernetes-client-libraries ([2426281](https://github.com/jacaudi/cloudflare-operator/commit/2426281e9117602f9bbcde8456d7c73bedd0b298))
* **deps:** update test-libraries ([64c8ca3](https://github.com/jacaudi/cloudflare-operator/commit/64c8ca3080a4d051f9998fc502f78d874f327aec))


### Features

* **logging:** add slog setupLogger helper with tests ([0428618](https://github.com/jacaudi/cloudflare-operator/commit/042861899a11018adb5691adf9cc7142925d5ae2)), closes [#19](https://github.com/jacaudi/cloudflare-operator/issues/19)


### BREAKING CHANGES

* --zap-devel, --zap-encoder, --zap-log-level,
--zap-stacktrace-level, --zap-time-encoding are no longer accepted.
Use --log-level (debug|info|warn|error) and --log-format (json|text)
instead.
