# CI

This page describes the workflows in `.github/workflows/` through P8 and distinguishes
the jobs later phases of the [implementation plan](../implementation/plan.md) will add.

## Implemented workflows

| Workflow | Checks |
|---|---|
| `quality-gate.yml` | Vet and race/shuffled unit tests on `ubuntu-latest`, `macos-latest` and `windows-latest`; blocking golangci-lint, then `make fmt-check` (`golangci-lint fmt --diff`, because `golangci-lint run` does not report golines), then `make verify-generate` (bundled specs, generated servers, clients and 501 stubs must be committed current); govulncheck; cross-compile of every `cmd/*` binary; merged coverage gate (95% total, 90% per package) |
| `pr-title-validation.yml` | Conventional PR title |
| `dependency-review.yml` | GitHub Dependency Review |
| `deps-refresh.yml`, `auto-merge.yml`, `release-please.yml` | Dependency currency, auto-merge once the gate passes, releases |
| `release.yml` (on a `v*` tag, and on pull requests that change it, `.goreleaser.yaml` or `Dockerfile.release`) | Builds, checksums, SBOMs and signs the release binaries and appends them to the release, and publishes the signed multi-arch image to GHCR; see [Releases](#releases) |
| `cloud-conformance.yml` (manual) | The blob contract and probes against real GCS, Azure and S3; see [Real-cloud conformance](#real-cloud-conformance-manual) |
| `platform-auth-check.yml` (manual, and on pull requests that change a bootstrap) | Proves GitHub's trust in the deployer identities; see [Cloud trust and environments](#cloud-trust-and-environments) |
| `platform-scenarios.yml` (manual, and on pull requests that change it, its driver, `test/platform`, a scenario or a module; the `prove-all` label proves everything) | Applies, tests, destroys and reaps reference deployments on real clouds as the deployer; see [Platform scenarios](#platform-scenarios-manual) |

`GOWORK=off` isolates builds from parent workspaces. Actions are pinned by commit SHA.

## Jobs added by this project

| Job | Runner | Added in | What |
|---|---|---|---|
| `acceptance` | `ubuntu-latest` | P0 | Builds `artifactweave` with `-cover`, runs godog features with pinned `jf`, `terraform`, `helm`, `crane`, `oras`, `cosign` binaries, the runner's `docker`, `go`, `npm` and `gradle`, and the npm, Maven, Python and .NET clients in containers pinned by digest (from P7 and P8; see [Package clients](#package-clients-in-acceptance)). The `@gradle` scenarios run in a step of their own that does not gate the job. Uploads `cover-accept` |
| `parity` | `ubuntu-latest` | P0 | Restores or fetches the gitignored `.specoracle` cache of JFrog's published OpenAPI blocks and runs `TestSpecParity` against `api/parity-baseline.json`. Required since P1 (in the gate's `needs`): the baseline records unimplemented product contracts and explicitly assigned gaps, including the project-scoped remote fields and admin endpoints in [#487](https://github.com/weaveplatform/artifactweave/issues/487). Passing parity means no unrecorded drift, not that every operation is implemented. docs.jfrog.com is contacted only on a cache miss. The cache key carries the ISO week as well as the hash of `tools/specoracle/main.go`, so the first run of each week crawls afresh and a page JFrog changed reaches the check within a week ([#419](https://github.com/weaveplatform/artifactweave/issues/419)); a new family in the index list is crawled at once. A local `.specoracle` is never refreshed by itself: run `make oracle-fetch` again after the index list changes |
| `oci-conformance` (`OCI conformance`) | `ubuntu-latest` | P6 | Builds `opencontainers/distribution-spec`'s conformance suite at the commit of tag `v1.1.1`, starts one `-cover` binary on SQLite and the fs blob store with two local `docker` repositories and a user with Read, Deploy and Delete on both, and runs the categories switched on in the Makefile; see [OCI conformance](#oci-conformance). Uploads the JUnit and HTML reports as `oci-conformance-report` and the coverage as `cover-oci` |
| `terraform` | `ubuntu-latest` | P3 | Installs the pinned `jfrog/artifactory` and `jfrog/platform` providers from a filesystem mirror of their release zips (checked against the release SHA-256s and the committed lock files) and validates each scenario in `deploy/artifactweave/terraform`: repositories, identity, oidc, cdn-repositories and remote-repositories. Then it starts one `-cover` binary, mints an admin access token, and for each scenario applies, requires an empty `terraform plan` and destroys (`make terraform TERRAFORM_MODE=apply`, from P4, #69, #85 and #83); uploads `cover-terraform` |
| `deploy-terraform` | `ubuntu-latest` | P4b | `make deploy-check`: the `prove-matrix` table test and the reference-deployment contract lint, then over every Terraform root in `deploy/` and `test/terraform`: `terraform fmt -check`, `terraform validate`, tflint with the aws, google and azurerm rulesets, `trivy config`, `terraform test` against mocked providers, and the terraform-docs drift check (#124, decision 0030). Needs no cloud credentials |

`acceptance`, `oci-conformance` and `terraform` are listed in the coverage job's `needs`
and in the `gate` job's `needs`, so the merged coverage includes them and a failure blocks merge.
`deploy-terraform` is in the `gate` job's `needs` only: it runs no Go code, so it adds no
coverage.

### Planned jobs

These jobs are not in the current workflows and contribute no coverage yet. P20 also adds
an operation/client/format inventory check to reject unmapped capability gaps; the current
schema parity test does not provide that guarantee. The [capability roadmap](../implementation/capability-roadmap.md)
assigns the missing behaviors to phases through P41.

| Planned job | Phase | Intended checks |
|---|---|---|
| `e2e` (nightly) | P9 | `-tags e2e` acceptance against real upstream registries |
| `acceptance-peers` | P13–P15 | Two or three `-cover` instances on separate ports and data directories for federated repository convergence, partitions and full sync (P13), Access Federation and topology (P14), and release bundle distribution to an edge (P15). Separate from `acceptance` to preserve the per-PR budget; its `cover-accept-peers` artifact and coverage/gate dependencies are added with the job |

## Unit coverage

`make test` runs every unit package in one `go test` with `-coverpkg=<module>/...`.
Through `-exec`, `tools/coverexec` gives each test binary a coverage directory of its own
under `cover/unit/parts`. When the binaries are done, `go tool covdata merge` combines the
parts into `cover/unit`, the flat directory that each `Unit tests` job uploads as
`cover-unit-<os>` and that `make cover` reads.

Each unit-test binary has an explicit 15-minute timeout. The growing server route
and permission matrices exhausted Go's default 10-minute package deadline on
Windows with race detection and full-module coverage enabled, while individual
tests continued making progress
([Windows failure](https://github.com/weaveplatform/artifactweave/actions/runs/39565019432/job/119105112653)).
The timeout applies to each package independently. Acceptance retains its
separate budget below.

**Why not one shared directory:** binaries that link the same instrumented packages write
the same `covmeta.<hash>` file. Each writes a temporary file and renames it onto that name.
Linux and macOS let two binaries finishing together both rename, but Windows refuses the
second rename with "Access is denied", and that test binary fails
([#368](https://github.com/weaveplatform/artifactweave/issues/368)).

## Acceptance budget

The required Linux acceptance job has an explicit **15-minute (900-second)**
deadline, including setup, client image preparation, the native scenarios,
informational Gradle and coverage upload. `make accept` and the separate Gradle
invocation use `go test -timeout=15m`; the job deadline still bounds their combined
elapsed time. Local `make accept` has the same test watchdog.

Previously the job budget was documented but not configured, and `make accept`
inherited Go's 10-minute test watchdog. [PR #643's acceptance run](https://github.com/weaveplatform/artifactweave/actions/runs/39626830399/job/119314120059)
reached that watchdog while the suite was progressing, despite a successful run
on the same commit. Making both limits explicit removes that mismatch without
changing scenarios, required jobs, coverage artifacts or thresholds. Historical
measurements below retain the limits that applied when they were recorded.

New adapters must fit within the overall job budget. If they cannot, a reviewed
partition must retain every required scenario, merge all coverage artifacts and
include each partition in the required quality gate.

## Controlled upstream tests

P9's [`internal/fakeupstream`](../../internal/fakeupstream/README.md) supplies chi
HTTP/HTTPS fixtures for transport, cache and adapter tests. Its self-checks run in
the existing unit jobs with race detection and shuffle; the package remains in the
normal merged coverage gate, with no new exclusion. They cover mutable artifacts
and metadata, validators/ranges, auth, cookies, TLS/mTLS, restricted forward/CONNECT
proxies, cancellation and network/content faults using barriers and an injected
clock. They make no requests to public registries.

The local Go 1.27.3 darwin/arm64 race/shuffle run took 1.4 seconds for three
repetitions and reported 95.7% statement coverage. This is local unit evidence, not a
Linux acceptance timing estimate. The harness adds no acceptance scenario or timeout
increase. Native remote adapter tickets must configure clients only with the
application endpoint, use fresh client caches, and assert upstream counts and byte
hashes so cache hits or direct internet fallback cannot conceal a missing adapter.
Their additional elapsed time must be measured within the 900-second Linux job
budget described above. If partitioning becomes necessary, add the new
job's coverage artifact to aggregation and its result to the required gate; preserve
the coverage thresholds. The real-registry nightly work remains #638.

## Why acceptance runs on Linux only

GitHub's macOS runners have no Docker daemon and the Windows runners only run Windows
containers. testcontainers-go, MinIO, PostgreSQL and the client containers therefore run
only on `ubuntu-latest`. Unit suites that need Docker call `t.Skip` when the daemon is
unreachable, so the macOS and Windows matrix legs stay green and cover the same packages
through fakes. See decision
[0015](../research/decisions/0015-testing-and-acceptance.md).

Every test that starts a container asks `pkg/containertest` first: `RequireLinux` in a unit
test, `CheckLinux` in an acceptance step. The check gives the engine 5 seconds to answer,
and a test skips with "the Docker engine did not answer within 5s" when it does not. Docker
Desktop on macOS can accept connections on its socket and never answer, and
testcontainers' own health check then waits with no deadline, so before the bound every
Docker-backed test blocked until the package's `-timeout` killed the test binary and hid
the result of every other test in it
([#458](https://github.com/weaveplatform/artifactweave/issues/458)). The first answer
holds for the rest of the test binary, so later tests do not wait again. On the Linux
runners the engine answers at once and the tests run.

The `@docker` and client scenarios drive the `docker` command itself, so their step "the
Docker daemon can reach the registry" runs `docker info` instead, under the same 5-second
deadline. A `docker info` that does not answer in time is killed, and those scenarios skip
with "docker info did not answer within 5s" in the log rather than hanging until the job's
timeout ([#497](https://github.com/weaveplatform/artifactweave/issues/497)). As with
`CheckLinux`, the first answer holds for the whole acceptance run. `TestDockerInfo` checks the
probe against stand-ins that hang, fail and answer.

The cross-cloud blob mirror scenario (`TestCrossCloud` in `pkg/blob/mirror`) is one of
those unit suites. It runs MinIO, fake-gcs-server and Azurite side by side in the Linux
unit job and skips on macOS and Windows; the emulator images are pinned in
`pkg/containertest`.

## Package clients in acceptance

P7's clients (#270) run in the `acceptance` job, and P7's exit checks (#284, #294, #295)
run each with a second version beside the first. P8's clients (#534) join them.

- **npm, Maven, Python and .NET** run from images pinned by digest in
  `test/acceptance/clients` and moved by Dependabot's `docker` entries. Majors are held,
  because the Node LTS line, Maven 3, Python 3 and the .NET SDK's lines are the clients
  decision 0015 and #534 name. Nothing else is held: the Maven images take the JDK
  their new tag names, and a fault in a JFrog client on a new release is worked around in
  its scenario rather than by holding the update (#523). The job builds the seven images in the background
  while it sets up and compiles the `-cover` binary, and the scenarios wait for them. Each
  runs with `--network host` and the runner's user, so it reaches the binary on 127.0.0.1
  and its files can be removed afterwards. `HOME` is the scenario's directory, so each
  client reads the configuration the scenario writes there (`.npmrc`, `pip.conf`,
  `.pypirc`, `NuGet.Config`) and never a developer's.

  | Image | Client | Scenarios |
  |---|---|---|
  | `node.Dockerfile` | Node 24 LTS with its npm 11 | every npm scenario |
  | `node-npm12.Dockerfile` | npm 12 installed on the same Node image, because no Node release bundles it yet; npm is pinned in the file and moved by hand | every npm client scenario again: each is a Scenario Outline over `npm` and `npm 12` |
  | `maven.Dockerfile` | The newest Maven 3 (3.10) on Eclipse Temurin | every Maven scenario |
  | `maven-3.9/Dockerfile` | Maven 3.9 on Eclipse Temurin, in a directory of its own so that its Dependabot entry can hold the 3.9 line | the release and snapshot round trip again, as a Scenario Outline over `Maven` and `Maven 3.9` |
  | `python.Dockerfile` | The newest Python 3 (`slim`) with `pip`, `twine`, `uv` and `poetry` installed at versions pinned in the file and moved by hand; poetry has a virtual environment of its own | every `@pypi` scenario (`Python`) |
  | `dotnet.Dockerfile` | The .NET SDK's LTS line (10.0), whose `dotnet` carries NuGet's client | every `@nuget` scenario (`.NET`) |
  | `dotnet-11.Dockerfile` | The SDK's newest line (11.0; its go-live release candidate until .NET 11's release on 2026-11-10, after which Dependabot moves the digest to the release) | every `dotnet` scenario again: each is a Scenario Outline over `.NET` and `.NET 11` |

The existing `@nuget_publish` scenarios also fetch the SDK-generated Portable PDBs by
symbol-server key and compare their bytes, then verify 404 after native package deletion
(#571). The original focused symbol-proof run passed both SDK scenarios together in 5.65
seconds locally on Linux (60 steps), before the expanded publishing lifecycle was combined
with it. It reused the existing images and startup; no debugger tool or additional client
matrix is needed.

- **helm and terraform** are pinned binaries, like `crane`, `oras` and `cosign`, from
  `.github/actions/registry-clients`: helm 4 (`ARTIFACTWEAVE_HELM_BIN`), and the terraform
  that `deploy/check.sh` pins (`ARTIFACTWEAVE_TERRAFORM_BIN`), which the action reads from
  there. They run on the runner, so their scenarios need no Docker; each skips without its
  binary. `HOME`, and helm's configuration, cache and data directories, are the scenario's,
  so `repositories.yaml`, `.terraformrc` and `.terraform.d/credentials.tfrc.json` come from
  there; the runner's `HELM_*` and `TF_*` variables are dropped.
  The `@terraform_providers` scenarios additionally use the official null provider 3.3.2
  archive (`ARTIFACTWEAVE_NULL_PROVIDER_ZIP`), downloaded and SHA-256 checked by the same
  action. They install and apply through the signed registry and a TLS network mirror
  without a signing key. Local runs skip without the archive; the mirror also skips on
  macOS/Windows because Terraform 1.16.4's Go 1.26 build honors `SSL_CERT_FILE` only on
  Linux. CI always supplies both binaries and the archive and executes both paths. The
  mirror proxy overrides the public base URL so its signed archive URLs remain HTTPS.
  Registry scenarios fetch the binary's public discovery document over HTTP and copy
  its `modules.v1` and `providers.v1` URLs into a `host` block in the scenario's
  `.terraformrc`. Terraform requires HTTPS for its own discovery request, but accepts
  HTTP service URLs from this override. The isolated `HOME` default config is deliberate:
  setting `TF_CLI_CONFIG_FILE` disables Terraform 1.16.4's lookup of
  `.terraform.d/credentials.tfrc.json`. The combined `@terraform_registry` scenario
  passes `TF_TOKEN_<host>` directly in the child process environment, including the
  port, and proves it overrides an invalid saved credential. The runner's credentials
  remain excluded. The mirror uses a credentials block for its TLS host instead.
- **The P8 smoke scenarios** were introduced before the P8 formats and still run against generic repositories
  laid out by hand (`package_clients.feature`): `pip download` through `pip.conf` and the
  folder listing, read as a simple index, and `uv pip install` of the wheel's item path;
  `dotnet restore` from a service index that names only a flat container, since
  `dotnet nuget push` needs a publish service that a generic repository lacks; `helm pull`
  through an index made by `helm repo index` and by the chart's URL; and `terraform init`
  of a module whose source is a zip's item URL. The wheel and the nupkg are built by the
  steps, so no scenario reaches a build backend on the network.

- **Maven's plugins** come from `~/.m2/repository`, restored by `actions/cache` and keyed
  on the fixture POMs that pin every plugin version. The scenarios deploy with
  `package deploy:deploy`, never `install`, and resolve into a fresh local repository
  that chains to the cache, so an artifact under test is never served from the cache.
- **The JFrog CLI's package commands** use the runner's tools for `jf npm` and `jf gradle`
  (Gradle on the default Java 17). The Python and .NET scenarios instead mount the pinned
  CLI into the existing Python and SDK containers, so delegated commands use those exact
  client versions and isolated configuration.
  `jf mvn` cannot use the runner's Maven, which is 3.10: the JFrog Maven extractor that
  `jf` 2.126.0 downloads (`build-info-extractor-maven3` 2.43.9 in the recorded proof) fails to start
  on Maven 3.10 with `NoSuchMethodError` on `DefaultArtifactResolver`'s constructor. The
  `jf mvn` scenario copies Maven 3.9 out of the `maven-3.9` image and names it in
  `M2_HOME`, which `jf` reads before it looks for `mvn` on `PATH`. Its builds set
  `maven.compiler.release` to 17 for the runner's Java, and install into the scenario's own
  local repository.
- **Gradle** runs informationally for M2 (decisions 0015 and 0036). `make accept` runs with
  `ARTIFACTWEAVE_ACCEPT_TAGS=~@gradle`, and a later step with `continue-on-error` runs the
  `@gradle` scenarios against a binary built with the same flags, so its build is a cache
  hit and its coverage joins `cover/accept`. A failure there shows on the step and leaves
  the job green.
- **Go** is the runner's toolchain, with `GOTOOLCHAIN=local`, no checksum database and
  every cache under the scenario's directory. The go command sends credentials only over
  HTTPS, and the binary serves plain HTTP, so the `@go` scenarios put a TLS-terminating
  reverse proxy in front of the binary. go trusts it through `SSL_CERT_FILE`, which macOS
  and Windows honour from Go 1.27, so these scenarios also run locally on macOS.

**Added time for the clients (#270): about a minute.**

| Run | Job | `make accept` |
|---|---|---|
| Cold Maven cache (run 38943154688) | 274 s | 235 s |
| Warm Maven cache (run 38944697563) | 271 s | 229 s |
| The last four runs on `main`, before these clients | 192 to 245 s | 157 to 201 s |

The image builds overlap setup and the binary's compile, so the cost is the three
scenarios themselves. The cache saves little, because the plugins are about 16 MB.

**Added time for the P7 exit checks: about two minutes.** The acceptance job took 445 s on
#519 against 331 s on `main` before it, within the job's 15-minute budget. Measured
locally, scenario by scenario: the second npm run adds about a minute,
most of it the login scenario's paced prompts; the Maven scenarios, the second Maven run and
`jf mvn` add about a minute and a half, mostly plugin downloads into fresh local
repositories; the wiring scenario takes about a second. The Gradle step adds under a
minute.

**Added time for the P8 clients (#534): about ten seconds, inside the spread between runs.**
The acceptance job took 505 s on #563 (`make accept` 439 s), against 404 to 605 s
(`make accept` 356 to 528 s) in the last four runs on `main` before it. Measured locally,
the five P8 smoke scenarios take about 8 s together: a `dotnet restore` about 2 s for each
SDK, the Python tools under 2 s, and helm and terraform a second or less each. The three
images build in the background with the other four, and the helm and terraform downloads
add about a second to the clients step.

**Terraform registry exit checks (#556): one additional native scenario.** A local
Linux run of all six `@terraform` scenarios took 12.83 s, excluding compilation and
the Docker availability probe. The new combined module/provider scenario took 4.27 s
(4.36 s in isolation), including anonymous rejection, environment credentials,
newest-compatible module selection, plan/apply and delete/reindex/restore recovery.
The existing JFrog publishing scenario now also asserts the selected module version
and runs `terraform plan`; all its commands together took 1.72 s. These measurements
used the Linux runner on an ARM64 host, with the pinned AMD64 Terraform 1.16.4 and
null-provider fixture and native ARM64 JFrog CLI 2.126.0. They describe scenario cost,
not a measured change in the full CI job. CI continues to run the pinned AMD64 clients
under its 900 s job budget. No new image, provider download or service is added; the
full acceptance job remains the budget check. Without the Terraform binary, all six
scenarios skip cleanly.

**Helm exit checks (#551): seconds of additional client work.** The existing classic
and OCI scenarios now verify signed provenance, search and rendering, OCI login and
show, and replacement with and without Delete permission. Two additional scenarios
cover layout enforcement and index changes after deploy, copy, move, delete and partial
reindex. They reuse the pinned Helm 4.3.1 and JFrog 2.126.0 binaries, with no extra
client image or service.

Local `@helm` measurements on macOS, excluding the binary build and Docker probe:

| Scope | Scenarios | Time |
|---|---|---|
| `main` at `a3aa194b` | 5 | 11.18 s |
| Expanded exit checks | 7 | 6.73 to 13.45 s |
| New enforcement and lifecycle scenarios, within the final run | 2 | 1.24 s |

Concurrent checks on the developer host make the end-to-end difference smaller than
the variation between runs; these measurements do not establish a precise Linux
increment. The last successful
[main acceptance job](https://github.com/weaveplatform/artifactweave/actions/runs/39443555721)
took 618 s (`make accept` 542 s), against the 900 s budget. The full Linux job remains
the budget check for each PR. With neither client binary configured, all seven Helm
scenarios skip cleanly.

**PyPI exit checks (#541): native installs and build records.** The `@pypi`
scenarios now exercise pip, uv and Poetry in HTML and JSON mode, offline sdist builds,
required hashes, yanking and exact pins, layout and overwrite permissions, Warehouse
JSON, and delete/reindex recovery. JFrog pip/twine and uv verify dependency and
artifact build records; JFrog Poetry verifies publishing and an artifact build record.
The existing single pinned Python image supplies every tool.

The final local Linux-container run completed **13 scenarios and 326 steps in
24.64 s**, including the original generic-repository smoke scenario; the complete Go
package run took 28.39 s including its binary build and Docker probe. The same local
setup ran the five pre-change scenarios at `7e8eebe0` in 5.79 s, an observed local
increase of 18.85 s. These are local measurements, not a Linux CI increment. Without configured client images, all 13
scenarios skip their client-dependent steps cleanly.

The pre-change [main acceptance job](https://github.com/weaveplatform/artifactweave/actions/runs/39578651300/job/119150454360)
took **674 s** overall, with **495.884 s** inside the Go acceptance suite and 596 s
for the `make accept` step. At this P8 checkpoint the 900 s job budget and 600 s
test watchdog were unchanged; the full PR acceptance run and the P8 integration
run supplied the budget checks. The current limits are documented above.

Two pinned-client limits are documented in [decision 0037](../research/decisions/0037-pypi.md):
Twine 7 refuses `--skip-existing` for private URLs before uploading (ordinary identical
retries succeed), and JFrog 2.126.0 rewrites Poetry's install source without `/simple`.
Native Poetry installation and JFrog pip/uv dependency records are verified; the
JFrog Poetry build proof uses publishing.

**NuGet exit checks (#547): about 48 seconds of added local client work.** Existing publish/v2 scenarios now run clean-cache
restores of native packages on both pinned SDK images, verify SHA-512 lock hashes,
duplicate/refusal and delete/re-push behavior, standalone symbols and forced
authentication. Four added scenarios cover snapshot cleanup and JFrog native
push/restore build records. The v2 JFrog restore uses an explicit `NuGet.Config`:
JFrog 2.126.0 native mode was observed to ignore `--nuget-v2` and use v3.
The pinned SDK images have neither `nuget.exe` nor Mono, so this matrix does not
claim legacy `nuget.exe list` or `jf nuget restore` coverage. It exercises v2
NuGet.Protocol through `dotnet package search`/`restore` and `jf dotnet`.

The .NET 11 RC2 NativeAOT CLI can finish search/push HTTP work but hang on exit;
#547 reproduced push on the pinned ARM64 image. These commands, including JFrog's
push subprocess, use Microsoft's documented
[`DOTNET_CLI_ENABLEAOT=false` fallback](https://github.com/dotnet/core/blob/main/release-notes/11.0/preview/preview7/sdk.md).
Restore, pack and delete retain their normal execution. Every successful client
command must still exit zero.

Local Linux ARM64 Docker measurements, with warm images and excluding the binary
build and Docker probe:

| Scope | Scenarios | Time |
|---|---:|---:|
| Original publish outline, with the same managed push fallback | 2 | 8.98 s |
| Expanded publish outline | 2 | 30.77 s |
| New JFrog build-info outlines | 2 | 17.91 s |
| New snapshot retention outlines | 2 | 4.50 s |
| Added .NET 11 v2 search/restore example | 1 | 3.85 s |
| Complete final `@nuget` matrix (518 steps) | 16 | 71.31 s |

The approximately 48 s increment is the expanded outline's difference plus the
five new examples. A concurrent earlier run was slower; the full Linux CI job
remains the check against the 900 s budget. No image or timeout was added.

**P8 final wiring and phase sign-off (#560).** The final cycle reuses the pinned Python
3.15 image (pip 26.2.1, twine 7.0.0, uv 0.13.2 and Poetry 2.5.1), both .NET SDK images,
Helm 4.3.1, Terraform 1.16.4, null provider 3.3.2 and JFrog CLI 2.126.0. It adds no client
image or background service. The [implementation plan](../implementation/plan.md#p8-pypi-nuget-helm-terraform-locals)
maps each native exit criterion to its permanent feature and prerequisite PR.

The `@p8_wiring` outline uses the existing wheel, nupkg, Helm chart and Terraform archive
fixtures. It checks both source and target indexes and actual download bytes after REST
deploy, copy, delete, trash restore and reindex. Terraform module and provider-mirror
downloads follow the signed URLs returned by their protocols. A read-only SQLite query
waits for each accepted reindex to complete, with a 20-second deadline and diagnostics for
failed or parked work. This observer changes no stored content or index. Only the Helm
case needs an external binary; none needs a client container.

| Measurement | Acceptance job | Go acceptance package | Interpretation |
|---|---|---|---|
| Main `7e8eebe0`, [run 39578651300](https://github.com/weaveplatform/artifactweave/actions/runs/39578651300) | 674 s | 495.884 s | Baseline before the final P8 cycle; the `make accept` step took 596 s |
| All eight final-cycle PRs, integration `25e3b8f7`, [run 39587717027](https://github.com/weaveplatform/artifactweave/actions/runs/39587717027) | 748 s | 583.244 s | Complete native acceptance passed; the `make accept` step took 673 s |
| Local shared wiring, macOS | Not a CI measurement | 7.973 s | Six scenarios / 207 steps: 3.187 s of scenarios, including the existing P7 case; the five P8 cases took 2.03 s together |

The complete final-cycle run took 74 seconds more for the Linux acceptance job and
87.360 seconds more for its Go acceptance package than the main baseline. These are
observed differences between CI runs, not isolated per-feature costs: compilation, cache
state and host load vary. Local increments from individual tracks must not be summed and
presented as a CI measurement. The complete job passed within 900 seconds and its tests
within the existing 600-second watchdog. The reported Go package elapsed time also includes
the binary build in `TestMain`, before `m.Run` starts that watchdog. Informational Gradle
acceptance took a further 24.368 seconds, included in the job total. The integration branch
combines PRs #603 through #610; its merge resolutions preserve all native assertions and
status/principal rows.
All prerequisite PRs and PR #609 are merged. PR #609 recorded the final phase sign-off
and reran the quality gate on the merged base, including the dependency refresh in #602;
the table above retains the earlier combined integration measurement.

Pinned-client boundaries are part of the evidence, not skipped success assertions. Twine
7 rejects private-registry `--skip-existing` before HTTP; ordinary retries are tested.
JFrog Poetry's pinned install source rewriting omits `/simple`, so native Poetry proves
installation and JFrog Poetry proves publishing/build-info. The .NET SDK images have no
nuget.exe/Mono, and JFrog's observed native-mode `--nuget-v2` behavior is bypassed with an
explicit v2 source whose Download requests are asserted. The .NET 11 RC2 managed CLI
fallback is limited to the affected search and push commands, which still must exit zero.
The Terraform backend exercises pasted-token login and saved-token reuse; `login.v1`
browser approval remains deferred to #262.

## Transient infrastructure failures

Two failures came from the runners rather than the code, and each needed a manual rerun
(#148). CI now absorbs both.

| Failure | Response |
|---|---|
| `create container: reaper: … wait for reaper …: context deadline exceeded` | `quality-gate.yml` sets `TESTCONTAINERS_RYUK_DISABLED=true`, so testcontainers-go never starts its reaper (Ryuk). Hosted runners are discarded after each job, so nothing is left behind. The emulators terminate at the end of each test through `testcontainers.CleanupContainer`. The PostgreSQL container that `internal/sqlstore/sqltest` shares across a test binary stays up until the runner goes. Local runs keep the reaper, which removes that container when the binary exits |
| `read "https://proxy.golang.org/…": stream error: … INTERNAL_ERROR` | Every Go job runs `.github/actions/go-mod-download` straight after `actions/setup-go`. It runs `go mod download`, retrying twice with 15 s and 30 s pauses, so lint, tests, `go tool` and golangci-lint then read the module cache and never fetch. Only the download is retried: a failing test or lint step still fails |

## Real-cloud conformance (manual)

`cloud-conformance.yml` runs the blob contract (`blobtest.Run`) and the research 18
probes against a real Google Cloud Storage bucket, a real Azure Blob Storage container
and a real Amazon S3 bucket. fake-gcs-server, Azurite and MinIO do not enforce compose
preconditions, copy-source authorisation, signed URLs, S3's copy limits or service
limits, so this is the only check of them. The workflow is not part of the gate: it needs
cloud credentials and is started from the Actions tab (**Run workflow**, target `all`,
`gcs`, `azure` or `s3`). Each run
writes under `conformance/<random>/` and deletes that prefix when it ends. The step
summary lists the `PROBE` lines that answer the open questions in
[research 18 §4](../research/18-cloud-blob-storage.md#4-open-questions-for-a-real-cloud-check).
A run whose variables or secrets are missing fails rather than skipping. All three jobs run
in the `cloud-conformance` environment, because the clouds trust that environment's
subject and nothing else (see [Cloud trust and environments](#cloud-trust-and-environments)).

| Name | Kind | Value |
|---|---|---|
| `CONFORMANCE_GCS_BUCKET` | variable | The bucket (`terraform output bucket` in `deploy/platform/terraform/gcp/single-bucket`) |
| `CONFORMANCE_GCP_SERVICE_ACCOUNT` | variable | The data-plane service account the job acts as (`terraform output conformance_service_account` in `deploy/ci/terraform/gcp`). `gcp/single-bucket` grants it `roles/storage.objectAdmin` on the bucket, and it signs URLs through IAM `signBlob` as itself |
| `CONFORMANCE_GCP_WIF_PROVIDER` | variable | The Workload Identity Federation provider (`terraform output workload_identity_provider` in `deploy/ci/terraform/gcp`). The job exchanges its GitHub OIDC token for the service account, so no key is stored; organisations enforcing `iam.disableServiceAccountKeyCreation` allow no keys anyway |
| `CONFORMANCE_AZURE_ACCOUNT` | variable | The storage account (`terraform output account` in `deploy/platform/terraform/azure/single-bucket`). It allows no Shared Key |
| `CONFORMANCE_AZURE_CONTAINER` | variable | The container in that account |
| `CONFORMANCE_AZURE_CLIENT_ID`, `CONFORMANCE_AZURE_TENANT_ID`, `CONFORMANCE_AZURE_SUBSCRIPTION_ID` | variables | The data-plane managed identity the job logs in as with `azure/login` (OpenID Connect), from `deploy/ci/terraform/azure`. `azure/single-bucket` grants it Storage Blob Data Contributor on the account |
| `CONFORMANCE_S3_BUCKET` | variable | The bucket (`terraform output bucket` in `deploy/platform/terraform/aws/single-bucket`) |
| `CONFORMANCE_AWS_ROLE_ARN` | secret | The data-plane role the job assumes with `aws-actions/configure-aws-credentials` (OpenID Connect), from `deploy/ci/terraform/aws`. A secret rather than a variable because the ARN carries the account ID, which the public workflow logs must not show; the action also masks the account ID. The role has no policy of its own: `aws/single-bucket`'s bucket policy grants it that bucket's objects |
| `CONFORMANCE_AWS_REGION` | variable | The role's region |

`make ci-github-vars CLOUD=gcp` (and `CLOUD=azure`, `CLOUD=aws`) sets the identity
variables; `make gcp-github-vars`, `make azure-github-vars` and `make aws-github-vars` set
the bucket and account.

Give every store a lifecycle rule that deletes `conformance/` objects after a day, in
case a cancelled run skips its cleanup; on S3, also abort incomplete multipart uploads,
which are billed until aborted. On GCS, a bucket with soft delete enabled bills
the deleted temporary parts for the retention period.

The same tests run from a workstation. They skip unless their variables are set:

```bash
make gcp-conformance     # or, by hand, after make gcp-credentials (writes .local/gcp-credentials.json):
ARTIFACTWEAVE_TEST_GCS_BUCKET=$(terraform -chdir=deploy/platform/terraform/gcp/single-bucket output -raw bucket) \
ARTIFACTWEAVE_TEST_GCS_ACCESS_ID=$(terraform -chdir=deploy/platform/terraform/gcp/single-bucket output -raw service_account) \
GOOGLE_APPLICATION_CREDENTIALS=deploy/platform/terraform/gcp/single-bucket/.local/gcp-credentials.json \
  go test -count=1 -v -run 'TestContractOnRealGCS|TestRealGCSProbes' ./pkg/blob/gcs/

make aws-conformance     # or, by hand, after aws login:
ARTIFACTWEAVE_TEST_S3_BUCKET=$(terraform -chdir=deploy/platform/terraform/aws/single-bucket output -raw bucket) \
ARTIFACTWEAVE_TEST_S3_REGION=eu-west-2 \
  go test -count=1 -v -run 'TestContractOnRealS3|TestRealS3Probes' ./pkg/blob/s3/

make azure-conformance   # or, by hand, after az login:
ARTIFACTWEAVE_TEST_AZURE_ACCOUNT=$(terraform -chdir=deploy/platform/terraform/azure/single-bucket output -raw account) \
ARTIFACTWEAVE_TEST_AZURE_CONTAINER=artifactweave \
  go test -count=1 -v -run 'TestContractOnRealAzure|TestRealAzureProbes' ./pkg/blob/azure/
```

The GCS run acts as the conformance service account through the impersonated credentials
`make gcp-credentials` writes from your own Application Default Credentials
(`gcloud auth application-default login`). `deploy/ci/terraform/gcp`
creates the account, lets your login act as it, and holds the federation for the workflow;
`gcp/single-bucket` creates the bucket and grants the account.

The S3 run uses the AWS default credential chain: your `aws login` session locally, the
role `configure-aws-credentials` assumes in the workflow. Locally it acts as your own login
rather than the conformance role, which only GitHub's OIDC token can assume. The contract
lowers the copy limits, so committing its large blob takes the `UploadPartCopy` path that
objects over 5 GiB take on S3.

The Azure run authenticates with `DefaultAzureCredential`: your `az login` locally, the
`azure/login` OIDC session in the workflow. Both hold Storage Blob Data Contributor on the
account, and the probes sign copy sources with a user delegation SAS from it. An
`ARTIFACTWEAVE_TEST_AZURE_ACCOUNT_KEY`, for an account that still allows Shared Key, switches
the contract and probes to that key instead. `make azure-conformance` refuses to run unless
the Azure CLI's active subscription is the acceptance one
(`deploy/ci/check-subscription.sh`, shared by the azure roots, with the subscription in
`deploy/ci/lab.mk`).

## Cloud trust and environments

Each cloud's bootstrap (`deploy/ci/terraform/<cloud>`, decision 0030)
holds GitHub's trust, in two GitHub environments:

| Environment | Identity on each cloud | Rights | Used by |
|---|---|---|---|
| `cloud-conformance` | data plane: `artifactweave-conformance` | objects in the `single-bucket` bucket or account only | `cloud-conformance.yml` |
| `cloud-platform` | deployer: `artifactweave-deployer` | create and destroy scenario resources: the GCP lab project's storage, load balancing, Certificate Manager and the scenarios' DNS zone; Azure's `artifactweave-scenarios-rg` and DNS zone; AWS buckets named `artifactweave-*`, CloudFront, ACM, the scenarios' hosted zone and roles under `/artifactweave-scenarios/`, inside a permissions boundary every role it creates must carry; each cloud's state store | `platform-auth-check.yml`, `platform-scenarios.yml` |

Every trust matches this repository's immutable OIDC subject,
`repo:weaveplatform@336817058/artifactweave@1415179278:environment:<env>`, exactly: GCP's
provider admits only the two environment subjects and each service account trusts one;
each Azure identity has one federated credential for one subject; each AWS role's trust
policy matches one subject with `StringEquals`. `cloud-platform` asks
the maintainer to approve every run, from any branch, so a pull request can prove its own
scenario; self-approval is allowed. `make ci-github-vars CLOUD=<cloud>` creates both
environments (`deploy/ci/github-environments.sh`) and sets these variables:

| Name | Value |
|---|---|
| `PLATFORM_GCP_WIF_PROVIDER`, `PLATFORM_GCP_SERVICE_ACCOUNT` | The provider and the deployer service account |
| `PLATFORM_GCP_PROJECT`, `PLATFORM_GCP_STATE_BUCKET` | The lab project and the remote state bucket |
| `PLATFORM_AZURE_CLIENT_ID`, `PLATFORM_AZURE_TENANT_ID`, `PLATFORM_AZURE_SUBSCRIPTION_ID` | The deployer identity, for `azure/login` |
| `PLATFORM_AZURE_RESOURCE_GROUP`, `PLATFORM_AZURE_STATE_ACCOUNT` | The scenarios resource group and the remote state account |
| `PLATFORM_AWS_ROLE_ARN` (secret), `PLATFORM_AWS_REGION` | The deployer role, for `configure-aws-credentials`, stored as a secret because the ARN carries the account ID; and the region |
| `PLATFORM_AWS_STATE_BUCKET`, `PLATFORM_AWS_DNS_ZONE_ID` | The remote state bucket and the scenarios' hosted zone |

`platform-auth-check.yml` (`cloud-platform`; dispatched by hand, and run on pull requests
that change a bootstrap, after the maintainer approves) logs in to all three clouds as the
deployer and makes read-only calls that need its rights: the GCS state bucket and the DNS
zone's records, the scenarios resource group and the Azure state container, and the S3
state bucket and the hosted zone's records. It creates nothing.

## Platform scenarios (manual)

`platform-scenarios.yml` runs the reference deployments in `deploy/platform/terraform` on
real clouds, from the `cloud-platform` environment, so the maintainer approves every run.
Its inputs are `cloud`, `scenario`, `action` (`test`, `apply`, `destroy` or `reap`),
`instance` and `ttl_minutes`; [`deploy/platform/README.md`](../../deploy/platform/README.md#running-a-scenario-in-ci)
shows how to dispatch each. Every action goes through `deploy/ci/scenario-ci.sh`:

- Terraform 1.16.4, pinned by SHA-256 as in the `terraform` job, works on a fresh copy of
  the scenario with a backend generated for the instance's state, at
  `scenarios/<cloud>/<scenario>/<instance>` in the bootstrap's state store. On Azure the
  provider and backend exchange the job's OIDC token themselves (`ARM_USE_OIDC`). On AWS,
  `configure-aws-credentials` assumes the deployer from the `PLATFORM_AWS_ROLE_ARN` secret,
  with the account ID masked, and the S3 backend locks with `use_lockfile`.
- `test` builds artifactweave, starts it with the scenario's `artifactweave_config` output,
  and runs `test/platform` through `deploy/ci/platform-tests.sh`, the same step
  `make verify` runs against a scenario applied from a workstation: a 40 MiB deploy and round trip, a ranged GET, the storage
  API's checksums, a `HEAD` answered by the registry, a `downloadRedirect` 302 to the
  scenario's `storage_host` whose signed URL serves the file and a range of it, and the
  same URL refused without its signature. Scenarios add their own checks in
  `test/platform`: the `multi-region` mirrors and `multicloud/mirror` show a copy on each member, the redirect
  preferring the member in the instance's zone, uploads and reads with that member drained
  (artifactweave restarted with its `write_enabled` off), and repair copying back to it once
  re-enabled. Every output reaches the checks through `ARTIFACTWEAVE_SCENARIO_OUTPUTS`. The
  instance is destroyed in a step that always runs.
- `destroy` reads the instance's state from the store first. An instance whose state is
  empty or absent, because its apply failed early, needs no configuration: `destroy`
  removes the empty state and succeeds, so a failed run's clean-up never fails on it.
- Each run works on a copy of the scenario beside a copy of `modules/`, so module sources
  (`../../modules/<cloud>/<capability>`) resolve as they do in the repository.
- `reap` destroys expired instances and deletes resources that outlived their state. AWS
  buckets are found by listing the deployer's `artifactweave-` buckets with their regions and
  reading each one's tags there, because a scenario can span regions and the tagging API
  answers for one region only. A versioned bucket is emptied of its versions first. For
  `aws/cdn`, an AWS `reap` also lists CloudFront distributions and `us-east-1` certificates
  by their tags, disabling a distribution and waiting for every edge before deleting it, and
  finds the untagged origin access control, cache policy, key group, public keys and the
  instance's records by name; `check-clean` looks for all of them. A multicloud `reap` leaves
  these to AWS's. For `gcp/cdn`, a GCP `reap` deletes the load balancer's pieces by name, the
  SSL policy among them, then the Certificate Manager map entries, maps, certificates and DNS
  authorisations by their labels, then the instance's records in `PLATFORM_GCP_DNS_ZONE`;
  `check-clean` looks for all of them too. For `azure/cdn`, an Azure `reap` deletes Front Door
  profiles before anything else in the scenarios resource group and leaves a profile's
  children, its endpoint among them, to it; with a custom domain, it also deletes the tagged
  TXT and CNAME records in the zones the deployer sees. A destroy that fails deletes the
  instance's Front Door profiles directly before it reports the failure, and `check-clean`
  names any profile left as billed by the hour.
- `cloud` may be `multicloud`, for `multicloud/mirror`: the job logs in to GCP, Azure and AWS
  as their deployers, the state lives in GCP's state bucket under `scenarios/multicloud/`, and
  names take AWS's `artifactweave-awscn-` prefix. Its `reap` and `check-clean` look on all
  three clouds; its `reap` deletes only resources labelled with a multicloud scenario, which
  a single cloud's `reap` skips.
- `aws/cdn` and `gcp/cdn` run with a host of their own, `cdn-<instance>` in the bootstrap's zone,
  and a key `scenario-ci.sh` generates for each instance, outside Terraform: a CloudFront key
  pair through `deploy/ci/cloudfront-key.sh` for `aws/cdn`, and a Cloud CDN signed URL key
  through `deploy/ci/cloud-cdn-key.sh` for `gcp/cdn`, which `apply` adds to the backend bucket
  with `gcloud` before applying again to grant Cloud CDN's fill account. Their generic checks'
  redirects go through the real edge; `cdn_test.go` adds `cdnRedirect` alone, the `S3`
  redirect header, filenames kept through the edge cache, an expired signature refused, and
  `jf rt dl` across the edge, with the pinned `jf` the job installs
  (`.github/actions/jfrog-cli`, the acceptance job's pin too). For `gcp/cdn` it first waits,
  up to 45 minutes, until the edge serves a blob of its own from the URL artifactweave
  redirects to, which takes the managed certificate, the load balancer and the grant, and then
  checks that Cloud CDN passes both response overrides to the bucket on a cache fill, which
  Google does not document. The AWS session lasts an hour, so the job renews it before a
  proof's second half and before a clean-up after a failure.
- `azure/cdn` puts a storage account behind a Front Door Premium profile, which bills each
  started hour it exists (about $0.45), so its proof runs `test` alone, its destroy and
  `check-clean`: `prove-matrix.sh` marks it `test` and the workflow skips the later apply,
  destroy and reap. The endpoint's own `*.azurefd.net` host serves, and the account answers
  the runner's address alone. Before artifactweave starts, `frontdoor_test.go` approves Front
  Door's private endpoint connection, which the azurerm provider cannot, and waits for the
  storage firewall and for the edge to reach the account, up to 40 minutes. It runs
  `cdn_test.go`'s checks with a SAS signer, then shows that an edge copy stops being served
  once its SAS and the route's cache cap have passed, and that with the account's public
  endpoint off, a SAS on the account is refused while the edge still serves over Private
  Link. For the same cost, a change to `cdn_test.go` proves `aws/cdn` and `gcp/cdn` but not
  `azure/cdn`, which is proven again only when its own files change or under `prove-all`.
- The `geo-replicated` scenarios add checks that look past artifactweave at the cloud's own
  copy: S3's replication status and the replica object, the GCS bucket's real dual-region
  placement, and the Azure secondary endpoint serving the blob through artifactweave's SAS.
  A copy still in flight after 12 minutes is reported rather than failed, because the
  clouds bound replication only by their recovery point objectives.
- State and plans are never uploaded, and nothing uploads coverage: scenario runs are
  evidence, not coverage, so `make test` leaves `test/platform` out.

On a pull request that changes the workflow, the driver, `test/platform`, a scenario or a
module, the same job runs a proof for each scenario `deploy/ci/prove-matrix.sh`
chooses from the changed paths:

| Changed path | Proves |
|---|---|
| The workflow, `scenario-ci.sh`, `platform-tests.sh` or `prove-matrix.sh` | Every cloud's `single-bucket` |
| `cloudfront-key.sh` | Every scenario whose `ci-vars.sh` takes its key pair (`CLOUDFRONT_*`): `aws/cdn` |
| `cloud-cdn-key.sh` | Every scenario whose `ci-vars.sh` takes its key (`CLOUD_CDN_*`): `gcp/cdn` |
| A `test/platform` Go file | The scenarios it names as quoted `"<cloud>/<scenario>"` literals: `mirror_test.go` the `multi-region` scenarios and `multicloud/mirror`, `replica_test.go` the `geo-replicated` ones, `cdn_test.go` `aws/cdn` and `gcp/cdn`, `frontdoor_test.go` `azure/cdn` |
| A `test/platform` Go file that names none (`harness_test.go`, `generic_test.go`), any other `test/platform` file, or a deleted one | Every cloud's `single-bucket` |
| A module's top-level `.tf` (`modules/<cloud>/<capability>/*.tf`) | Every scenario, in any cloud directory, whose Terraform sources exactly that module |
| A scenario's top-level `.tf`, `.terraform.lock.hcl` or `ci-vars.sh` | That scenario |
| Anything else: READMEs, Makefiles, `tests/`, the local runner (`local.sh`), the bootstraps and the rest of `deploy/ci`, docs | Nothing |

The `prove-all` label proves every scenario that runs in CI (`prove-matrix.sh --all`), on
the push that adds it and on every later push while it stays. Adding any other label proves
nothing. The paths filter still decides whether the workflow runs, so the label proves only
pull requests the workflow covers. `deploy/ci/prove-matrix_test.sh` checks
these rules against a generated tree, and that every scenario a `test/platform` file names
runs in CI; `make deploy-check` runs it first.

Each proof runs `test`; an `apply`, then a `destroy` that has only the remote state; an
instance applied already expired, then a `reap` that removes it; then `check-clean`, which
fails if any state, bucket or resource of the run remains. A scenario `prove-matrix.sh`
marks `test` (`azure/cdn`) skips the middle two, so it creates one instance, not three. A
failed step triggers a clean-up step that destroys the run's instances. Each proof has its own run tag (the pull
request, the run and the scenario's initials) and its own concurrency group, so proofs run in
parallel; its `reap` takes `REAP_PREFIX` and its `check-clean` the run tag, so neither touches
another proof's instances. Both match the tag up to the hyphen before the step, because one
scenario's initials can begin another's (`mirror`'s `m`, `multi-region`'s `mr`).

## OCI conformance

The `OCI conformance` job runs the OCI distribution-spec conformance suite against the
registry (decision 0015, research 23 section 9). `test/oci/run.sh`, which
`make oci-conformance` calls, fetches `opencontainers/distribution-spec` by the commit tag
`v1.1.1` points to and builds `conformance/` with its own `go.mod`; this module never imports
it. The build is cached in `.tools/` per commit. The script then starts one `-cover` binary,
creates the `oci-conformance` and `oci-mount` local `docker` repositories and a `conformance`
user with Read, Deploy and Delete on both, and runs the suite with:

- `OCI_NAMESPACE=oci-conformance/conformance` and
  `OCI_CROSSMOUNT_NAMESPACE=oci-mount/conformance`, so mounts cross repositories;
- `OCI_USERNAME` and `OCI_PASSWORD`, so the suite authenticates as that user;
- `OCI_AUTOMATIC_CROSSMOUNT=0`, as a mount without `from` opens an upload session;
- `OCI_DELETE_MANIFEST_BEFORE_BLOBS=1`, as blob deletes answer 405 (decision 0031);
- `OCI_HIDE_SKIPPED_WORKFLOWS=0`, so the report shows every workflow.

A failed specification fails the job; a skipped one never does. The suite skips some
specifications by design, for example the 201-without-`from` mount when automatic cross-mount
is off, and the 202 follow-up when a mount succeeds with 201.

Each category is one variable at the top of the `Makefile`, switched on by the ticket whose
operations it tests, so turning one on is a one-line change:

| Category | Variable | Switched on by |
|---|---|---|
| Pull | `OCI_TEST_PULL` | #224, with blobs (#222) and manifests (#223) |
| Push | `OCI_TEST_PUSH` | #224, with blobs (#222) and manifests (#223) |
| Content Discovery | `OCI_TEST_CONTENT_DISCOVERY` | #227, once #225 (referrers) and #226 (tags list) had both landed |
| Content Management | `OCI_TEST_CONTENT_MANAGEMENT` | #227, once #226 (deletes) had landed; the suite needs Push and Content Discovery for it |

All four are on: 73 specifications pass and 6 are skipped by design. Setting a variable to 0,
for example `make oci-conformance OCI_TEST_CONTENT_MANAGEMENT=0`, runs without that category.

## Terraform scenarios

`make terraform` runs `test/terraform/run.sh` over `TERRAFORM_SCENARIOS`: the repositories,
identity, OIDC, CDN repositories and remote repositories references in `deploy/artifactweave/terraform/`. `test/terraform/` holds only
the driver.
- It downloads `terraform-provider-artifactory_<version>_<os>_<arch>.zip` and
  `terraform-provider-platform_<version>_<os>_<arch>.zip` once into a filesystem mirror,
  checking each against the SHA-256 pinned in the script.
- It points `TF_CLI_CONFIG_FILE` at a CLI configuration that installs both providers only
  from that mirror.
- For each scenario it runs `terraform init -lockfile=readonly` and `terraform validate`.

Each scenario's committed `.terraform.lock.hcl` holds its providers' hashes for linux and
darwin on amd64 and arm64.

The providers move automatically. Dependabot cannot move them, because `run.sh` pins their
release zips by SHA-256, so `deps-refresh.yml`'s `jfrog-providers` job runs
`deploy/ci/jfrog-providers.sh update` every day. It:
1. Finds each provider's latest GitHub release.
2. Downloads its `SHA256SUMS` and checks the detached signature against
   `deploy/ci/jfrog-terraform-providers.asc`, the key the Terraform registry publishes for
   the `jfrog` namespace. Its fingerprint, `7F2AD74397A7E25E2D0B082E2FA4D2A520237FA7`, is
   pinned in the script, so a rotated key fails the run for a person to review.
3. Moves the version in every reference's `versions.tf`, and the version and the four zip
   checksums in `run.sh`.
4. Writes each lock afresh with `terraform providers lock -fs-mirror` from a mirror of the
   four zips, each checked against the signed sums.
5. Moves the versions the READMEs name and regenerates their terraform-docs sections.
6. Runs `run.sh validate` over every reference, so the pins, checksums and locks agree
   before anything is committed.

A patch or minor update goes up as one PR from the org App on `deps/jfrog-providers`, titled
`chore(deps): refresh the JFrog Terraform providers to <versions>`. `auto-merge.yml` merges
it once the gate passes, including the `terraform` apply job and `deploy-check`. A major goes
up on `deps/jfrog-providers-major`, which `auto-merge.yml` never merges, so a person reviews
it. The script never moves a pin backwards.

`make jfrog-providers` (`MODE=check|update|dry-run`, default `dry-run`) runs the same
script locally. `dry-run` updates a copy of the tree and prints the diff;
`ARTIFACTORY_TARGET` and `PLATFORM_TARGET` name a version instead of the latest release.
`deploy/ci/jfrog-providers_test.sh`, which `make deploy-check` runs, checks the rewrites and
the signature check offline. Its signature cases use the real 12.11.14 `SHA256SUMS` and
signature committed in `deploy/ci/testdata/jfrog-providers`.

`make terraform TERRAFORM_MODE=apply` adds the full run, which the `terraform` CI job runs:
1. Build and start one `-cover` binary, writing its coverage to `cover/terraform`.
2. Mint an admin access token with the bootstrap admin's password
   (`POST /access/api/v1/tokens`), which the server takes because step 1 starts it with
   `ARTIFACTWEAVE_TOKEN_ALLOW_BASIC_AUTH=true`. The providers authenticate only with a Bearer
   token. Set `TERRAFORM_ACCESS_TOKEN` to supply one instead.
3. Apply each scenario with its own steps:
   - **repositories:** apply the repositories and the GPG key pair, with properties on the
     generic repository itself, deploy `fixture.txt`, set the Terraform provider
     repository's `primaryKeyPairRef` to the pair through the repositories API, then apply
     the file's properties, and check that the repositories data source lists the generic
     repository's package type as `Generic` and that the provider repository still names
     the pair;
   - **identity:** generate an RSA key pair for `artifactory_keypair`, apply, then check
     that the permission written through Access v2 reads back through the legacy v2
     permission target API;
   - **oidc:** apply the OIDC providers and their identity mappings;
   - **cdn-repositories:** apply repositories with CDN and direct-storage redirect flags,
     whose read-back is checked by the empty plan;
   - **remote-repositories:** seed a proxy through the strict YAML descriptor and apply
     fourteen provider-supported remote formats,
     require an empty refreshed plan, check common and format-specific API fields/defaults,
     rotate test credentials and update every repository, then repeat the API and empty-plan
     checks. Sensitive repository values are redacted and persistent bytes contain no
     plaintext fixture credentials. A remote `terraformbackend` request must fail validation.
     Upstream URLs use the fixture server; this proves management only, with native content
     evidence owned by #624–#633. Provider `artifactory_proxy` CRUD still requires P12/#303
     XML descriptor reads; the fixture removes its proxy through PATCH after destroy.
4. Require `terraform plan -detailed-exitcode` to exit 0.
5. Destroy.

Terraform runs on a copy of each scenario in the work directory, so its state never lands
in the source tree. The empty plan is the check that matters: the providers read back
every attribute they wrote. The repositories fixture found two gaps in the repository API,
both fixed in #69. The server echoed the boolean flags JFrog documents as strings in that
string form, which the provider cannot decode. It also left out `environments` when a
repository has none, which the provider reads as null against its default of an empty
set. The identity scenario found that the provider creates key pairs with
`POST /artifactory/api/security/keypair/`, with a trailing slash, which #85 routes.

`artifactory_permission_target` is a stub in provider 12.11.14: every operation returns
"deprecated. Use platform_permission instead". The identity scenario uses
`platform_permission` and reads the result back with the `artifactory_permission_target`
data source, which still calls `/artifactory/api/v2/security/permissions/{name}`.

The repositories scenario also holds a local repository of each P8 type (#535): PyPI,
NuGet, Helm, Helm OCI, Terraform module, Terraform provider and Terraform backend, with the
NuGet, Helm and Helm OCI fields set away from the provider's defaults, and an
`artifactory_keypair` of `pair_type = "GPG"`. They round-tripped without a server change:
the provider's lower-case `terraformType` was already accepted (it never reads the field
back), and its booleans are JSON booleans. `artifactory_local_terraform_provider_repository`
has no `primary_keypair_ref` in provider 12.11.14, so `run.sh` associates the pair through
`POST /artifactory/api/repositories/{key}`, and the empty plan shows the provider leaves
`primaryKeyPairRef` alone. `run.sh` makes a fresh passphrase-less GPG pair for each run
with `tools/gpgkey`, which uses `github.com/ProtonMail/go-crypto`, rather than `gpg`: `gpg` is
not on every host, macOS ships without it, and its agent fails when the socket path under
its home directory is too long. The provider warns "Usage of GPG can't be validated" at every plan,
because it checks only RSA private keys; the server checks that the two halves match.

The P9 remote-provider fixture (#637) adds fourteen remote resources and a second
update/empty-plan cycle with credential rotation. A local Go 1.27.3 darwin/arm64
run of all five provider scenarios, including coverage build and destroy, completed
in **23.92 seconds**. This is local timing; the required Linux Terraform job remains
the CI measurement. Configuration round-trips do not prove native remote retrieval,
which has separate format-specific acceptance scenarios.

## Deployment Terraform checks

`deploy/check.sh` (`make deploy-check`, and the `deploy-terraform` job) checks every
directory under `deploy/` and `test/terraform` that holds `*.tf` files. Roots are found,
not listed, so moving one needs no change to the script.

| Step | What |
|---|---|
| `prove-matrix` | `deploy/ci/prove-matrix_test.sh`, the table test of the rules that choose which scenarios a pull request proves. It needs no tools, so it runs before any is fetched |
| `contract` | `deploy/ci/lint-contract.sh`, then its `--self-test`: every root against decision 0030's rules for its kind; see [The reference-deployment contract](#the-reference-deployment-contract). It needs no tools either |
| `terraform fmt -check -recursive` | Formatting, including the `tests/*.tftest.hcl` files |
| `terraform validate` | Each root, with `init -backend=false` against its committed lock file; state and plugins stay in a temporary directory. Roots on the jfrog providers validate through `test/terraform/run.sh`, whose filesystem mirror pins their zips |
| tflint | Each root, with the rulesets pinned in `.tflint.hcl`. `azurerm_resources_missing_prevent_destroy` is off: every root is created and destroyed on purpose |
| `trivy config` | Each root, with the checks embedded in the pinned binary (`--skip-check-update`). An accepted finding is a `#trivy:ignore:<id>` comment beside the resource with its reason, so it moves with the root |
| `terraform test` | Each root with `tests/*.tftest.hcl`. The tests use `mock_provider` and `command = plan`: no credentials and no cloud calls |
| terraform-docs | Each README with `BEGIN_TF_DOCS` markers is regenerated in a copy, using `.terraform-docs.yml`, and must match the committed file. `make deploy-docs` rewrites them in place |

The script fetches terraform (unless the one on `PATH` is the pinned version), tflint,
trivy and terraform-docs into the gitignored `.tools/` directory and checks each archive
against the SHA-256 from its release, so a laptop and CI run the same versions. To move a
tool, change its version and its four checksums (linux and darwin, amd64 and arm64) in
`deploy/check.sh`. The rulesets' versions live in `.tflint.hcl`.

Each root's `.terraform.lock.hcl` holds `h1:` hashes for linux and darwin on amd64 and
arm64. The script shares a provider plugin cache across roots, and Terraform refuses a
cached provider whose `h1:` hash the lock file lacks, so a lock written on one platform
fails in CI. After changing a root's providers, run `terraform providers lock
-platform=linux_amd64 -platform=linux_arm64 -platform=darwin_amd64 -platform=darwin_arm64`
in it.

Dependabot's `terraform` ecosystem updates the infrastructure roots' providers and lock
files, minor and patch versions only: a provider major merged on mocked tests alone could
break a real apply. `deploy/artifactweave` is left out: `run.sh` pins its provider zips by
SHA-256, so `deps-refresh.yml` updates those pins, signed checksums and lock files through
the separate process under
[Terraform scenarios](#terraform-scenarios).

## The reference-deployment contract

`deploy/ci/lint-contract.sh` checks each Terraform root under `deploy/` against the rules
decision [0030](../research/decisions/0030-reference-deployments.md) sets for its kind, and
the root's path decides its kind:

| Root | Kind | Rules |
|---|---|---|
| `deploy/platform/terraform/{aws,gcp,azure,multicloud}/<name>` | scenario | S1 to S11 |
| `deploy/platform/terraform/modules/<cloud>/<capability>` | module | M1 to M6, and S6 |
| `deploy/artifactweave/terraform/<name>` | app layer | A1 to A4 |
| `deploy/ci/terraform/<cloud>` | bootstrap | B1 |
| all of `deploy/` | global | G1 and G2 |

A directory holding `*.tf` that fits none of these fails as `layout`. Each finding names its
rule with the decision's ID:

```text
FAIL [S5] deploy/platform/terraform/aws/single-bucket: harness.tf lacks output "expires_at"
```

The lab values that S6 and S8 forbid are every value in `deploy/ci/lab.mk` plus the lab's
fixed names: the conformance and deployer identities, the Azure resource groups, the AWS
permission boundary and role path, and `weaveplatform.run`. S6 also covers comments and
defaults, so an example region in a scenario or module cannot be the lab's `eu-west-2`.

The lint reads the sources with grep and awk and relies on `terraform fmt`'s layout, so it
needs no tools. `--self-test` builds a repository in a temporary directory, with a passing
root of each kind that follows the real README templates, then breaks one rule per case and
expects the lint to fail it.

Every finding fails `make deploy-check`, so a new root meets the contract from its first
commit; there is no list of exempt roots, since epic #189 ported the last of them. A new
scenario starts from a copy of one that passes, and its README from
`deploy/platform/SCENARIO-TEMPLATE.md`.

## AQL fixtures from the JFrog CLI

`pkg/aql/aqltest/testdata` holds the AQL the pinned `jf` sends, one file per command shape.
With `ARTIFACTWEAVE_JF_BIN` pointing at the pinned binary, `make capture-aql`:
1. runs the acceptance scenario "the query shapes the fixtures capture", with the server
   logging every query it is sent at debug level (`ARTIFACTWEAVE_AQL_CAPTURE` names the
   directory the harness writes them to);
2. copies each query over its fixture.

Run it whenever the pinned `jf` changes, then check that the expected rows in
`pkg/aql/aqltest` still hold.

## Releases

release-please (`release-please.yml`) keeps a release pull request open on `main`, built from
the Conventional Commits since the last release. Merging it tags `vX.Y.Z` and creates the
GitHub release with the changelog. It pushes the tag with the org's release-please App
token: a tag pushed with the default `GITHUB_TOKEN` triggers no workflow, which would leave
the release tagged and unbuilt.

The tag triggers `release.yml`, which runs [GoReleaser](../../.goreleaser.yaml) in `append`
mode on the existing release (#507):

- `artifactweave` for linux, darwin and windows on amd64 and arm64, built as `make build`
  builds it: `CGO_ENABLED=0`, `GOWORK=off`, `-trimpath`, the `disable_grpc_modules` tag, and
  `internal/buildinfo`'s version, commit and date stamped with `-X`;
- a `tar.gz` archive per platform (`zip` for Windows), a SHA-256 checksum file, and an SPDX
  SBOM per archive (syft);
- a keyless cosign signature over the checksum file, in a Sigstore bundle. The
  certificate names `release.yml` at the tag, so `cosign verify-blob
  --certificate-identity` checks which workflow built the release, and the checksums then
  check every archive and SBOM. The README shows the two commands.

The same run publishes the container image (#510). GoReleaser's `dockers_v2` builds one
multi-arch image for linux/amd64 and linux/arm64 from the binaries it has just built, with
[`Dockerfile.release`](../../Dockerfile.release), which only copies the binary into the
pinned distroless base, so the image holds exactly what the archives hold. It pushes the
image to `ghcr.io/weaveplatform/artifactweave` as `<version>`, plus `<major>.<minor>` and
`latest` for releases that are not pre-releases. buildx attaches an SBOM and a provenance
attestation, and cosign signs the image by digest with the same keyless workflow identity as
the checksum file. The job has `packages: write` for the push. The root `Dockerfile` still
builds from source for `make docker` and acceptance.

Release-please owns the version, the changelog and the release; GoReleaser only appends
artifacts, so a failed build leaves the release intact and can be re-run from the Actions
tab without re-tagging.

On a pull request that changes `release.yml`, `.goreleaser.yaml` or `Dockerfile.release`, the
`check` job builds a snapshot of every archive, the checksums and one image per platform
(`goreleaser release --snapshot --skip=sign,sbom`), without pushing, so a broken config fails
before a tag rather than after it. Locally:
`goreleaser check` and `goreleaser release --snapshot --clean --skip=sign,sbom`, which writes
to the gitignored `dist/`.

## Local equivalents

| Command | What |
|---|---|
| `make gate` | Local vet, formatting/lint, unit tests, coverage, vulnerability scan and cross-compilation. Generation/parity, native acceptance, OCI conformance and Terraform checks are separate targets; this is not the complete CI gate |
| `make accept` | The acceptance job (needs Docker). `ARTIFACTWEAVE_JF_BIN`, `ARTIFACTWEAVE_CRANE_BIN`, `ARTIFACTWEAVE_ORAS_BIN`, `ARTIFACTWEAVE_COSIGN_BIN`, `ARTIFACTWEAVE_HELM_BIN` and `ARTIFACTWEAVE_TERRAFORM_BIN` name the pinned `jf` (`.github/actions/jfrog-cli`) and `crane`, `oras`, `cosign`, `helm` and `terraform` (`.github/actions/registry-clients`); their scenarios skip without them, and the `docker` scenarios run on Linux only, where the daemon shares the binary's 127.0.0.1. `ARTIFACTWEAVE_NODE_IMAGE`, `ARTIFACTWEAVE_NPM12_IMAGE`, `ARTIFACTWEAVE_MAVEN_IMAGE`, `ARTIFACTWEAVE_MAVEN39_IMAGE`, `ARTIFACTWEAVE_PYTHON_IMAGE`, `ARTIFACTWEAVE_DOTNET_IMAGE` and `ARTIFACTWEAVE_DOTNET11_IMAGE` name the npm 11, npm 12, Maven 3.10, Maven 3.9, Python, .NET 10 and .NET 11 images built from `test/acceptance/clients`, and their scenarios skip without them or off Linux; `jf mvn` needs the Maven 3.9 image too. `jf npm` and `jf gradle` run the `npm` and `gradle` on `PATH` and skip without them. `ARTIFACTWEAVE_ACCEPT_TAGS` takes a godog tag expression, such as `~@gradle`. `ARTIFACTWEAVE_MAVEN_REPO` moves the Maven repository they mount from `~/.m2/repository`. The `@go` scenarios need only `go` on `PATH` |
| `make oci-conformance` | The OCI conformance job (needs network the first time, to fetch and build the suite into `.tools/`); reports in `oci-report/` |
| `make generate` | Regenerate servers and clients; CI fails on drift |
| `make terraform TERRAFORM_MODE=apply` | The terraform job (needs `terraform`; without the mode it only validates) |
| `make deploy-check` | The deploy-terraform job (fetches pinned tools into `.tools/`; set `GITHUB_TOKEN` if tflint hits GitHub's rate limit) |
| `make deploy-docs` | Rewrite the terraform-docs sections of the READMEs |
| `make platform-<target> SCENARIO=<cloud>/<scenario>` | A target of a `deploy/platform` scenario on a real cloud, for example `platform-plan SCENARIO=gcp/single-bucket`; `gcp-<target>`, `azure-<target>` and `aws-<target>` are short forms for the three `single-bucket` scenarios. CI never runs these |
| `make platform-verify SCENARIO=<cloud>/<scenario>` | `test/platform` against a scenario you applied, through the same `deploy/ci/platform-tests.sh` step CI runs; `platform-up`, `platform-smoke` and `platform-down` run artifactweave natively against it (`deploy/ci/local.sh`) |
| `make ci-<target> CLOUD=<cloud>` | A target of a cloud's bootstrap in `deploy/ci/terraform`, for example `ci-plan CLOUD=azure` or `ci-github-vars CLOUD=aws`. CI never runs these |
| `make capture-aql` | Refresh the AQL fixtures from the pinned `jf` (needs `ARTIFACTWEAVE_JF_BIN`) |


## Generic remote native evidence

`@generic_remote` runs real curl and pinned JFrog CLI 2.126.0 against the actual
application and controlled upstream. It proves cold `jf rt direct-download` and curl
GETs, warm reuse during an upstream outage, cache-alias `jf rt dl`/AQL search, Read-only
access without Deploy, exact downloaded bytes, upstream request counts and repository
deletion. Client configuration and output directories are isolated per scenario.
The local macOS run passed 1 scenario/44 steps in 2.12 seconds (7.10 seconds reported
by Go including startup); this is not a Linux full-suite timing estimate. No timeout
or matrix change is needed. Runtime race tests additionally cover HEAD/ranges/304,
transient reads, checksum syntax, signed delivery, restart identity and revocation
before publication. Native-format adapters remain separate P9 exit obligations.
