# Changelog

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

### Fixed

- `CloudflareZoneConfig`: a permission/plan failure on one settings group (most commonly `bot_management` on Free zones, or a token without `Zone:Bot Management:Edit`) no longer blocks the rest of the spec from being applied. Each group now records its own `<Group>Applied` status condition with reason `Applied`, `NotConfigured`, `PermissionDenied`, or `CloudflareAPIError`. The resource's `Ready` condition is `False` with `Reason=PartialApply` until every configured group succeeds. ([#51](https://github.com/jacaudi/cloudflare-operator/issues/51))

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
