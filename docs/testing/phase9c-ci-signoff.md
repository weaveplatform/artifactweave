# Phase 9c CI qualification

The four-lane CI policy is qualified for [#810](https://github.com/weaveplatform/artifactweave/issues/810). Representative PRs, a manual merged-main run and the current release candidate passed. [#792](https://github.com/weaveplatform/artifactweave/issues/792) separately owns the final all-format run after its qualification changes merge.

The implementation merged in [#822](https://github.com/weaveplatform/artifactweave/pull/822); [#837](https://github.com/weaveplatform/artifactweave/pull/837) renamed the selector to `tools/citestselection`. No coverage floor, exclusion or selected-job requirement was relaxed.

## Representative PR runs

The [probe ledger](evidence/phase9c-ci-probes.json) retains exact head/base identities, changed paths, selected/skipped lane reasons, downloaded test inventories, job conclusions, timestamps and retry history. Inventories matched local planner reproduction as JSON values. Five probes use baseline `69071727c505db8694ccc680732be4a8c69b0504`; the mixed-root probe was refreshed onto repaired main `2633fc54c769d2e2ee51a625fad5db4f2d18b0a1`. All temporary probes are closed without merging.

| Probe | Profile and selected lanes | Result | Elapsed / runner minutes |
|---|---|---|---|
| [docs](https://github.com/weaveplatform/artifactweave/actions/runs/41684754262) | `docs`: documentation only | success | 1.42 / 1.30 |
| [format](https://github.com/weaveplatform/artifactweave/actions/runs/41684767361) | `format`: npm unit, package acceptance and npm native clients | success | 7.40 / 24.88 |
| [mixed](https://github.com/weaveplatform/artifactweave/actions/runs/41695315776) | `full`: all four lanes (shared root package) | success | 54.37 / 137.58 |
| [provider](https://github.com/weaveplatform/artifactweave/actions/runs/41684792364) | `deployment`: provider conformance only | success | 3.43 / 3.28 |
| [deployment](https://github.com/weaveplatform/artifactweave/actions/runs/41684803667) | `deployment`: deployment checks only | success | 5.87 / 5.77 |
| [terraform-shared](https://github.com/weaveplatform/artifactweave/actions/runs/41685777779) | `deployment`: provider conformance and deployment checks | success | 4.70 / 7.40 |

Elapsed time includes retry waiting; runner time sums distinct executed attempts, including failures. GitHub mirrors reused successes under new job IDs with their original timestamps; those executions are counted once and linked to their original attempt in the ledger. These are observed examples, not a controlled performance benchmark.

## Full manual and release comparison

The [full-run ledger](evidence/phase9c-ci-full-runs.json) retains every job attempt, artifact identities, actual coverage-policy output and exact commits, including all 42 job records from the failed pre-#928 manual run and 21 records from a separate failed readiness attempt of #928. Each run records its workflow hash, native time budgets and Lua download options. Each full inventory equals the downloaded [coverage-prerequisite inventory](evidence/phase9c-ci-coverage-prerequisite.json): **3,723 entries** (2,070 unit, 1,649 package acceptance, four fuzz seeds). The JSON references that shared inventory without duplicating it.

| Run | Exact head | Coverage | Elapsed / runner minutes |
|---|---|---|---|
| [mixed-probe](https://github.com/weaveplatform/artifactweave/actions/runs/41695315776) | `2b8692da8cbe2de10e157224c4965d9c33ef82d0` | 95.0% (74,199/78,104) | 54.37 / 137.58 |
| [main-manual](https://github.com/weaveplatform/artifactweave/actions/runs/41714297500) | `b7209c0c4219e0b40ee13f902c2803fb4a43b9bf` | 95.0% (74,202/78,104) | 37.83 / 134.67 |
| [release](https://github.com/weaveplatform/artifactweave/actions/runs/41695727742) | `faa2d9042bea825c04764e995481209f3512525a` | 95.0% (74,201/78,104) | 88.80 / 193.92 |

All full runs execute the three-OS unit race/shuffle matrix and fuzz seeds, Go package acceptance, required native partitions, Terraform provider conformance, Terraform deployment checks, OCI, lint, security, spec parity and cross-compilation. Gradle retains its separately labelled informational status. The 95% overall and 90% per enforced package policy passes with the recorded `.testcoverage.yml` identity; existing exclusions are unchanged.

The package inventory is not the complete native harness inventory. The test-kind guard also sees seven native harness entry points and one real-cloud entry point outside package selection. Native acceptance runs its own harness. Public-upstream `TestAccRealUpstreams`, the separate `@bowerlive` Godog feature, and real-cloud scenarios remain opt-in under their separate workflow contracts; this deterministic qualification does not claim they ran.

## Selection and failure interpretation

The 3,723 entries form the selector inventory, not a count of completed tests. Package entries retain environment/platform prerequisites and may skip. Baseline direct `TestAccPinnedClient`, `TestAccHuggingFaceNativeXet` and the local-directory JetBrains IDE test are not enabled; native Godog Hub/Xet/JetBrains scenarios provide separate evidence. Other optional entries include real-cloud blob tests, Terraform client prerequisites and crash subprocess helpers. The #792 refresh explicitly enables the direct Hub/Xet tests; that later execution is not attributed to these baseline runs.

Full native filters are core `~@nuget&&~@gradle&&~@bowerlive&&~@jvmrecipes`, NuGet `@nuget&&~@gradle`, JVM `@jvmrecipes`, and informational Gradle `@gradle`. These strings and the known optional package entries are retained in each full-run record.

The npm probe selects 140 entry points and only npm/native wiring clients. Unit and acceptance prefixes run in separate jobs; both Terraform lanes skip with recorded reasons. Same-run affected coverage applies, but a test-only change has no changed production package floor and makes no whole-project coverage claim.

The mixed root package conservatively selects full. Its unit and acceptance entry points remain in the same package and execute in their respective jobs. Selector fixtures cover build tags, unknown tests, renamed/deleted paths, shared dependencies, release branches, manual full override and rejection of unsuccessful selected jobs. Four fuzz entry points have explicit unit-lane ownership; three TestMain hooks are not independently selected tests.

The original mixed run failed total coverage at 94.9% (74,146 / 78,104). Test-only [#925](https://github.com/weaveplatform/artifactweave/pull/925) repaired archive integrity, migration rollback/retry, Xet authorization and cached PyPI hash coverage. Its full run passed at 95.0% (74,199 / 78,104), then it merged as `2633fc54c769d2e2ee51a625fad5db4f2d18b0a1`.

The first shared-tooling probe correctly failed because it changed pinned `deploy/check.sh` without regenerating the recipe evidence lock. Its replacement isolates shared Terraform selection using `deploy/ci/lint-contract.sh`. The first manual/mixed native attempts and the release retry then hit a Lua source-download timeout during concurrent image setup. The test process had launched, but no completed native suite result was produced. Same-head retries retained those failures. [PR #928](https://github.com/weaveplatform/artifactweave/pull/928) adds bounded connection/transfer retries while retaining source checksum verification. All attempts are retained; coverage combines only artifacts from its own workflow run.

The first release native attempt reached the unchanged 30-minute suite deadline near the final VCS scenarios. A separate main-based native run in #927 passed ([41695659370](https://github.com/weaveplatform/artifactweave/actions/runs/41695659370), head `0e77afc54acdc132c9aeb85e6503f202b3089657`); it does not qualify the release head. The manual main retry also exhausted the 30-minute watchdog while a Vagrant scenario had run for 16 seconds. [PR #928](https://github.com/weaveplatform/artifactweave/pull/928) provides a 35-minute full-suite watchdog, 45-minute compilation/readiness wrapper and 55-minute overall job budget. Selected format tests retain 15 minutes. All scenarios and failure checks remain required; the earlier failures remain in the ledger.

The automatic readiness run of #928 separately encountered a missing MinIO image after a six-minute pull; the same digest started after another three-minute pull in a later scenario. That delay contributed to a 35-minute watchdog failure as a new Vagrant scenario began. Its failed first attempt is retained as diagnostic history, not qualifying evidence.

The mixed probe and release candidate passed with the earlier 30-minute test, 40-minute wrapper and 45-minute job budgets, without the new bounded Lua retries. The manual run at `b7209c0c4219e0b40ee13f902c2803fb4a43b9bf` validates the new 35/45/55-minute budgets and retry settings. These distinct configurations remain explicit in the per-run evidence. Release-please left its branch unchanged after the test-only #925 merge. Updating the candidate branch brought in repaired main without publishing a release. The recorded release SHA is the candidate qualified for this checkpoint; later changes require their own full run.
