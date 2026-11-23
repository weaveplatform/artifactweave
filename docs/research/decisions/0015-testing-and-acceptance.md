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

**CI selection (#673).** Ordinary one-format Go changes use the reviewed
server-test ownership inventory and the current Go reverse import graph to
select unit and native acceptance work. Changed production packages must reach
90% same-run statement coverage. Deployment-only changes run Terraform
checks. Shared, uncertain and release candidate changes retain the complete
three-OS, native, security and 95% total / 90% per-package gate. The stable
required Quality gate rejects a selected job that is missing, failed or
cancelled and checks that unselected jobs were skipped. This supplements the
test policy below; decision status is unchanged.

**Four visible test lanes (#810).** The impact plan names selected and skipped
Go unit, Go acceptance, Terraform provider conformance and Terraform
deployment checks. Three-OS Go unit jobs use an anchored `^TestUnit` selection;
the reviewed fuzz seed corpora (`FUZZ_PKGS`) run separately in that lane. Linux
Go acceptance uses anchored `^TestAcc` across affected packages, including mixed
packages; narrow native client partitions execute exactly `TestAccFeatures`
with their godog tags. The full core native job also runs the other acceptance
harness `TestAcc` entries once.
The provider conformance lane applies the pinned JFrog providers, demands an
empty plan and destroys. The deployment lane checks Terraform formatting,
validation, lint, Trivy, mocked tests and docs. Deployment roots and the
READMEs under `deploy/` select the second. Provider fixtures, their harness and
shared Terraform tooling select both, because the deployment lane also
validates, lints and drift-checks the provider roots (#1858). Every
deployment run also takes the three-OS unit lane for `tools/citestselection`,
whose `TestUnit` guards read `test/terraform/run.sh`, the `check-*.py`
inventories and the provider roots, and blocking lint (#1861).
Full release/manual runs require all four kinds and same-run merged coverage;
the stable `Quality gate` rejects missing, failed or canceled selected lanes.
Acceptance-only format owners explicitly skip the unit matrix while retaining
their selected acceptance and same-run coverage checks.

**Unit tests** use the standard library and `go-cmp`, no assertion library, table-driven
with hand-written fakes behind narrow interfaces, `testing/synctest` for timers, always
`-race -shuffle=on -count=1`. They run on all three operating systems.

**Test kinds.** Every runnable top-level Go test declares its intent with
`TestUnit...` for isolated in-process checks or `TestAcc...` for API, protocol,
integration and real-client evidence. The kind belongs to the test, not its
file or package; mixed-kind packages are valid. An AST guard inspects every
application `_test.go` source, including build-tagged and platform-specific
files, before the Go test and native acceptance targets run. It records the
three required `TestMain` hooks and the three `Fuzz...` seed-corpus targets
with explicit CI owners. Their Go-required names remain unchanged. The
impact plan selects by prefix within reviewed packages and retains exact test
names as a run artifact. The `TestMain` hooks remain Go hooks and the `Fuzz`
targets retain their distinct reviewed owner.

**Contract suites** (`internal/app/storetest`, `internal/platform/blob/blobtest`) run
against the in-memory store, SQLite, PostgreSQL and MinIO. Container-backed cases skip
with a clear message when Docker is unreachable, so the macOS and Windows jobs stay green
while only Linux exercises PostgreSQL and MinIO.

**API tests** start `httptest.NewServer`, drive it through the generated clients and
include the route × principal matrix.

**Fault injection.** Every handler package depends on narrow interfaces and the shared
`internal/platform/testkit/fault` fakes (client disconnect mid-stream, blob write failure
after headers, upstream 5xx, slow upstream, wrong content type), so error paths in
streaming handlers are covered without real failures. The remote-repository design adds
`internal/artifacts/adapters/upstream/fakeupstream` chi fakes of each upstream registry with the
same fault modes; that package is not part of the current local-protocol implementation.

**Acceptance** runs on `ubuntu-latest` only. The required non-Gradle scenarios
are split between core and NuGet jobs, both added to the coverage and gate
`needs`. godog features in `test/acceptance/features` drive the `-cover` built binary.
Go-based clients (`jf`, `terraform`, `helm`, `crane`, `oras`, `cosign`) are pinned tool
binaries, not containers; `maven`, `dotnet`, `node` and `python` run in containers with
cached dependency volumes. The `go` command is the runner's own toolchain, the one
`actions/setup-go` installs from `go.mod`, run with `GOTOOLCHAIN=local` and
`GOFLAGS=-modcacherw`, so it needs no pin of its own. The client images are pinned by digest
and kept current by Dependabot. They run with `--network host` and the runner's user, so a
container reaches the binary on `127.0.0.1`, the base URL inside metadata stays valid, and
cleanup can delete what they write. The npm, Go, Maven, PyPI, NuGet, Helm, Terraform
registry and Terraform backend scenarios gate their jobs; Gradle runs in an
informational job and does not gate merge ([0032](0032-package-format-handlers.md)).
Maven build-tool preparation belongs to those same jobs: the committed SHA-512
manifest covers plugin/transitive POMs and JARs plus the pinned JF extractor.
Every restored object is verified, incomplete supplies are repaired before
assertions, and scenarios receive private tool-only repositories. All Maven
assertions block external repositories while retaining owned loopback HTTP;
product fixture coordinates remain cold. Removing the intended repository must
fail another fresh consumer. Preparation failure fails setup truthfully (#1852).
The default storage is SQLite with the fs blob store;
PostgreSQL and MinIO run a `@storage`-tagged subset. Each required partition
uploads its own coverage, merged into the gate. Budget: 15 minutes wall clock
per required acceptance job.

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
- **Categories.** Pull, Push, Content Discovery and Content Management are all on. The job
  fails on any failed specification, never on a skipped one.

**NTLM proxy native proof (#1835).** The core lane's `@generic_remote` ownership
includes `@ntlm_proxy`; its JFrog CLI step fails when the required binary is absent.
A controlled proxy independently verifies NTLMv2 proof, domain/workstation and the
same socket, while separate origin records and downloaded files prove cold bytes.
Transport suites additionally cover failure, rotation, SQLite and PostgreSQL.
Replication's consumer (#1848) has its own proof: `@replication_ntlm_proxy` pushes
between two owned processes on SQLite and PostgreSQL through the same verifier.

**Terraform scenarios.** `test/terraform/run.sh` applies the app-layer references in
`deploy/artifactweave/terraform` (decision 0030), covering the implemented P0–P8 resources
of the JFrog providers, against the binary, then plans (no changes allowed), then
destroys. Providers come from a filesystem mirror of the pinned release zips, built at run
time and checked against their SHA-256s, so the job does not depend on
registry.terraform.io.

The provider driver owns its fixture server and storage. It passes a private
SQLite/filesystem configuration, isolates inherited application settings in that
child, and accepts readiness only after the live child's fresh structured listener
event names its private ephemeral backend. An owned TCP forwarder proves each
connected peer socket belongs to that child before forwarding any client bytes,
using Linux `/proc` or macOS `lsof`. A connection stays pinned to that peer without
reconnecting, so a replacement listener cannot receive credentials while the child
drains. Missing socket evidence fails closed. Failed startup, an occupied port,
or a failed/quarantined forwarder aborts the run. The frontend remains bound in
quarantine until active client commands finish and parent cleanup reaps it, so
provider retries cannot reach a replacement frontend. Missing listener evidence
or readiness timeout aborts before authentication or
provider effects. A retained workspace contains a fresh subdirectory per run so
state and startup evidence cannot carry over. Owned loopback spies and child
Go/chi fixtures exercise these failures in the existing deployment checks (#1845).

Each implementation phase must extend these scenarios for its provider resources and
prove their actual behavior. P41 qualifies the expanded resource set; the current
fixtures do not establish coverage of every capability in the
[capability roadmap](../../implementation/capability-roadmap.md).

**Planned remote verification:** nightly `-tags e2e` runs will resolve each format through
real upstream registries when the remote fetcher is implemented (decision 0007).

**Coverage** is written with `-cover -coverpkg=./... -args -test.gocoverdir`, merged with
`go tool covdata`, enforced by `go-test-coverage` from `.testcoverage.yml`. Generated code
and `cmd/` are excluded. 95% is a floor; unreachable defensive branches are removed, not
excluded.

**CI application during #673.** The first selective gate permits only a
documentation profile for changes confined to `README.md`, Markdown under
`docs/` and retained JSON evidence under `docs/testing/evidence/`. It runs
documentation checks without building package clients and makes no code
coverage claim. All other PRs, the same-repository release-please candidate
and manual qualification retain the complete jobs and merged coverage above.
The required `Quality gate` checks selected successes and intentional skips
explicitly. A later #673 change must map package dependencies and native
scenarios before narrowing code PRs; this addendum does not waive their
acceptance or coverage obligations.

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

- The 15-minute budget is enforced by splitting features into disjoint,
  exhaustive required tag partitions when it is exceeded; all partitions and
  their coverage remain in the gate.
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
