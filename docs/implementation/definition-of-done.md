# Definition of done

The full profile below applies to release-please pull requests, manual phase
gates, shared code and any change whose impact cannot be bounded. A PR changing
only `README.md`, Markdown documentation or retained JSON evidence under
`docs/testing/evidence/` uses the documentation profile. A mapped one-format
Go change runs affected `TestUnit` entry points on three operating systems when
present, selected Linux package `TestAcc` entry points, blocking lint, matching native
acceptance and a 90% same-run statement coverage floor for every changed
production package. Provider-fixture edits run pinned JFrog Terraform provider
apply/empty-plan/destroy conformance; deployment-root edits run Terraform
static/mocked checks; shared Terraform tooling selects both. The stable required `Quality
gate` verifies selected success and deliberate skips; it rejects missing or
failed selected jobs. Format and deployment runs do not claim whole-project
coverage. Every phase in the [implementation plan](plan.md) receives full
qualification at its exit gate. Tooling details are in
[testing/ci.md](../testing/ci.md) and decision
[0015](../research/decisions/0015-testing-and-acceptance.md).

## Every full-profile pull request

| Gate | Check |
|---|---|
| Build | `make build` cross-compiles `cmd/artifactweave` for linux, darwin and windows (amd64 and arm64) with CGO disabled |
| Lint | `golangci-lint` with the repository `.golangci.yml` reports zero issues, and `make fmt-check` passes all configured formatters. Wire-model packages are the only `tagliatelle` exceptions ([0002](../research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md)) |
| Go unit | `make test` selects `^TestUnit` with `-race -shuffle=on` on Linux, macOS and Windows; the three owned fuzz seed corpora run separately in the same lane |
| Go acceptance | `make accept-go` selects `^TestAcc` across all selected non-native packages, including mixed packages; Linux native-client partitions run `TestAccFeatures` with scoped godog tags |
| Terraform provider conformance | The pinned JFrog provider fixtures apply against artifactweave, produce an empty follow-up plan, then destroy |
| Terraform deployment | `make deploy-check` verifies fmt, validate, tflint, trivy, mocked `terraform test` and documentation drift for deployment roots |
| Coverage | The merged total stays at or above 95% and no package drops below 90%, enforced by `make cover` with `.testcoverage.yml`. Generated server/client packages, `cmd/`, `tools/` and the named test harnesses are excluded as listed there. New packages aim for 95% |
| Contracts | Changes to stores, blob backends or format handlers run the matching suite: `storetest`, `blobtest` or `formattest` with the format's fixture. P9 adds the remote fetcher and its `fakeupstream` fault matrix |
| Status codes | A handler's response codes come from [research 02](../research/02-artifactory-core-api-inventory.md), [05](../research/05-platform-identity-and-security-apis.md), or the protocol's research and decision record. The table-driven status tests (`internal/server/status*_test.go`) cover every new route; `TestAccStatusTableCoversImplemented` fails while an implemented route has no row |
| Spec drift | `make verify-generate` produces no diff in bundled `api/*.openapi.yaml`, `internal/server/gen/` or `pkg/client/` |
| Acceptance | The Linux `acceptance` job passes within its 15 minute budget. A feature that changes client-visible behaviour adds or updates a `.feature` file |
| Security | `gosec` (through golangci) and `govulncheck` are clean. Stored secrets use `internal/crypt` sealed columns; encrypted Terraform state uses sealed blobs. Nothing logs a credential |
| Docs | Behaviour changes update the matching decision record in place and the relevant research or operations page. A new decision gets a new numbered record |
| Capability ownership | Every new or deferred capability is mapped in the [capability roadmap](capability-roadmap.md), with an owning phase and acceptance criterion. Only evidenced supersession permits exclusion; P20 adds automated inventory enforcement |
| PR title | Conventional-commit title, checked by `pr-title-validation.yml`, so release-please can generate the changelog |

## Every phase

A phase is complete when:

1. All of its tasks in [plan.md](plan.md) are merged.
2. Its exit criteria are demonstrated against the real clients named in the plan (JFrog CLI, the JFrog Terraform providers, the native package clients). The PR description links the evidence: test names and CI runs.
3. Its acceptance features are in `test/acceptance/features` and pass in CI.
4. Its decision records are still accurate. Existing **Accepted** decisions retain their
   status; P18 reviews the remaining **Proposed** records for the initial sequence;
   P20–P41 decisions are reviewed with their phases and expanded qualification.
5. Its capability inventory agrees with observed runtime and client behavior. Any work
   moved elsewhere has an explicit receiving phase and remains open there. A schema,
   generated stub, repository CRUD proof or client skip does not complete a capability.

## Coverage rules

- 95% is a floor. Test through the public surface of each package and use fakes at external boundaries: the blob store, the upstream fetcher, the clock, the SQL store.
- Streaming handlers reach the floor through `internal/testfault`: client disconnect mid-stream, blob write failure after headers were sent, upstream 5xx, slow and wrong content type. These paths are tested, not excluded.
- Code reachable only with Docker (PostgreSQL, MinIO, the container-backed clients) is covered by the Linux jobs. The macOS and Windows unit runs cover the same packages through fakes.
- Unreachable defensive branches are removed instead of being marked as excluded.

## Acceptance budget

The per-PR acceptance job runs SQLite and the local disk blob store by default and the
PostgreSQL and MinIO combination only for scenarios tagged `@storage`. Go-based clients
(`jf`, `terraform`, `helm`, `crane`, `oras`, `cosign`) are pinned binaries; only Maven,
.NET, Node and Python run in containers, with isolated client configuration and a shared
Maven dependency cache. The job must finish in 15 minutes. The plan moves expensive
scenarios to a nightly `-tags e2e` run from P9; that workflow must be added before a
scenario can be moved out of the per-PR gate.
