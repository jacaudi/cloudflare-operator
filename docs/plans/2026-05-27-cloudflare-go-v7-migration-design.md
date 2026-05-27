# cloudflare-go v6 → v7 migration — design

**Status:** approved (design phase)
**Date:** 2026-05-27
**Feature name:** `cloudflare-go-v7-migration`
**Pair:** implementation plan at `docs/plans/2026-05-27-cloudflare-go-v7-migration-implementation.md` (to be written by `superpowers:writing-plans`)

## Context

The operator currently depends on `github.com/cloudflare/cloudflare-go/v6 v6.10.0`. Renovate has attempted the major-version bump to v7 several times (most recently PR #140), but those PRs are pure `go.mod` substitutions and don't migrate Go source files — they leave the build broken because every file under `internal/cloudflare/` still imports the `/v6` paths. PR #140 was closed with a note that this needs a manual code change.

A pre-design research pass (`upgrade-researcher` agent, walking the [official v7 migration guide](https://github.com/cloudflare/cloudflare-go/blob/main/docs/migration-guides/v7.0.0-migration-guide.md) and the published v6.10.0 and v7.3.0 source trees) established that the migration is mechanical:

- The v7.0.0 breaking changes are confined to three packages: `ai_search`, `email_security`, `workers`. None of these are imported by this repo.
- Every package this repo *does* import (`option`, `dns`, `zones`, `zero_trust`, `rulesets`, `bot_management`, `shared`) is structurally identical between v6.10.0 and v7.3.0 — same method signatures, same struct fields, same error types, same pagination types, same constants.
- The SDK is generated from Stainless against Cloudflare's OpenAPI spec, so there are no hand-edited evolutions to reason about.

Conclusion: this is a module-path rename plus `go mod tidy`. No call-site logic changes.

## Scope

**In scope**

- Bump `github.com/cloudflare/cloudflare-go` from `v6 v6.10.0` to `v7 v7.3.0`.
- Rewrite every `"github.com/cloudflare/cloudflare-go/v6"` import (and `/v6/<subpkg>`) to the matching `/v7` path across all 11 affected Go files under `internal/cloudflare/`.
- Update `go.mod` and `go.sum`.
- Verify with the existing test suite (unit + envtest) and CI.

**Out of scope**

- No call-site logic changes. The API surface is identical; nothing else needs to move.
- No adoption of v7-only additions (`Records.Batch`, `Records.Edit`). Researcher found them unneeded for this repo.
- No structural refactor of `internal/cloudflare/`, no interface tightening, no mock changes.
- No edits to the `internal/cloudflare/mock/` package — it satisfies this repo's own interfaces (`interfaces.go`), not SDK types, and does not import the SDK.

## Mechanics

Every changed line falls into one of two categories:

### Import-path rewrites

For every Go file under `internal/cloudflare/` that imports the SDK, rewrite the literal import string:

- `"github.com/cloudflare/cloudflare-go/v6"` → `"github.com/cloudflare/cloudflare-go/v7"`
- `"github.com/cloudflare/cloudflare-go/v6/option"` → `"github.com/cloudflare/cloudflare-go/v7/option"`
- `"github.com/cloudflare/cloudflare-go/v6/dns"` → `"github.com/cloudflare/cloudflare-go/v7/dns"`
- `"github.com/cloudflare/cloudflare-go/v6/zones"` → `"github.com/cloudflare/cloudflare-go/v7/zones"`
- `"github.com/cloudflare/cloudflare-go/v6/zero_trust"` → `"github.com/cloudflare/cloudflare-go/v7/zero_trust"`
- `"github.com/cloudflare/cloudflare-go/v6/rulesets"` → `"github.com/cloudflare/cloudflare-go/v7/rulesets"`
- `"github.com/cloudflare/cloudflare-go/v6/bot_management"` → `"github.com/cloudflare/cloudflare-go/v7/bot_management"`
- `"github.com/cloudflare/cloudflare-go/v6/shared"` → `"github.com/cloudflare/cloudflare-go/v7/shared"`

The import alias `cfgo` is kept as-is — it is intentionally version-agnostic in name.

### go.mod / go.sum

- `go.mod` require line `github.com/cloudflare/cloudflare-go/v6 v6.10.0` → `github.com/cloudflare/cloudflare-go/v7 v7.3.0`
- `go mod tidy` resolves `go.sum` deterministically.

### File inventory (exhaustive)

Production code (7 files):
- `internal/cloudflare/client.go` — imports `v6`, `v6/option`
- `internal/cloudflare/dns.go` — imports `v6`, `v6/dns`
- `internal/cloudflare/zone.go` — imports `v6`, `v6/zones`
- `internal/cloudflare/zone_lifecycle.go` — imports `v6`, `v6/zones`
- `internal/cloudflare/ruleset.go` — imports `v6`, `v6/rulesets`
- `internal/cloudflare/tunnel.go` — imports `v6`, `v6/zero_trust`
- `internal/cloudflare/zoneconfig.go` — imports `v6`, `v6/bot_management`, `v6/zones`

Tests (4 files):
- `internal/cloudflare/dns_test.go` — imports `v6`, `v6/dns`
- `internal/cloudflare/tunnel_test.go` — imports `v6`, `v6/zero_trust`
- `internal/cloudflare/zone_lifecycle_test.go` — imports `v6`
- `internal/cloudflare/zoneconfig_test.go` — imports `v6`, `v6/shared`

Module:
- `go.mod`
- `go.sum`

Total: 11 Go files + 2 module files.

## Verification

Strong signals — all must be green before merge:

- `go build ./...` clean
- `go vet ./...` clean
- `make test` (unit + envtest) green locally on the migration commit
- CI on the PR: `Run Tests / Test`, `Envtest Suite`, every `Lint Code / Lint (*)` job green

Implicit signal: the researcher's source-level diff against the published v6.10.0 and v7.3.0 trees shows every type, method signature, struct field, and constant used in this repo is byte-for-byte identical. Existing tests passing is therefore high-confidence evidence of behavioral equivalence.

## Risks and mitigations

**Researcher missed a difference.** Possible but low-probability — the source-level diff was direct, not hearsay. Mitigation is the compiler: any signature mismatch fails `go build` immediately, with the failure local to one file and obvious. Runtime drift would surface in the existing test suite.

**Transitive-dep churn from `go mod tidy`.** v7's transitive tree should be near-identical to v6's (same SDK generator, same supporting libs), but `go mod tidy` may pull in newer indirect minors as a side-effect of the require-line change. Mitigation: review the `go.sum` diff before committing; reject any unrelated major-version bumps that creep in. Acceptable churn is patch / minor on indirect deps only.

**Renovate will try to redo this after merge.** This repo's Renovate config tracks cloudflare-go and has previously opened PRs for v6→v7. Mitigation: after this branch merges, Renovate stops proposing v7 (since `go.mod` is already on v7). It will not re-open until a v8 is published — at which point a separate migration is warranted on its own merits.

**Mock divergence.** Out of scope by design — but I will verify zero diff under `internal/cloudflare/mock/` before pushing, as a sanity check that the SDK rename didn't accidentally reach into mock code.

## Branch / PR shape

- **Branch:** `chore/cloudflare-go-v7` off `main` (`chore/` prefix matches the commit's semantic-prefix; no existing convention in the repo dictates otherwise).
- **Commit:** one commit, message `chore(deps): migrate cloudflare-go from v6 to v7`.
  - Semantic prefix is `chore` (dependency upgrade with no behavioral change), not `feat`.
  - Body explains the trigger (Renovate's PR #140 couldn't auto-merge), the conclusion (mechanical rename), and the verification (researcher diff + tests).
- **PR:** one PR; expect green CI on first run. PR body references the closed PR #140 and the [official v7 migration guide](https://github.com/cloudflare/cloudflare-go/blob/main/docs/migration-guides/v7.0.0-migration-guide.md).

## Execution

Per `rules/plan-workflow.md`, execution follows the standard bundle once the implementation plan is written:

1. `superpowers:using-git-worktrees` — isolate work in a dedicated worktree
2. `superpowers:subagent-driven-development` — dispatch a fresh subagent for the (single-task) implementation
3. `superpowers:test-driven-development` — TDD discipline within the subagent
4. `superpowers:verification-before-completion` — verify tests pass before claiming complete
5. `superpowers:requesting-code-review` — per-task review (built into subagent-driven-development)
6. Final independent code review on the full diff from branch point
7. `superpowers:finishing-a-development-branch` — complete the branch

Skills carry their own model and effort settings — those will not be overridden.
