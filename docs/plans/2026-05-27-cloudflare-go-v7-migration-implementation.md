# cloudflare-go v6 → v7 migration — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bump `github.com/cloudflare/cloudflare-go` from v6.10.0 to v7.3.0 across the operator with zero call-site logic changes, verified by the existing unit + envtest suites.

**Architecture:** Single-commit atomic module-path rename on a dedicated branch off `main`. The migration is mechanical — every changed line is either an import-path rewrite (`/v6` → `/v7`) or a `go.mod`/`go.sum` update. Pre-design research established v7's breaking changes are confined to packages this repo doesn't import; every package this repo does import is structurally identical between v6.10.0 and v7.3.0 (same method signatures, struct fields, error types, constants, pagination types). The existing test suite is therefore a sufficient correctness check.

**Tech Stack:** Go 1.26.2, sigs.k8s.io/controller-runtime, sigs.k8s.io/gateway-api v1.5.1, github.com/cloudflare/cloudflare-go v6 (current) / v7 (target).

**Spec:** [`docs/plans/2026-05-27-cloudflare-go-v7-migration-design.md`](2026-05-27-cloudflare-go-v7-migration-design.md)

> **For Claude:** REQUIRED EXECUTION WORKFLOW (follow in order):
> 1. `superpowers:using-git-worktrees` — Isolate work in a dedicated worktree
> 2. `superpowers:subagent-driven-development` — Dispatch a fresh subagent per task
> 3. `superpowers:test-driven-development` — All subagents use TDD
> 4. `superpowers:verification-before-completion` — Verify all tests pass per task
> 5. `superpowers:requesting-code-review` — Code review after each task (built in)
> 6. After all tasks: comprehensive code review on full diff from branch point (automatic)
> 7. `superpowers:finishing-a-development-branch` — Complete the branch
>
> Skills carry their own model and effort settings. Do not override them.

---

## File map

The migration touches exactly these files. No new files are created; no files are deleted.

**Production code (7 files, all under `internal/cloudflare/`):**
- `client.go` — imports `v6`, `v6/option`
- `dns.go` — imports `v6`, `v6/dns`
- `zone.go` — imports `v6`, `v6/zones`
- `zone_lifecycle.go` — imports `v6`, `v6/zones`
- `ruleset.go` — imports `v6`, `v6/rulesets`
- `tunnel.go` — imports `v6`, `v6/zero_trust`
- `zoneconfig.go` — imports `v6`, `v6/bot_management`, `v6/zones`

**Tests (4 files, all under `internal/cloudflare/`):**
- `dns_test.go` — imports `v6`, `v6/dns`
- `tunnel_test.go` — imports `v6`, `v6/zero_trust`
- `zone_lifecycle_test.go` — imports `v6`
- `zoneconfig_test.go` — imports `v6`, `v6/shared`

**Module files:**
- `go.mod`
- `go.sum`

**Verified untouched:**
- `internal/cloudflare/mock/` (entire subtree) — mocks satisfy this repo's own interfaces in `interfaces.go`, do not import the SDK.

---

## Task 1: Capture pre-migration baseline

**Files:** None modified. Read-only verification step.

This task establishes the "tests pass before change" baseline that the subsequent migration must preserve. It is the GREEN state before the RED change in Task 2.

- [ ] **Step 1.1: Confirm on the migration branch with a clean tree**

```bash
git status
git branch --show-current
```

Expected: working tree clean; current branch is `chore/cloudflare-go-v7` (created by the worktree skill that runs before this plan).

- [ ] **Step 1.2: Confirm baseline build is clean**

```bash
go build ./...
go vet ./...
```

Expected: both exit 0 with no output. If either fails, stop — main itself is broken and this plan cannot proceed.

- [ ] **Step 1.3: Confirm baseline unit + envtest pass**

```bash
make test
```

Expected: every Go package reports `ok`, no FAIL lines. envtest brings up the test apiserver and exercises the gateway-api / DNS fixtures. Takes 90–150 seconds.

If `make test` fails on a known flake (e.g. `§10.4 TXT-adoption`), rerun once before declaring baseline broken. If it fails consistently, stop the plan — fix the flake on `main` first.

- [ ] **Step 1.4: Record current cloudflare-go pins**

```bash
grep "cloudflare-go" go.mod
grep "cloudflare-go" go.sum | head
```

Expected pre-migration state:
- `go.mod` contains exactly: `github.com/cloudflare/cloudflare-go/v6 v6.10.0`
- `go.sum` contains entries for `v6.10.0` only (no `v7` entries)
- No `cloudflare-go/v7` line anywhere

If actual state differs, stop and investigate before proceeding.

---

## Task 2: Bump `go.mod` to v7 only (RED — the failing test)

