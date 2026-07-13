# Contributing

Thanks for your interest in the Cloudflare Operator. This document covers the
conventions the build, test, and release tooling depend on so your change lands
cleanly.

The operator is a Go [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime)
project built with `make`. If you have contributed to a kubebuilder-style
operator before, most of this will be familiar.

## Prerequisites

- **Go 1.26** (see the `go` directive in [`go.mod`](go.mod)).
- **make** and a POSIX shell — the [`Makefile`](Makefile) is the entry point for
  every workflow below.
- Standard build tools (`git`, a C-free build — the manager builds with
  `CGO_ENABLED=0`).

Project-specific tooling (`controller-gen`, `setup-envtest`, `helm-docs`,
`crd-ref-docs`) is installed on demand into `./bin/` by `make tools`; the
`generate`, `manifests`, and `test` targets depend on it, so you rarely need to
run `make tools` yourself. `golangci-lint` is wired as a Go tool dependency and
invoked via `go tool golangci-lint` by `make lint`.

## Development workflow

```bash
# 1. Regenerate everything derived from the api/v2alpha1 Go types:
#    CRD bundles, deepcopy code, the chart's CRD templates, chart/README.md,
#    and docs/crd-reference.md. See "Generated artifacts" below.
make generate

# 2. Run the full test suite (unit + envtest, with -race and coverage).
make test

# 3. Lint (golangci-lint + a gofmt cleanliness gate).
make lint

# 4. Build the manager binary to bin/manager.
make build
```

`make all` runs `generate test build` in sequence.

To run the operator locally against your current kube context (after applying
the CRDs from `bin/crd-staging/`), see the **Local development** section of the
[README](README.md#local-development).

## Generated artifacts

CRDs, RBAC, deepcopy code, the chart's CRD templates, `chart/README.md`, and
`docs/crd-reference.md` are all **generated** from the kubebuilder markers and
Go types under `api/v2alpha1/`. **Never hand-edit the generated outputs.**

After changing an API type or a marker, regenerate and commit the results:

```bash
make generate
```

`make generate` (via `make manifests`) drives `controller-gen` for the CRDs and
deepcopy code, copies the five bundle CRDs into `chart/templates/` (gated on
`.Values.crds.install` / `.Values.crds.keep`), then regenerates `chart/README.md`
with `helm-docs` and `docs/crd-reference.md` with `crd-ref-docs`. Reviewers
expect the generated files in your PR to match what `make generate` produces
from your source changes, so run it and commit the diff rather than editing the
outputs by hand.

## Testing

- **Unit tests** live next to the code they cover (e.g.
  `internal/controller/tunnel/*_test.go`) and use the controller-runtime
  [fake client](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/client/fake)
  (with `interceptor.Funcs` for error-injection) — no apiserver required.
- **Integration tests** live under `test/envtest/` and run against a real
  apiserver via controller-runtime's
  [envtest](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/envtest).
  `make test` provisions the kube control-plane binaries through
  `setup-envtest` and exports `KUBEBUILDER_ASSETS` automatically:

  ```bash
  make test   # KUBEBUILDER_ASSETS="$(setup-envtest use 1.32.0 -p path)" go test ./... -race -coverprofile=cover.out
  ```

  The envtest suite skips gracefully when `KUBEBUILDER_ASSETS` is unset, so a
  bare `go test ./...` still runs the unit tests. Prefer `make test` locally so
  the integration suite actually exercises your change.

New behavior should come with tests. Follow the existing table-driven style, and
add an envtest case under `test/envtest/` when a change affects reconcile
behavior end-to-end.

## Commit convention

Commits follow [Conventional Commits](https://www.conventionalcommits.org/). The
release pipeline ([`.releaserc.json`](.releaserc.json), semantic-release on
pushes to `main`) derives the next version and the `CHANGELOG.md` entry from
commit types:

- `feat:` — a new feature (**minor** release, grouped under "Features")
- `fix:` — a bug fix (**patch** release, "Bug Fixes")
- `refactor:` — internal change, no behavior change (**patch** release, "Refactors")
- `chore(cloudflared):` — the in-tree cloudflared bump (**patch** release, "Dependencies")
- `chore(deps):` — dependency bumps; grouped under "Dependencies" in the release
  notes, but does not by itself trigger a release (it rides along with the next
  `feat`/`fix`/`refactor`/`chore(cloudflared)`)

Any commit carrying a `!` / `BREAKING CHANGE` produces a **minor** release — see
Versioning below. Commit types not listed above (e.g. `docs:`, `test:`, `ci:`)
do not trigger a release. Use a scope where it clarifies the area (e.g.
`feat(tunnel):`, `fix(zone):`).

## Versioning (alpha)

This project is pre-1.0. Breaking changes warrant only a **minor** version bump:
`.releaserc.json` maps `breaking → minor`, so even a `!` / `BREAKING CHANGE`
commit produces a minor release, not a major one. Do not treat API or behavior
changes as major while the project is pre-1.0.

## Opening a pull request

Before you open a PR, make sure locally:

```bash
make generate   # generated artifacts committed and in sync
make lint       # golangci-lint clean, gofmt clean
make test       # unit + envtest green
```

PRs are validated by the **PR Validation** workflow
([`.github/workflows/pr.yml`](.github/workflows/pr.yml)), which runs three jobs:

- **Lint** — YAML, Go (`golangci-lint`), and Helm chart linting.
- **Tests** — `go test` over `./api/... ./internal/... ./cmd/...` with a
  coverage threshold.
- **Envtest Suite** — `make test` (unit + envtest against a real apiserver).

Container images and Helm charts are built only by the post-merge pipeline
(`ci-cd.yml`); PRs do not publish artifacts. A test image can be built on demand
from a feature branch via the manually-triggered **Test Build** workflow.

## License headers

New Go source files carry the MIT header from
[`hack/boilerplate.go.txt`](hack/boilerplate.go.txt):

```go
/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
*/
```

`controller-gen` stamps this header onto the deepcopy files it generates; add it
to any hand-written `.go` file you create.

## License

By contributing, you agree that your contributions are licensed under the
project's [MIT License](LICENSE).
