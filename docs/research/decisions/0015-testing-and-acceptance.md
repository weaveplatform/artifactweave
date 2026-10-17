# 0015: Testing and acceptance

Checked against: Artifactory 7.161.32

Status: Proposed

## Context

The template's quality gate requires at least 95% merged coverage and 90% per package,
unit tests on Linux, macOS and Windows, and acceptance tests against real dependencies in
Docker. The thesis of the project is compatibility, which only the real clients can prove:
the JFrog CLI, the three Terraform providers, `docker`, `npm`, `go`, `mvn`, `pip`,
`twine`, `dotnet`, `helm` and `terraform`. GitHub's macOS and Windows runners have no
Docker daemon. The OCI distribution specification ships a conformance suite as a separate
Ginkgo module.

## Decision

**Unit tests** use the standard library and `go-cmp`, no assertion library, table-driven
with hand-written fakes behind narrow interfaces, `testing/synctest` for timers, always
`-race -shuffle=on -count=1`. They run on all three operating systems.

**Contract suites** (`pkg/store/storetest`, `pkg/blob/blobtest`) run against the
in-memory store, SQLite, PostgreSQL and MinIO. Container-backed cases skip with a clear
message when Docker is unreachable, so the macOS and Windows jobs stay green while only
Linux exercises PostgreSQL and MinIO.

**API tests** start `httptest.NewServer`, drive it through the generated clients and
include the route × principal matrix.

**Fault injection.** Every handler package depends on narrow interfaces and the shared
`internal/testfault` fakes (client disconnect mid-stream, blob write failure after headers,
upstream 5xx, slow upstream, wrong content type), so error paths in streaming handlers are
covered without real failures. `internal/fakeupstream` serves chi fakes of each upstream
registry with the same fault modes.

**Acceptance** runs on `ubuntu-latest` only, as one job added to the coverage and gate
`needs`. godog features in `test/acceptance/features` drive the `-cover` built binary.
Go-based clients (`jf`, `terraform`, `helm`, `crane`, `oras`, `cosign`) are pinned tool
binaries, not containers; `maven`, `dotnet`, `node` and `python` run in containers with
cached dependency volumes. The `go` command is the runner's own toolchain, the one
`actions/setup-go` installs from `go.mod`, run with `GOTOOLCHAIN=local` and
`GOFLAGS=-modcacherw`, so it needs no pin of its own. The client images are pinned by digest
and kept current by Dependabot. They run with `--network host` and the runner's user, so a
container reaches the binary on `127.0.0.1`, the base URL inside metadata stays valid, and
cleanup can delete what they write. For M2, `npm`, `go` and `mvn` gate the job; Gradle runs
too but does not gate it ([0032](0032-package-format-handlers.md)). The default storage is SQLite with the fs blob store;
PostgreSQL and MinIO run a `@storage`-tagged subset. Coverage goes to `cover/accept` and
is merged into the gate. Budget: 15 minutes wall clock per pull request.

**OCI conformance** is its own CI job, `OCI conformance`, on `ubuntu-latest`, required by
the gate, with its `GOCOVERDIR` merged into coverage ([research 23](../23-docker-oci-registry.md)
section 9):
- **Suite.** `opencontainers/distribution-spec` `conformance/` at tag `v1.1.1`, pinned by
  commit and built with Go. Its JUnit and HTML reports are uploaded.
- **Registry.** The `-cover` binary on SQLite and the fs blob store, with two local `docker`
  repositories and a user with Read, Deploy and Delete on both, created through the REST API
  before the run.
- **Settings.** `OCI_ROOT_URL` is the binary's URL; `OCI_NAMESPACE=<repo>/conformance`;
  `OCI_CROSSMOUNT_NAMESPACE=<second repo>/conformance`, so mounts cross repositories;
  `OCI_USERNAME` and `OCI_PASSWORD`, so the token flow runs; `OCI_AUTOMATIC_CROSSMOUNT=0`;
  `OCI_DELETE_MANIFEST_BEFORE_BLOBS=1`, as blob deletes answer 405
  ([0031](0031-docker-oci-registry.md)); `OCI_HIDE_SKIPPED_WORKFLOWS=0`.
- **Categories.** Pull, Push, Content Discovery and Content Management are all on once the
  registry is complete; until then the categories whose operations exist are on. The job
  fails on any failed specification, never on a skipped one.

**Terraform scenarios.** `test/terraform/run.sh` applies the app-layer references in
`deploy/artifactweave/terraform` (decision 0030), which together cover the in-scope
resources of the JFrog providers, against the binary, then plans (no changes allowed), then
destroys. Providers come from a filesystem mirror of the pinned release zips, built at run
time and checked against their SHA-256s, so the job does not depend on
registry.terraform.io.

**Nightly** `-tags e2e` runs resolve each format through real upstream registries.

**Coverage** is written with `-cover -coverpkg=./... -args -test.gocoverdir`, merged with
`go tool covdata`, enforced by `go-test-coverage` from `.testcoverage.yml`. Generated code
and `cmd/` are excluded. 95% is a floor; unreachable defensive branches are removed, not
excluded.

## Rationale

Only the real clients prove the contract, so the acceptance suite is the product's test.
Running it where Docker exists, with the Go clients as binaries, keeps it fast enough to
gate every pull request; fault-injection fakes are what make a 90% floor honest for code
whose interesting paths are failures.

**Alternatives considered:**
- *Acceptance on every OS*: not possible without Docker; the clients behave the same.
- *Containers for every client*: slower, and host networking from containers to the
  binary complicates every scenario.
- *Mocking the clients*: proves nothing about compatibility.

## Constraints

- The 15-minute budget is enforced by splitting features into tags when it is exceeded.
- The OCI conformance module uses Ginkgo and lives outside this module; it is never
  imported.
- Nightly e2e needs network and real credentials and is not a merge gate.

## Verification

- The quality-gate workflow shows the unit matrix, the Linux acceptance job and the
  conformance job as required checks.
- `make cover` passes the thresholds with acceptance coverage merged.
- A feature that deliberately injects each fault mode exists so the fakes are exercised.

## References

- [09-jfrog-cli-compatibility](../09-jfrog-cli-compatibility.md)
- [08-terraform-providers](../08-terraform-providers.md)
- `weaveplatform/hostweave@main`, `docs/research/decisions/0018-testing-and-coverage.md`
- <https://github.com/opencontainers/distribution-spec/tree/v1.1.1/conformance>
- [23-docker-oci-registry](../23-docker-oci-registry.md), section 9
- <https://github.com/cucumber/godog>
- <https://github.com/testcontainers/testcontainers-go>
