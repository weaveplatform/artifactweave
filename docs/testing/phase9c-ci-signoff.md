# Phase 9c CI qualification

#810 remains open until the full mixed-package, manual, and current release-candidate gates pass. The four-lane implementation was merged in [#822](https://github.com/weaveplatform/artifactweave/pull/822); [#837](https://github.com/weaveplatform/artifactweave/pull/837) renamed the selector to `tools/citestselection`.

## Representative PR runs

These temporary, comment-only PRs exercise the workflow against merged main `69071727c505db8694ccc680732be4a8c69b0504`. The [retained JSON](evidence/phase9c-ci-probes.json) includes exact selected test names and packages, lane reasons, every job conclusion and timestamp, PR head/base identities, and run links. Its test inventories were downloaded from GitHub and matched exactly as JSON values against a local planner run at each recorded head/base. Skipped Go lanes intentionally have empty inventories.

| Probe | Profile and selected lanes | Result | Elapsed / runner minutes |
|---|---|---|---|
| [docs](https://github.com/weaveplatform/artifactweave/actions/runs/41684754262) | `docs`: documentation only | success | 1.42 / 1.03 |
| [format](https://github.com/weaveplatform/artifactweave/actions/runs/41684767361) | `format`: npm unit, package acceptance and npm native clients | success | 7.40 / 24.70 |
| [mixed](https://github.com/weaveplatform/artifactweave/actions/runs/41684780367) | `full`: all four lanes (shared root package) | failure | 37.12 / 149.40 |
| [provider](https://github.com/weaveplatform/artifactweave/actions/runs/41684792364) | `deployment`: provider conformance only | success | 3.43 / 3.05 |
| [deployment](https://github.com/weaveplatform/artifactweave/actions/runs/41684803667) | `deployment`: deployment checks only | success | 5.87 / 5.58 |
| [terraform-shared](https://github.com/weaveplatform/artifactweave/actions/runs/41685777779) | `deployment`: provider conformance and deployment checks | success | 4.70 / 7.35 |

## Interpretation

The npm run selects 140 entry points and only npm/native wiring clients. It keeps `TestUnit` and `TestAcc` in separate jobs, skips both Terraform lanes, and merges coverage from the same run. Because the probe edits tests only, it has no changed production package subject to the 90% affected-package floor; it does not claim whole-project coverage.

Editing the mixed root package is a conservative full-profile input. Its inventory includes both kinds in `internal/server`; the unit matrix and package acceptance job execute their respective anchored names. The full run failed the existing overall coverage floor; all test lanes passed. A green retry is required after [#925](https://github.com/weaveplatform/artifactweave/pull/925) merges.

The first shared-tooling probe changed pinned `deploy/check.sh` without regenerating its client-recipe evidence lock. [That run](https://github.com/weaveplatform/artifactweave/actions/runs/41684814398) correctly failed the impact prerequisite and aggregate gate. The revised probe changes shared Terraform contract lint to isolate the two-lane selection from evidence-lock maintenance.

The inventory guard currently owns four fuzz seed entry points and three `TestMain` hooks, as documented in the [CI guide](ci.md). Fuzz seeds belong to the unit matrix; hooks are not independently selected tests. Existing selector fixtures cover build tags, unknown tests, renamed/deleted inputs, shared code, release branches, explicit full override and aggregate rejection of unsuccessful selected jobs.

## Full qualification still required

The [combined-format run](https://github.com/weaveplatform/artifactweave/actions/runs/41656448640) and [release candidate run](https://github.com/weaveplatform/artifactweave/actions/runs/41657031833) passed all test lanes and every package coverage floor, but failed overall coverage at **94.9% (74,146 / 78,104 statements)**. Those runs do not qualify Phase 9c.

The test-only prerequisite [#925](https://github.com/weaveplatform/artifactweave/pull/925) adds archive integrity, migration rollback/retry, Xet authorization and cached PyPI hash regressions. Its [fresh full CI run](https://github.com/weaveplatform/artifactweave/actions/runs/41687403546) on `e305003ee681f8c00baf69317115a2997305d2d9` passed every required lane and coverage at **95.0% (74,199 / 78,104 statements)**, with every package at or above 90%. The [retained record](evidence/phase9c-ci-coverage-prerequisite.json) contains the downloaded exact test inventory, all job identities/conclusions and coverage result. Local 94.2% coverage omitted native clients and other operating systems; no historical/local artifact mixture is used for this successful result. GitHub still requires a code-owner approval before the normal merge.

Before closing #810, retain a green full mixed-package run, a full manual run on merged main, and a green run on the current release-please head. Record their exact identities, inventories, jobs, timings, and same-run 95% total / 90% package coverage. [#792](https://github.com/weaveplatform/artifactweave/issues/792) then consumes the final merged-main evidence for the all-format phase ledger.