**Files:**
- Modify: `go.mod` (one line)

This task makes the build fail by declaring v7 in `go.mod` while every Go source file still imports `/v6`. The compilation failure is the test that proves Task 3's import rewrite is meaningful. **Do not commit at the end of this task — the tree is intentionally broken.**

- [ ] **Step 2.1: Update the `cloudflare-go` require line in `go.mod`**

```diff
 require (
-	github.com/cloudflare/cloudflare-go/v6 v6.10.0
+	github.com/cloudflare/cloudflare-go/v7 v7.3.0
 	github.com/go-logr/zapr v1.3.0
```

Use the `Edit` tool with `old_string: "\tgithub.com/cloudflare/cloudflare-go/v6 v6.10.0"` and `new_string: "\tgithub.com/cloudflare/cloudflare-go/v7 v7.3.0"`. (The literal `\t` is a tab; `go.mod`'s require block is tab-indented.)

- [ ] **Step 2.2: Run `go build` and confirm it fails**

```bash
go build ./... 2>&1 | head -20
```

Expected: build fails with import errors like:

```
internal/cloudflare/client.go:13:2: no required module provides package github.com/cloudflare/cloudflare-go/v6; to add it:
        go get github.com/cloudflare/cloudflare-go/v6
```

If the build succeeds at this point, something is wrong (perhaps v6 lingers in `go.sum`). Stop and investigate.

This compilation failure is the explicit RED step — proof that Task 3's import rewrite is load-bearing.

---

## Task 3: Rewrite all import paths (GREEN — make the test pass)

**Files (11 total):**
- Modify: `internal/cloudflare/client.go`
- Modify: `internal/cloudflare/dns.go`
- Modify: `internal/cloudflare/zone.go`
- Modify: `internal/cloudflare/zone_lifecycle.go`
- Modify: `internal/cloudflare/ruleset.go`
- Modify: `internal/cloudflare/tunnel.go`
- Modify: `internal/cloudflare/zoneconfig.go`
- Modify: `internal/cloudflare/dns_test.go`
- Modify: `internal/cloudflare/tunnel_test.go`
- Modify: `internal/cloudflare/zone_lifecycle_test.go`
- Modify: `internal/cloudflare/zoneconfig_test.go`

Each file gets one or more literal-string replacements. The import alias (`cfgo`, `dns`, `zones`, etc. — whatever the source file uses) is preserved exactly; only the quoted import path changes.

- [ ] **Step 3.1: Rewrite the top-level `cloudflare-go/v6` import**

For every file in the list above, replace the literal string:

```
github.com/cloudflare/cloudflare-go/v6"
```

with:

```
github.com/cloudflare/cloudflare-go/v7"
```

(Trailing closing quote is included to avoid matching subpackage paths in this pass — those are handled in subsequent steps. Note: this also won't match e.g. `/v6/dns"` because the `"` is in the way; that's intentional.)

Use the `Edit` tool with `replace_all: false` once per file (each file has exactly one top-level `v6"` import).

- [ ] **Step 3.2: Rewrite the subpackage imports**

For each subpackage, search and replace across the 11 files. Use one `Edit` call per (file, subpackage) pair, or one `Bash` step using `find … -exec` if preferred — the result must be identical regardless of tool choice:

| `old_string` | `new_string` |
|---|---|
| `"github.com/cloudflare/cloudflare-go/v6/option"` | `"github.com/cloudflare/cloudflare-go/v7/option"` |
| `"github.com/cloudflare/cloudflare-go/v6/dns"` | `"github.com/cloudflare/cloudflare-go/v7/dns"` |
| `"github.com/cloudflare/cloudflare-go/v6/zones"` | `"github.com/cloudflare/cloudflare-go/v7/zones"` |
| `"github.com/cloudflare/cloudflare-go/v6/zero_trust"` | `"github.com/cloudflare/cloudflare-go/v7/zero_trust"` |
| `"github.com/cloudflare/cloudflare-go/v6/rulesets"` | `"github.com/cloudflare/cloudflare-go/v7/rulesets"` |
| `"github.com/cloudflare/cloudflare-go/v6/bot_management"` | `"github.com/cloudflare/cloudflare-go/v7/bot_management"` |
| `"github.com/cloudflare/cloudflare-go/v6/shared"` | `"github.com/cloudflare/cloudflare-go/v7/shared"` |

A scripted single-shot equivalent (idempotent, safe to re-run):

```bash
grep -rl "github.com/cloudflare/cloudflare-go/v6" internal/cloudflare/ | xargs sed -i '' 's|cloudflare-go/v6|cloudflare-go/v7|g'
```

(macOS `sed -i ''`; Linux uses `sed -i`. Use whichever the executor's environment requires.)

- [ ] **Step 3.3: Verify zero `v6` import strings remain**

```bash
grep -rn "cloudflare-go/v6" --include="*.go" . ; echo "exit: $?"
```

Expected: no matches; `grep` exits 1 (which is fine).

If any matches surface, fix them by hand and re-run.

- [ ] **Step 3.4: Verify the build is now clean**

```bash
go build ./...
```

Expected: exit 0, no output. This is the GREEN step — the import rewrite makes the package compile again.

If the build fails at this point, the failure is local to one file and the error message names it. Fix the issue (likely a typo in the rewrite) and retry. **Do not skip ahead while build is red.**

---

## Task 4: Tidy `go.sum` and verify scope of dep churn

**Files:**
- Modify: `go.sum`

`go mod tidy` rewrites `go.sum` to match the new `go.mod` exactly. Because the v7 SDK's transitive tree is structurally near-identical to v6's (same Stainless generator, same supporting libs), the diff should be limited to cloudflare-go's own checksums. Any unrelated major-version bumps in transitive deps are red flags and must be rejected.

- [ ] **Step 4.1: Run `go mod tidy`**

```bash
go mod tidy 2>&1 | tail -20
```

Expected: a few lines of `go: downloading …` for v7 packages, no errors, no `ambiguous import` complaints. If tidy reports an error, stop and surface it — that's outside the design's risk envelope.

- [ ] **Step 4.2: Inspect the `go.sum` diff for unexpected churn**

```bash
git diff --stat go.mod go.sum
git diff go.mod
git diff go.sum | grep "^[+-]" | grep -v "^[+-][+-][+-]" | head -40
```

Expected `go.mod` diff: exactly two lines changed — the cloudflare-go require line. No new direct require entries; no removed entries beyond v6.

Expected `go.sum` diff: removal of `cloudflare-go/v6` entries; addition of `cloudflare-go/v7` entries; *possibly* small patch / minor version drift on indirect deps shared with the SDK.

**Reject the migration if:** an unrelated indirect dep bumps a major version (e.g. `cloudflare-go`-adjacent deps moving across a major), or a brand-new direct require appears. Per the spec's risk section, this would be unexpected and warrants stopping.

- [ ] **Step 4.3: Verify `internal/cloudflare/mock/` is untouched**

```bash
git diff --stat internal/cloudflare/mock/
```

Expected: empty (no output beyond the header — mock package was out of scope and must not have been modified by the rewrite).

If anything under `mock/` shows as modified, revert those files (`git checkout -- internal/cloudflare/mock/`) and investigate.

- [ ] **Step 4.4: Verify zero `cloudflare-go/v6` strings remain anywhere**

```bash
grep -rn "cloudflare-go/v6" --include="*.go" --include="go.mod" --include="go.sum" . ; echo "exit: $?"
```

Expected: no matches; `grep` exits 1.

---

## Task 5: Run full verification suite

**Files:** None modified. Read-only verification step.

This task confirms that the post-migration state is functionally equivalent to the pre-migration baseline captured in Task 1.

- [ ] **Step 5.1: Vet the entire module**

```bash
go vet ./...
```

Expected: exit 0, no output. (Pre-existing deprecation warnings about `mgr.GetEventRecorderFor` are emitted by the LSP/IDE but not by `go vet` directly — they should not appear here. If they do, the IDE behavior changed and that's unrelated to this migration.)

- [ ] **Step 5.2: Run the full test suite**

```bash
make test
```

Expected: every package reports `ok`; envtest passes all `§N.N` cases; no FAIL lines. Same end-state as Task 1's baseline. Allow ~90–150s.

If a flake recurs (`§10.4 TXT-adoption`), retry once. Persistent failures other than that known flake indicate a real divergence and must be investigated — diff the failing test against `main` to see if the migration is implicated.

- [ ] **Step 5.3: Lint coverage spot-check**

```bash
gofmt -l internal/cloudflare/
```

Expected: empty (no files need reformatting). Replacement is identical-length so formatting should be undisturbed, but this confirms.

---

## Task 6: Commit, push, open PR

**Files:**
- Commit: all changes from Tasks 2–4 in one atomic commit.

- [ ] **Step 6.1: Stage the migration**

```bash
git add go.mod go.sum internal/cloudflare/
git status
```

Expected `git status` (Changes to be committed):
- modified: `go.mod`
- modified: `go.sum`
- modified: 11 files under `internal/cloudflare/` (7 production + 4 tests, none under `mock/`)

Total: 13 files.

- [ ] **Step 6.2: Confirm the staged diff is migration-only**

```bash
git diff --staged --stat
```

Spot-check: no `mock/` files; no files outside `internal/cloudflare/` and the two module files; no Go file whose diff is anything other than the import-string change.

- [ ] **Step 6.3: Commit with the planned message**

```bash
git commit -m "$(cat <<'EOF'
chore(deps): migrate cloudflare-go from v6 to v7

Renovate's PR #140 attempted this bump as a go.mod-only substitution
and failed CI because every internal/cloudflare/*.go still imports
the /v6 paths. This commit rewrites those imports across 11 files
and updates go.mod / go.sum to v7.3.0.

Per the v7.0.0 migration guide
(https://github.com/cloudflare/cloudflare-go/blob/main/docs/migration-guides/v7.0.0-migration-guide.md)
the breaking changes are confined to the ai_search, email_security,
and workers packages, none of which this repo imports. The packages
in use (option, dns, zones, zero_trust, rulesets, bot_management,
shared) are structurally identical between v6.10.0 and v7.3.0 —
same method signatures, struct fields, error types, pagination
types, and constants. No call-site logic changes.

Verification: go build / go vet clean; make test green (unit +
envtest). Mock package under internal/cloudflare/mock/ is unchanged
by design (does not import the SDK).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

Expected: `git log -1 --oneline` shows a single new commit on top of main with the chore(deps) prefix.

- [ ] **Step 6.4: Push the branch**

```bash
git push -u origin chore/cloudflare-go-v7
```

Expected: branch pushed, tracking set, gh URL printed for opening a PR.

- [ ] **Step 6.5: Open the PR**

```bash
gh pr create --title "chore(deps): migrate cloudflare-go from v6 to v7" --body "$(cat <<'EOF'
## Summary

Migrates `github.com/cloudflare/cloudflare-go` from v6.10.0 to v7.3.0. Pure module-path rename across the 11 files under `internal/cloudflare/`; no call-site logic changes.

This supersedes the auto-closed [#140](https://github.com/jacaudi/cloudflare-operator/pull/140), which was a go.mod-only substitution that couldn't compile.

## Why mechanical

Per the [official v7.0.0 migration guide](https://github.com/cloudflare/cloudflare-go/blob/main/docs/migration-guides/v7.0.0-migration-guide.md), v7's breaking changes are confined to three packages — `ai_search`, `email_security`, and `workers` — none of which this repo imports. Every package in use (`option`, `dns`, `zones`, `zero_trust`, `rulesets`, `bot_management`, `shared`) is structurally identical between v6.10.0 and v7.3.0: same method signatures, struct fields, error types, pagination types, and constants. The SDK is generated by Stainless from Cloudflare's OpenAPI spec, so there are no hand-edited evolutions to reason about.

## Scope

- 7 production files under `internal/cloudflare/` — import-path bump only
- 4 test files — import-path bump only
- `go.mod` + `go.sum`
- `internal/cloudflare/mock/` is intentionally untouched (mocks satisfy this repo's own interfaces; never imported the SDK)

## Test plan

- [x] `go build ./...` clean
- [x] `go vet ./...` clean
- [x] `make test` green locally (unit + envtest)
- [ ] CI: `Run Tests / Test`, `Envtest Suite`, all `Lint Code` jobs green

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Expected: PR URL printed. Capture it.

- [ ] **Step 6.6: Confirm CI was queued for the new PR**

```bash
gh pr view --json statusCheckRollup -q '.statusCheckRollup[] | "\(.name // .context): \(.conclusion // .status // "queued")"'
```

Expected: at least one entry per workflow (PR Validation: `Run Tests / Test`, `Envtest Suite`, the `Lint Code / Lint (*)` jobs). Conclusions may be empty / `IN_PROGRESS` / `QUEUED` — that's fine. If the command returns nothing, the PR was created but GitHub hasn't yet enqueued the workflow; re-run a moment later.

Do not merge yet — wait for green and let the per-task and full-diff reviewers (Steps 5–6 of the execution workflow) approve.

---

## Self-review notes

- **Spec coverage:** Every "In scope" item from the design's Scope section has a task. Verification signals from the design's Verification section map to Task 5's steps. Risk mitigations (mock divergence check, transitive-dep churn review) are explicit steps in Tasks 4 and 5. PR shape matches design Section "Branch / PR shape."
- **Placeholder scan:** No "TBD", no "appropriate error handling," no "similar to task N" — every step has concrete commands and exact expected output. The PR template's checklist intentionally leaves CI box unchecked because CI runs after PR creation.
- **Type consistency:** No new types introduced; only literal-string rewrites in imports. The `cfgo` and other import aliases used downstream are preserved by design (the rewrite changes only the quoted path, not the alias before it).
- **Cross-task references:** Task 2 produces a deliberately broken intermediate state; Tasks 3 and 4 share that working tree and end on the GREEN/clean state that Task 5 verifies and Task 6 commits. Task ordering must be strictly sequential — no parallelization opportunity.
