# Phase 9 qualification and sign-off

**Status: pending.** This is the evidence ledger for
[#639](https://github.com/weaveplatform/artifactweave/issues/639), not a declaration
that P9 is complete. Feature prerequisites must merge before final qualification.
Passing separate branch runs cannot establish compatibility on the combined tree.

The [retrieval contract](../implementation/remote-retrieval-contract.md) is the
setting-to-behavior inventory. The [definition of done](../implementation/definition-of-done.md)
defines required gates; [CI instructions](ci.md) describe commands and pinned clients.
This ledger changes no coverage threshold, timeout or supported capability.

## Integration order

Recorded on 2026-10-22. PR links are authoritative for later head changes and merge
status. A rebase requires new evidence for its current head.

| Track | PR / issue | Dependency | Remaining sign-off obligation |
|---|---|---|---|
| Terraform modules | [#674](https://github.com/weaveplatform/artifactweave/pull/674), #631 | Merged foundations | Merged as `206c084b`; combined-tree qualification remains |
| Terraform providers / mirror | [#678](https://github.com/weaveplatform/artifactweave/pull/678), #632 | #674 | Merged as `650bfa53`; combined-tree qualification remains |
| Public Terraform provider compatibility | [#702](https://github.com/weaveplatform/artifactweave/pull/702), #701 | #678 and first #638 run | Merged as `13538287`; targeted native public provider passed; complete all-family main rerun remains |
| NuGet v2 / symbols / retention | [#677](https://github.com/weaveplatform/artifactweave/pull/677), #629 | Merged #667 and #670 | Merged as `857a6ad0`; combined-tree qualification remains |
| NuGet remote/cache reindex | [#707](https://github.com/weaveplatform/artifactweave/pull/707), #700 | NuGet remote adapters | Queue-backed cached-package reindex is in draft PR; exact-head combined gate and merge remain |
| Smart Remote | [#675](https://github.com/weaveplatform/artifactweave/pull/675), #634 | Merged foundations | Merged as `5c1e0647`; combined-tree qualification remains |
| Upstream browsing | [#676](https://github.com/weaveplatform/artifactweave/pull/676), #635 | #675 | Merged as `7d57a9ef`; combined-tree qualification remains |
| Permission-safe mapped discovery | [#680](https://github.com/weaveplatform/artifactweave/pull/680), #672 | #676 | Merged as `a86baec1`; combined-tree qualification remains |
| Upstream version / GAVC search | [#695](https://github.com/weaveplatform/artifactweave/pull/695), #213 | #680 | Merged as `da245996`; combined-tree qualification remains |
| Transitive AQL | [#679](https://github.com/weaveplatform/artifactweave/pull/679), #636 | #675 | Merged as `1db584bf`; combined-tree qualification remains |
| Public upstream conformance | [#696](https://github.com/weaveplatform/artifactweave/pull/696), #638 | Relevant adapters | Workflow merged as `3adbb5d2`; [first complete dispatch](https://github.com/weaveplatform/artifactweave/actions/runs/39786506252) failed in Terraform provider; correction and rerun required |
| Full-gate acceptance budget | [#706](https://github.com/weaveplatform/artifactweave/pull/706), #705 | Growing native matrix | Merged as `fe3aca19`; required core/NuGet partitions and same-run merged coverage passed at `0e288665` |
| Phase sign-off | [#639](https://github.com/weaveplatform/artifactweave/issues/639) | Every row above | Combined-tree run, documentation reconciliation and completion review |

All feature prerequisites #674, #675, #676, #677, #678, #679, #680, #695 and #696
are merged. Public-conformance fixes
[#698](https://github.com/weaveplatform/artifactweave/pull/698) and
[#699](https://github.com/weaveplatform/artifactweave/pull/699) are also merged.
The baseline merged main at `02b1710c` contains these features. This pending
ledger is on the sign-off candidate branch and requires rebase after the
prerequisites merge.
The first all-family public workflow ran on that tree and exposed a Terraform provider
failure assigned to #701. Its correction is merged as `13538287`; the full public
rerun remains. #700's NuGet remote/cache reindex and the final combined-tree
quality gate also remain before sign-off.

## Native and contract matrix

Every final-run row needs the tested commit/tree, workflow run, client versions,
scenario/step counts, zero unexplained skips and relevant diagnostics. Native clients
start with empty client caches and direct upstream fallback disabled. HTTP requests,
source contracts and generated repository fixtures are labelled separately.

| Behavior | Evidence to retain on the combined tree | Boundary of the claim |
|---|---|---|
| Shared policy / generic | `generic_remote.feature`, `cache_maintenance.feature`; source/remote and all-store remote-cache contracts | Cold/warm/expired and negative cache, offline/hard fail, no-store, ranges/conditions, maintenance, revocation and source lifetimes |
| Docker / OCI / Helm OCI | `registry_remote.feature`, existing local registry/Helm features, OCI conformance | Native authenticated pulls, verified manifests/blobs, discovery and offline reads; virtual aggregation stays P10 |
| Maven family | `maven_remote.feature`, existing Maven features; layout/companion faults | Native Maven proof; path support alone does not qualify every Gradle/Ivy/SBT client generation |
| npm | `npm_remote.feature`, existing npm client/search features | Scoped/unscoped cold install and offline `ci`; filtered metadata and complete integrity admission |
| Python | `pypi_remote.feature`, existing pip/uv features | HTML/JSON discovery, wheel/sdist installs, metadata sidecars and preserved signatures |
| NuGet | Complete `@nuget`, including `nuget_remote.feature`, `nuget_v2_remote.feature` and `nuget_remote_symbols.feature`; #700 remote/cache reindex | .NET 10/11 and JFrog restore; real PDB bytes and SSQP HTTP proof are distinct from debugger integration; reindex operates only on cached packages |
| Classic Helm | `helm_remote.feature`, existing Helm features | Native signed cold/offline pulls and signature failure; index refresh does not prefetch charts |
| Terraform | Complete `@terraform`, including `terraform_remote.feature` and `terraform_provider_remote.feature`, and JFrog provider fixture | Native module/provider/mirror initialization and verified offline downloads; configuration CRUD is separate content evidence |
| Go source | Remote Go source contracts and `internal/format/gomod` tests | P9 source only; P10 owns native mixed local/remote virtual download/build with no fallback |
| Smart Remote | `smart_remote.feature`, Smart fault/durable/store tests | Discovery, property ownership, download claims, absence/outage and loop bounds; controlled peer is not a vendor capture |
| Browsing / mapped search | `remote_listing.feature`, `remote_search.feature`; layout/source/runtime faults | Cold discovery without binary prefetch, canonical permissions, supported lossless mappings, bounded traversal and final disclosure checks |
| Transitive AQL | `smart_aql.feature`, parser/server/peer faults | Pinned JFrog search and actual transitive download; cold normal-key results stay distinct from real cache aliases |
| Live public upstreams | #638's pinned manifest, scheduled/dispatch workflow and first run | Internet conformance with availability/rate-limit failures reported separately; does not replace deterministic native acceptance |

## Security and integrity review

These findings concern the candidate branches. Repeat affected checks after integration;
this is not an assertion that every future code path has been audited.

| Concern | Review result and regression obligation |
|---|---|
| Layout authority | #680 separates upstream traversal from canonical permission paths. Layout edits atomically change affected source generations. Filename-only scopes require actual root Read rather than a sibling grant. Verify in-memory/SQLite/PostgreSQL rollback, concurrent edits and final disclosure. |
| Descriptor aliases | Remote lookup accepts one upstream candidate whose forward projection equals the canonical request. Alternate `.pom` spellings cannot alias the mapped descriptor. Generic copy/move translation retains its rules. |
| Module extraction | #674 validates the complete virtual link graph before parent traversal. Regressions cover chained symlink escape, cycles, bounded work and hardlink-through-symlink failures while retaining valid internal links. |
| Provider integrity | #678 verifies complete installable bundles. Rotation tests prohibit mixing one metadata revision's hashes with another revision's archive; stale signed locations fail. Large advertised version/platform sets remain lazy. #701 must accept bounded legacy protocol tokens without weakening bundle admission. |
| NuGet admission | #677 retains bounded XML, response-base URL checks, package identity/hashes and complete PDB key checks. Package grants cannot authorize symbol paths; explicit empty feed contexts survive configuration. |
| Credentials / SSRF | Adapters use shared target restrictions, origin-scoped configured credentials, TLS and redirect policy. Confirm current principal, source and credential revisions before network work and final delivery; public URLs must not disclose private upstream credentials or locations. |
| Smart synchronization | Local property ownership survives refresh. Durable statistics claims precede submission and uncertain sends are not replayed. Origin absence requires an item-info miss rather than an outage; diagnostic observations/counters are per HTTP representation. |
| Search disclosure | #676/#680/#213/#679 retain exact current file/folder checks, virtual route restrictions, bounded traversal and final response checks. Failed listings return no partial results. AQL POST is closed to parsed read-only peer queries; search never fabricates cached artifact rows. |

A source-path review on merged `02b1710c` found no concrete blocking defect in the
reviewed remote transport, source/cache, mapped discovery, transitive AQL, NuGet v2
or Terraform provider paths. [`security.go`](../../internal/remote/security.go) pins
DNS answers per outbound hop; the client validates redirects and scopes credential
headers. [`source.go`](../../internal/source/source.go) checks authorization before
network work, and the item cache checks bytes and current authority in its
publication transaction. Mapped listing and transitive AQL recheck canonical
permissions before disclosure. Representative regressions include
`internal/remote/credentials_test.go`, `destination_test.go`, `policy_fault_test.go`,
`internal/source/source_test.go`, `internal/server/nuget/remote_admission_test.go`,
`internal/server/terraform/remote_provider_policy_test.go` and
`_rotation_test.go`, and `internal/server/aql_transitive_peer_test.go`. This
read-only review does not replace exact-head gate, native or live-vendor evidence;
the #700/#701 changes require their own review after integration.

Vendor behaviors inferred from documentation or controlled peers remain `[I]` and
are consolidated in [#216](https://github.com/weaveplatform/artifactweave/issues/216).
P19 must compare real Artifactory responses. Preserve existing ADR decision statuses.

## Recorded branch evidence

These are historical results, not qualification of a later untested head.

- Terraform modules: `ee1dcd33b1da5f1d38a896c3fa9ce2bd1e383345`,
  [quality run 39765943281](https://github.com/weaveplatform/artifactweave/actions/runs/39765943281):
  every required job passed, including all three OSes, native acceptance, six-platform
  build, Terraform, OCI and security. Merged coverage was **96.2%** (44,098 / 45,861),
  with the 90% per-package gate satisfied. Acceptance including setup took **13m37s**.
- Smart Remote: `1a3d5332bd7094c8a9d3bdb50fb3c332e5a5a516` passed **4 scenarios /
  73 steps / zero skips** with JFrog CLI 2.126.0 on macOS. The prior Linux failure assumed
  diagnostic row zero belonged to the latest curl request, although JFrog and curl can
  select different representations. The corrected assertion and race regression preserve
  independent observations; the new Linux gate remains required.
- Smart Remote was rebased after #674 merged: `4017d119` repeats **4 scenarios /
  73 steps / zero skips** on the combined module/Smart tree. Only shared documentation
  conflicted; the runtime and test patches are unchanged.
- Browsing: `f927f58cd8cf86d1c3fa363ce9f9558ca955d4a3` passed **2 scenarios / 56 steps /
  zero skips** for HTML and Smart peer metadata on macOS.
- Transitive AQL: `77242981c34c98b8af2bc4c1bd6de2e5c3e55069` passed **1 scenario /
  29 steps / zero skips** with JFrog CLI 2.126.0 on macOS, including cold search/download
  and cached download while the peer is unavailable.
- Terraform provider public correction: #702 at `df4417dc` passed its
  [targeted live run 39787926963](https://github.com/weaveplatform/artifactweave/actions/runs/39787926963).
  Terraform 1.16.4 completed native cold and warm `init`; versions, download,
  checksum and signature requests returned 200, and both phases matched the pinned
  original ZIP SHA-256 `d2855b922ea345dbd89ea287e4c6c4757e38bc0aaffeb2b79aa0b8004f9c53ff`.
  It made 25 local requests under the 160 ceiling with no assertion retries. This
  targeted branch run does not replace the full default-branch matrix after merge.
- #702's rebased [exact-head gate 39797476639](https://github.com/weaveplatform/artifactweave/actions/runs/39797476639)
  at `20f04f7f` passed both required native acceptance partitions, all three
  OS unit suites, the merged coverage job and aggregate Quality gate after #706
  landed. The coverage log reports **95.7% total (47,253 / 49,361)**, with
  the 95% total and 90% per-package thresholds satisfied. The Windows
  `internal/server` package completed in **625.529s**, below its 15-minute
  package deadline. This branch gate does not replace the final combined
  #700/#701 sign-off gate or a public `family=all` rerun on merged main.
- #706's corrected [exact-head gate 39793369976](https://github.com/weaveplatform/artifactweave/actions/runs/39793369976)
  passed on `0e288665`: required core acceptance **242 scenarios / 5,513 steps**
  in **10m29s** including setup and required NuGet **22 / 682** in **5m07s**.
  Informational Gradle passed **1 / 25** in **2m46s**. All three OS unit
  suites, both required coverage uploads and the aggregate gate succeeded.
  The [merged coverage job](https://github.com/weaveplatform/artifactweave/actions/runs/39793369976/job/119887384844)
  passed **95.7% total (47,253 / 49,361)** and the 90% per-package check
  from eight same-run coverage artifacts. This proves the partition itself;
  the combined #702/#707 sign-off gate still remains.
- #702's first [exact-head full gate 39788012847](https://github.com/weaveplatform/artifactweave/actions/runs/39788012847)
  passed its required native feature step in 13m56s (`make accept` reported 728.365s),
  but the 15-minute job deadline cancelled the following informational Gradle step
  before coverage upload. This run cannot qualify the branch; [#705](https://github.com/weaveplatform/artifactweave/issues/705)
  owns a required-scenario partition without raising the 900-second limit.

Temporary local logs are development evidence, not durable release evidence. Replace
provisional entries with retained GitHub run/artifact links before sign-off.

## First public upstream dispatch (incomplete)

[Run 39786506252](https://github.com/weaveplatform/artifactweave/actions/runs/39786506252)
tested merged `main` at `02b1710c` on 2026-10-22, using the committed immutable pins,
fresh client homes, bounded per-family requests and no assertion retries. Its retained
`public-upstream-outcomes` artifact and [committed report](evidence/phase9-public-39786506252.json)
report **14 of 15 families passed**. Generic,
Maven, Ivy, Gradle, SBT, npm, PyPI and NuGet v2/v3 passed HTTP protocol/digest checks;
these rows do not claim native installation. Native Helm, Docker, OCI, Helm OCI and
Terraform module checks passed. Native Terraform provider `init` failed while listing
version 3.3.2: the local remote repository returned 502 after six local requests.
The live version list includes older releases with a major-only protocol token that
the current parser rejects; [#701](https://github.com/weaveplatform/artifactweave/issues/701)
owns a strict compatibility correction.
An independent diagnostic HEAD to the public upstream returned 200, so this is an
application/fixture failure to investigate, not evidence of an upstream outage.
The failed run does not satisfy #638 or the P9 exit. Retain the correction PR and a
successful complete rerun before changing this ledger's pending status.

## Final qualification procedure

1. Merge prerequisites and update the sign-off branch from main. Record head/tree SHAs
   and verify #213/#672 are included. Reconcile every P9-owned retrieval-contract row.
2. Run the complete `go | Quality gate` on that candidate. The current workflow supports
   `gh workflow run quality-gate.yml --ref <candidate-branch>`. Require all three OS
   race/shuffle suites, store/blob contracts, native acceptance, Terraform, OCI, spec,
   lint/format/generation, vulnerability and six-platform build jobs.
3. Inspect actual native scenario counts and skips, not just the job conclusion. Keep
   acceptance within **900 seconds**; record setup/execution and critical-path costs
   separately, together with pinned clients and fixture inputs.
4. Require that run's merged coverage to reach **95% total / 90% per included package**.
   Never combine profiles from different source revisions to manufacture a pass.
5. Retain #638's first real run and failure classifications. A workflow that cannot be
   dispatched before reaching the default branch keeps its obligation open until run.
6. Reconcile README, plan, operations, research/deviations and capability ownership with
   measured behavior. Add remaining vendor inferences to #216. Only then replace this
   pending status with the qualification record and submit final sign-off for review.
7. Runtime, fixture, workflow, dependency or conflict-resolution changes require new
   qualification of the final candidate. A later evidence-only documentation commit must
   identify itself and prove the tested source tree unchanged; never relabel an older SHA.

[#673](https://github.com/weaveplatform/artifactweave/issues/673) requests affected checks
on implementation PRs and full qualification on release-please PRs. Until implemented,
the existing full gate remains required. Afterwards use its explicit full-run mode here;
selective feature-PR checks cannot establish phase completion.
[#705](https://github.com/weaveplatform/artifactweave/issues/705) is the narrower
acceptance-budget prerequisite exposed by the #702 exact-head gate; it does not close
the broader #673 policy work.

## Handoffs that remain open

| Owner | Retained capability |
|---|---|
| P9b / [#681](https://github.com/weaveplatform/artifactweave/issues/681) | LuaRocks, NVIDIA NIM, Skills, Agent Plugins and Agent Packages for their documented repository classes; native behavior and acceptance remain required, with peer convergence in P13 |
| P10 | Virtual aggregation, native Go virtual resolution and format-specific virtual semantics |
| P11 / [#487](https://github.com/weaveplatform/artifactweave/issues/487) | Project sharing runtime and administration/security behavior; P9's boundary design alone cannot close it |
| P19 / #216 | Live Artifactory observations for inferred metadata, error, synchronization and search behavior |
| P27 / P28 | Security/curation and remaining protocol/client gaps, including expanded module source getters and client generations |
| P29–P33 | Additional package ecosystems assigned in the capability roadmap |
| P35 / P37 | HA/operations and advanced replication/federation/synchronization qualification |
| P41 | Expanded platform/client combination qualification |

The [capability roadmap](../implementation/capability-roadmap.md) also retains enterprise
authentication, Evidence, Lifecycle/release bundles v2 and AppTrust in P21–P27. None becomes
out of scope when P9 closes. Only evidenced supersession permits exclusion.
