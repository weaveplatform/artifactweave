# CI

This page describes the workflows in `.github/workflows/` and the jobs the
[implementation plan](../implementation/plan.md) adds to them.

## Implemented checks (template)

| Workflow | Checks |
|---|---|
| `quality-gate.yml` | Vet and race/shuffled unit tests on `ubuntu-latest`, `macos-latest` and `windows-latest`; blocking golangci-lint followed by `make verify-generate` (bundled specs, generated servers, clients and 501 stubs must be committed current); govulncheck; cross-compile of every `cmd/*` binary; merged coverage gate (95% total, 90% per package) |
| `pr-title-validation.yml` | Conventional PR title |
| `dependency-review.yml` | GitHub Dependency Review |
| `deps-refresh.yml`, `auto-merge.yml`, `release-please.yml` | Dependency currency, auto-merge once the gate passes, releases |

`GOWORK=off` isolates builds from parent workspaces. Actions are pinned by commit SHA.

## Jobs added by this project

| Job | Runner | Added in | What |
|---|---|---|---|
| `acceptance` | `ubuntu-latest` | P0 | Builds `artifactweave` with `-cover`, runs godog features with pinned `jf`, `terraform`, `helm`, `crane`, `oras`, `cosign` binaries and container-backed Maven, .NET, Node and Python clients; uploads `cover-accept` |
| `parity` | `ubuntu-latest` | P0 | Restores or fetches the gitignored `.specoracle` cache of JFrog's published OpenAPI blocks and runs `TestSpecParity` against `api/parity-baseline.json`. Required since P1 (in the gate's `needs`): the baseline lists only out-of-scope products. docs.jfrog.com is contacted only on a cache miss |
| `conformance` | `ubuntu-latest` | P6 | Starts the `-cover` binary with a `docker-local` repo, clones `opencontainers/distribution-spec` at a pinned tag and runs its conformance module with `OCI_ROOT_URL`, `OCI_NAMESPACE=<repo>/<image>` and credentials; uploads `cover-conformance` |
| `terraform` | `ubuntu-latest` | P3 | Applies `test/terraform/` against the binary with a pre-populated provider mirror, then checks `terraform plan` is empty, then destroys |
| `acceptance-peers` | `ubuntu-latest` | P13 | Starts two or three `-cover` instances on separate ports and data directories, pairs them, and runs the `@peers` godog features: federated repository convergence, partition and full sync (P13), Access Federation and topology (P14), release bundle distribution to an edge (P15). Kept apart from `acceptance` so the per-PR budget holds; uploads `cover-accept-peers` and joins the coverage and gate `needs` |
| `e2e` (nightly) | `ubuntu-latest` | P9 | `-tags e2e` acceptance against real upstream registries |

`acceptance`, `acceptance-peers` and `conformance` are listed in the coverage job's `needs` and in the
`gate` job's `needs`, so the merged coverage includes them and a failure blocks merge.

## Why acceptance runs on Linux only

GitHub's macOS runners have no Docker daemon and the Windows runners only run Windows
containers. testcontainers-go, MinIO, PostgreSQL and the client containers therefore run
only on `ubuntu-latest`. Unit suites that need Docker call `t.Skip` when the daemon is
unreachable, so the macOS and Windows matrix legs stay green and cover the same packages
through fakes. See decision
[0015](../research/decisions/0015-testing-and-acceptance.md).

## Local equivalents

| Command | What |
|---|---|
| `make gate` | Everything the per-PR jobs run except acceptance |
| `make accept` | The acceptance job (needs Docker) |
| `make conformance` | The OCI conformance job (needs Docker and network for the spec clone) |
| `make generate` | Regenerate servers and clients; CI fails on drift |
