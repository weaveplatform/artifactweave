# Phase 9d final qualification

P9d moved artifactweave's code to the modular-monolith layout of [decision 0044](../research/decisions/0044-modular-monolith-migration.md) without changing its behaviour. This record qualifies the promoted tree on merged main `83561b74` and closes [PRO-05](https://github.com/weaveplatform/artifactweave/issues/1104), [EPIC-PRO](https://github.com/weaveplatform/artifactweave/issues/946) and the backlog index [#935](https://github.com/weaveplatform/artifactweave/issues/935). It follows the [Phase 9c qualification ledger](phase9c-signoff.md) and [#810's CI evidence](phase9c-ci-signoff.md). P10 has not started.

## Scope and commits

| Name | Commit | Meaning |
|---|---|---|
| B | `ec4592a513985e4986f60b060984e40c3347be73` | The pre-migration base: the parent of FND-01's squash commit `0e5def29` ([#1105](https://github.com/weaveplatform/artifactweave/pull/1105)), recorded in `migration/README.md` until PRO-03 deleted that file |
| D | `d4b573361ec3dc334fc6135dcd06ac914c76e6c3` | Merged main after [#1199](https://github.com/weaveplatform/artifactweave/pull/1199) (WEB-21 to PRO-04) and [#1201](https://github.com/weaveplatform/artifactweave/pull/1201) (the tidy-up) |
| Q | `83561b74696453b64f8d3a644567042a296bca85` | Merged main after [#1204](https://github.com/weaveplatform/artifactweave/pull/1204), which adds tests for four packages, and [#1209](https://github.com/weaveplatform/artifactweave/pull/1209), which adds a transaction test across two modules' stores |

The qualification started on D. The full CI run, the real-cloud conformance run and the platform scenarios ran there, but `make gate` failed its coverage step: four packages were below the 90% package floor on the unit and Go acceptance lanes that `make gate` merges (see [Coverage](#coverage)). [#1204](https://github.com/weaveplatform/artifactweave/pull/1204) added tests for them. Checking the epics' acceptance found one gap: no test showed that the module stores roll back together, which epic STO asks for, so [#1209](https://github.com/weaveplatform/artifactweave/pull/1209) added one (see [Tree](#tree)). The full CI run, the real-cloud conformance run, `make gate` and the release builds then ran again on Q. The platform scenarios were not repeated. Between D and Q only tests and test support changed: `git diff --stat D Q -- . ':(exclude)*_test.go'` lists `test/server/artifacts/hex/doc.go` (one comment line) and `internal/app/storetest/{core.go,doc.go}`, a test-support package that `go list -deps ./cmd/artifactweave` does not list. `test/platform` did not change either, so the binary each scenario built from D (`deploy/ci/platform-tests.sh:31`) has the same source as Q's.

[p9d-path-map.json](../implementation/p9d-path-map.json) maps each legacy package path to its final one. Decision 0044 records the only two changes a user can see: a database created by v0.1.x or v0.2.x is refused with `ErrLegacyHistory` and must be recreated (per-module migration baselines, STO-06), and the work queue's OpenTelemetry meter scope follows the package path to `github.com/weaveplatform/artifactweave/internal/platform/work` (PLT-14). Routes, statuses, response envelopes, YAML configuration keys, the CLI, and error, log and metric text are otherwise unchanged.

## Full quality gate

The full profile ran on both commits (`gh workflow run quality-gate.yml --ref main`; a manual run always selects it). Both passed on attempt 1:

| Lane | Job | D, [run 42979272313](https://github.com/weaveplatform/artifactweave/actions/runs/42979272313) | Q, [run 43014208366](https://github.com/weaveplatform/artifactweave/actions/runs/43014208366) |
|---|---|---|---|
| Unit tests, Linux | Go unit tests (ubuntu-latest) | success | success |
| Unit tests, macOS | Go unit tests (macos-latest) | success | success |
| Unit tests, Windows | Go unit tests (windows-latest) | success | success |
| Go acceptance | Go acceptance tests (Linux packages) | success | success |
| Native-client partitions | Go acceptance (Linux, native clients); (Linux, Conan 1 and 2 clients); (Linux, NuGet native clients); (Linux, JVM client recipes) | success | success |
| Gradle (informational) | Go acceptance (Linux, Gradle informational) | success | success |
| OCI conformance | OCI conformance | success | success |
| JFrog Terraform provider conformance | Terraform provider conformance (pinned JFrog apply, plan, destroy) | success | success |
| Terraform deployment checks | Terraform deployment checks (fmt, validate, tflint, trivy, mocked tests, docs) | success | success |
| Spec parity | Spec parity (JFrog oracle) | success | success |
| Lint and `make verify-generate` | Lint (blocking) | success | success |
| Vulnerabilities | govulncheck | success | success |
| Cross-compile | Cross-compile | success | success |
| Merged coverage | Coverage gate (≥95% total, ≥90% per package) | 95.5% (75507/79055) | 95.6% (75582/79055) |

On Q the lowest package in the merged coverage is `internal/artifacts/ecosystem/agentpackages/http` at 90.0% (126/140).

## Coverage

`make gate` merges only the unit and Go acceptance lanes; CI's coverage gate also merges the native-client lanes. On D `make gate` passed vet, lint, the unit tests and the Go acceptance tests, then failed `make cover`: the total was 95.2% (75253/79016), but four packages were below 90%. CI's own Linux unit and Go acceptance data from #1201's run gives the same four, so CI passed them only through the native-client lanes. #1204 added tests for them and changed no production code:

| Package | D, `make gate` | Q, `make gate` |
|---|---|---|
| `internal/artifacts/ecosystem/pub` | 78.5% (157/200) | 96.0% (192/200) |
| `internal/artifacts/ecosystem/pub/http` | 82.5% (298/361) | 97.0% (350/361) |
| `internal/artifacts/ecosystem/hex/http` | 89.8% (396/441) | 93.2% (411/441) |
| `internal/artifacts/adapters/http/itemkit` | 89.5% (990/1106) | 94.4% (1044/1106) |

`make gate` on Q passed (2026-11-11, macOS arm64 with Docker) and printed `Total test coverage: 95.4% (75408/79016)`, with every package at 90% or more. Its govulncheck step ran with `GOTOOLCHAIN=go1.27.4` and found no vulnerabilities. The workstation's installed Go was 1.27.3, under which govulncheck reports GO-2026-6859 (a `net/http` fault published on 2026-11-11 and fixed in Go 1.27.4) as reachable. CI already used Go 1.27.4, because `setup-go` reads `go 1.27` from `go.mod` and takes the newest patch release. [#1208](https://github.com/weaveplatform/artifactweave/issues/1208) tracks the root `Dockerfile`'s Go 1.27.2 build image, which nothing refreshes; the published image is built from the GoReleaser binary and is not affected.

## Real-cloud conformance

The three `single-bucket` conformance stores (`deploy/platform/terraform/{gcp,azure,aws}/single-bucket`) were applied again from the workstation on 2026-11-10, and `make gcp-github-vars`, `make azure-github-vars` and `make aws-github-vars` refreshed the repository variables for their new names. `gh workflow run cloud-conformance.yml --ref main -f target=all` ran on both commits, and both runs passed on attempt 1:

| Job | Backend | D, [run 42979276498](https://github.com/weaveplatform/artifactweave/actions/runs/42979276498) | Q, [run 43014213807](https://github.com/weaveplatform/artifactweave/actions/runs/43014213807) |
|---|---|---|---|
| Google Cloud Storage | `internal/platform/blob/gcs` | success | success |
| Azure Blob Storage | `internal/platform/blob/azure` | success | success |
| Amazon S3 | `internal/platform/blob/s3` | success | success |

## Platform scenarios

`deploy/ci/prove-matrix.sh --all` prints 13 scenarios. Each ran `platform-scenarios.yml` on D with `action: test` (apply, `test/platform` against a binary built from the checkout, destroy), approved in the `cloud-platform` environment. Twelve passed on attempt 1. The Azure CDN scenario's first run failed one check, "the edge keeps each item's filename" (`test/platform/cdn_test.go:98-111`): artifactweave redirected the download of a second name for the same blob to Azure Front Door, which answered the ranged request with 404 instead of 206, although the first name had just been served from the edge cache. Its destroy step succeeded. The Azure blob backend and the CDN signer have no logic change between B and D, only imports, a moved package comment and lint directives, so the scenario ran again on D with a new Front Door endpoint and passed every check; the second name was a cache miss served with its own filename, as in the scenario's passing run on 2026-10-16. [#1211](https://github.com/weaveplatform/artifactweave/issues/1211) tracks the failure. [Scope and commits](#scope-and-commits) says why the D runs stand for Q.

| Cloud | Scenario | Run | Result |
|---|---|---|---|
| aws | cdn | [42979280575](https://github.com/weaveplatform/artifactweave/actions/runs/42979280575) | success |
| aws | geo-replicated | [42979284139](https://github.com/weaveplatform/artifactweave/actions/runs/42979284139) | success |
| aws | multi-region | [42979287624](https://github.com/weaveplatform/artifactweave/actions/runs/42979287624) | success |
| aws | single-bucket | [42979291284](https://github.com/weaveplatform/artifactweave/actions/runs/42979291284) | success |
| azure | cdn | [42979295008](https://github.com/weaveplatform/artifactweave/actions/runs/42979295008), then [43004659837](https://github.com/weaveplatform/artifactweave/actions/runs/43004659837) | failure ([#1211](https://github.com/weaveplatform/artifactweave/issues/1211)), then success |
| azure | geo-replicated | [42979298387](https://github.com/weaveplatform/artifactweave/actions/runs/42979298387) | success |
| azure | multi-region | [42979302361](https://github.com/weaveplatform/artifactweave/actions/runs/42979302361) | success |
| azure | single-bucket | [42979306327](https://github.com/weaveplatform/artifactweave/actions/runs/42979306327) | success |
| gcp | cdn | [42979310203](https://github.com/weaveplatform/artifactweave/actions/runs/42979310203) | success |
| gcp | geo-replicated | [42979314989](https://github.com/weaveplatform/artifactweave/actions/runs/42979314989) | success |
| gcp | multi-region | [42979318408](https://github.com/weaveplatform/artifactweave/actions/runs/42979318408) | success |
| gcp | single-bucket | [42979321985](https://github.com/weaveplatform/artifactweave/actions/runs/42979321985) | success |
| multicloud | mirror | [42979325356](https://github.com/weaveplatform/artifactweave/actions/runs/42979325356) | success |

## Release artifacts

Both builds stamp the version and commit through `internal/platform/buildinfo`, which P9d moved twice (PLT-06, PRO-03). Neither prints `devel` or `unknown`. Both ran on Q:

| Artifact | Command | Output |
|---|---|---|
| Container image | `make docker`, then `docker run --rm artifactweave:v0.2.0-92-g83561b74 version` | `artifactweave v0.2.0-92-g83561b74 (83561b74696453b64f8d3a644567042a296bca85)` |
| GoReleaser snapshot | `goreleaser build --snapshot --clean --single-target` (GoReleaser 2.18.2), then `dist/artifactweave_darwin_arm64_v8.0/artifactweave version` | `artifactweave 0.2.1-next (83561b74696453b64f8d3a644567042a296bca85, 2026-11-11T11:41:08Z)` |

## Behaviour evidence

### Acceptance features

`git diff -U0 B Q -- test/acceptance/features | grep -E '^[+-][^+-]' | grep -vE '^[+-][[:space:]]*#'` leaves only Feature description prose whose one change is a path from the path map:

| File | Change |
|---|---|
| `cdn.feature:6` | `pkg/cdn/cdntest` to `internal/platform/cdn/cdntest` |
| `cdn_front_door.feature:6` | `pkg/cdn/cdntest` to `internal/platform/cdn/cdntest` |
| `multipart.feature:6` | `internal/service/multipart` to `internal/artifacts/core/multipart` |
| `round_trip.feature:5` | `pkg/aql/aqltest` to `internal/artifacts/core/aql/aqltest` |

The grep drops one more change, a comment in `trash.feature:119-120` that names the storage-info test at its new path. The whole diff is 5 files, 20 insertions and 18 deletions. Every commit that touched the features between B and Q is a P9d pull request (`git log --format='%h %s' B..Q -- test/acceptance/features` lists [#1142](https://github.com/weaveplatform/artifactweave/pull/1142), [#1149](https://github.com/weaveplatform/artifactweave/pull/1149), [#1178](https://github.com/weaveplatform/artifactweave/pull/1178), [#1192](https://github.com/weaveplatform/artifactweave/pull/1192), [#1193](https://github.com/weaveplatform/artifactweave/pull/1193) and [#1199](https://github.com/weaveplatform/artifactweave/pull/1199)). No step, table, doc string, tag or example changed, and the features passed in the gate's acceptance lanes.

### Status tables and route and principal matrices

The tables are assembled from part files, so the comparison covers every `status_*_test.go` and `matrix_*_test.go`, not only the files that hold the test functions. The 66 part files at B map one to one to 66 at Q:

| At B | At Q |
|---|---|
| `internal/server/{status,matrix}*_test.go` (12) | `internal/app/{status,matrix}*_test.go` (12) |
| `internal/server/ansible/status_test.go` | `internal/artifacts/ecosystem/ansible/http/status_test.go` |
| `test/server/<format>/{status,matrix}*_test.go` (51, nine formats) | `test/server/artifacts/<format>/{status,matrix}*_test.go` |
| `pkg/types/matrix_test.go` | `internal/artifacts/core/model/matrix_test.go` |
| `pkg/blob/mirror/status_test.go` | `internal/platform/blob/mirror/status_test.go` |

The test functions are at `internal/app/status_test.go:1005`, `internal/app/matrix_test.go:1367,1394` and the nine `test/server/artifacts/<format>/matrix_fixture_test.go`, and all of them ran in the full gate above. `git diff -M B Q` on the 66 files changes 2237 lines in 671 hunks. With package clauses, imports and qualifier-only changes removed, 80 hunks remain. All of them are harness wiring, apart from the tidy-up's added rows below:

- the fixtures hold `*inmem.Store` or `harness.Store` where they held `store.Store`, and tune the router through `harness.Tune`;
- `item.Service` gains `Formats: ecosystem.For`, the registry ART-04 injects;
- the roles service is built by `matrixRoles` over the artifacts module's repositories (ART-07, ACC-13);
- `Config` and `New` become `RouterConfig` and `NewRouter` (WEB-22);
- `multipart.Service` gains `Restrictions` (ACC-09);
- `sinkBodyLimit` becomes `configurationhttp.SinkBodyLimit` (WEB-11);
- `auth.NewAuthenticator` becomes `authn.NewAuthenticator` (ACC-12), and the permission literals use `accesstest` (ACC-16);
- item properties are `model.Properties`, and the mirror status test seeds replicas through `SetReplicas`.

No expected status, route or principal outcome changed.

The tidy-up ([#1201](https://github.com/weaveplatform/artifactweave/pull/1201)) added rows and changed none. `calculateYumRepositoryMetadata` and `calculateDebianRepositoryMetadata` had handlers before P9d, but the stub test's `implemented` table lacked them, so the API reliability ledger recorded both as "stub: 501". The tidy-up records them as implemented and adds rows for what their handlers answer:

| Operation | Status table rows added | Matrix row added |
|---|---|---|
| `calculateYumRepositoryMetadata` | admin 200; reindexer (async) 200; user 403; anonymous 401; missing repository 404; Helm repository 400; `path=x/repodata` 400; virtual repository with a reindexer 200; remote repository 501 | `reindexYum`: managers 200 |
| `calculateDebianRepositoryMetadata` | admin 200; reindexer with `writeProps=0` 200; `writeProps=2` 400; user 403; anonymous 401; missing repository 404; Helm repository 400 | `reindexDebian`: managers 200 |

### Tree

- `GOWORK=off go list ./...` on Q lists 387 packages, none under `migration/`. Of the path map's 151 package entries, one keeps its path (`internal/app`, WEB-18). None of the other 150 legacy paths is listed except `internal/layout`: PRO-02 deleted its legacy package (path map action `delete`), and the promoted layout test package now holds that path.
- `TestUnitLedgerComplete` passed on PRO-03's base `463182d9` with all 669 entries done, as [#1199](https://github.com/weaveplatform/artifactweave/pull/1199) records. PRO-03 deleted the ledger with `migration/`.
- `TestUnitImportTiers` (no legacy exemptions), `TestUnitPackageDocs` (every package), `TestUnitTopLevelDirectories` and `TestUnitImportRulesDetectViolations` are in `internal/layout` and passed in the unit lanes above.
- The composite stores build every module's store over one unit of work. `TxAcrossModules` ([#1209](https://github.com/weaveplatform/artifactweave/pull/1209)) writes through the artifacts module's blob index and the access module's users in one transaction, commits one pair and rolls the other back. It runs on every backend as a subtest of `TestUnitContract` (`internal/app/adapters/inmem`), `TestAccContractMemory` and `TestAccContractFile` (`internal/app/adapters/sql/sqlite`) and `TestAccContract` (`internal/app/adapters/sql/postgres`). With the user write moved outside the transaction, the PostgreSQL run fails.

## Faults found during qualification

The tidy-up, #1204's tests and this qualification found these faults. Their code or configuration is the same at B, so they predate P9d, and they are tracked outside it:

| Issue | Fault |
|---|---|
| [#1202](https://github.com/weaveplatform/artifactweave/issues/1202) | The API reliability ledger records 14 operations that have handlers as "stub: 501" |
| [#1203](https://github.com/weaveplatform/artifactweave/issues/1203) | Remote Alpine package requests dereference a nil remote source service; only test servers and other embedders of the router reach it |
| [#1205](https://github.com/weaveplatform/artifactweave/issues/1205) | A Hex virtual repository stops at a remote member that answers not-found, and answers 403 for the whole index when the caller may not read a remote member |
| [#1206](https://github.com/weaveplatform/artifactweave/issues/1206) | An archive entry download answers 200 with an empty body when the archive's blob is lost |
| [#1207](https://github.com/weaveplatform/artifactweave/issues/1207) | An empty Pub upload answers 413 "pub archive exceeds limit" |
| [#1208](https://github.com/weaveplatform/artifactweave/issues/1208) | The root `Dockerfile` builds with Go 1.27.2 and nothing refreshes its base image |
| [#1210](https://github.com/weaveplatform/artifactweave/issues/1210) | Opening a pull request with a label starts two quality gate runs, and the cancelled one leaves a failing `Go unit tests (${{ matrix.os }})` check that no later run replaces |

## Exit conditions

| Exit condition ([#935](https://github.com/weaveplatform/artifactweave/issues/935)) | Evidence |
|---|---|
| `go list ./...` shows no `migration/` path and none of the legacy paths | [Tree](#tree) |
| `TestUnitImportTiers` (no legacy exemptions) and `TestUnitPackageDocs` (every package) pass, and `TestUnitLedgerComplete` passed on PRO-03's base | `internal/layout` in the [full quality gate](#full-quality-gate)'s unit lanes; [#1199](https://github.com/weaveplatform/artifactweave/pull/1199) for the ledger |
| revive `exported` and `package-comments` are active repository-wide, and `make lint` is clean | `.golangci.yml` runs revive on every path and limits only the other 19 linters to `internal/`; the gate's lint job and `make gate` |
| `make gate` and a full run on merged main pass: unit on three OSes, Go acceptance, native-client partitions, OCI conformance, JFrog Terraform provider conformance, parity, `make verify-generate`, coverage 95/90 | [Full quality gate](#full-quality-gate) and [Coverage](#coverage), on Q |
| Status tables, route and principal matrices and acceptance features pass without edits to expectations | [Behaviour evidence](#behaviour-evidence) |
| `artifactweave version` shows the ldflags-injected values | [Release artifacts](#release-artifacts) |
| The repo-layout, server-test, CI and DoD docs point at the final paths | PRO-04 in [#1199](https://github.com/weaveplatform/artifactweave/pull/1199): [repo-layout.md](../implementation/repo-layout.md), [server-test-packages.md](server-test-packages.md), [server-test-ownership.md](server-test-ownership.md), [ci.md](ci.md), [definition-of-done.md](../implementation/definition-of-done.md) |
| plan.md marks P9d done | [plan.md](../implementation/plan.md), in the pull request that adds this record |

This documentation commit records the qualification of Q. Later documentation-only edits do not relabel the runs as testing another commit.
