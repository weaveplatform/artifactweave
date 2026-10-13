# Definition of done

These gates apply to every pull request and every phase in the
[implementation plan](plan.md). Tooling details are in
[testing/ci.md](../testing/ci.md) and decision
[0015](../research/decisions/0015-testing-and-acceptance.md).

## Every pull request

| Gate | Check |
|---|---|
| Build | `make build` cross-compiles `cmd/artifactweave` for linux, darwin and windows (amd64 and arm64) with CGO disabled |
| Lint | `golangci-lint` with the repository `.golangci.yml` reports zero issues, and `gofmt` is clean. Wire-model packages are the only `tagliatelle` exceptions ([0002](../research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md)) |
| Unit tests | `make test` passes with `-race -shuffle=on` on Linux, macOS and Windows; tests never depend on ordering. Suites that need Docker skip cleanly when it is unreachable |
| Coverage | The merged total stays at or above 95% and no package drops below 90%, enforced by `make cover` with `.testcoverage.yml`. Generated code (`*.gen.go`) and `cmd/` are excluded. New packages aim for 95% |
| Contracts | Changes to stores, blob backends, format handlers or the remote fetcher run the matching suite: `storetest`, `blobtest`, the format conformance fixtures or the `fakeupstream` fault matrix |
| Status codes | A handler's response codes come from the status-code column in [research 02](../research/02-artifactory-core-api-inventory.md) and [05](../research/05-platform-identity-and-security-apis.md), and the table-driven status test covers every new route |
| Spec drift | `make generate` produces no diff under `internal/server/gen/` or `pkg/client/` |
| Acceptance | The Linux `acceptance` job passes within its 15 minute budget. A feature that changes client-visible behaviour adds or updates a `.feature` file |
| Security | `gosec` (through golangci) and `govulncheck` are clean. Secrets go through `internal/crypt` sealed columns; nothing logs a credential |
| Docs | Behaviour changes update the matching decision record in place and the relevant research or operations page. A new decision gets a new numbered record |
| PR title | Conventional-commit title, checked by `pr-title-validation.yml`, so release-please can generate the changelog |

## Every phase

A phase is complete when:

1. All of its tasks in [plan.md](plan.md) are merged.
2. Its exit criteria are demonstrated against the real clients named in the plan (JFrog CLI, the JFrog Terraform providers, the native package clients). The PR description links the evidence: test names and CI runs.
3. Its acceptance features are in `test/acceptance/features` and pass in CI.
4. Its decision records are still accurate. Records move from **Proposed** to **Accepted** in P18.

## Coverage rules

- 95% is a floor. Test through the public surface of each package and use fakes at external boundaries: the blob store, the upstream fetcher, the clock, the SQL store.
- Streaming handlers reach the floor through `internal/testfault`: client disconnect mid-stream, blob write failure after headers were sent, upstream 5xx, slow and wrong content type. These paths are tested, not excluded.
- Code reachable only with Docker (PostgreSQL, MinIO, the container-backed clients) is covered by the Linux jobs. The macOS and Windows unit runs cover the same packages through fakes.
- Unreachable defensive branches are removed instead of being marked as excluded.

## Acceptance budget

The per-PR acceptance job runs SQLite and the local disk blob store by default and the
PostgreSQL and MinIO combination only for scenarios tagged `@storage`. Go-based clients
(`jf`, `terraform`, `helm`, `crane`, `oras`, `cosign`) are pinned binaries; only Maven,
.NET, Node and Python run in containers, with cached dependency volumes. The job must
finish in 15 minutes; a scenario that pushes it over moves to the nightly `-tags e2e` run.
