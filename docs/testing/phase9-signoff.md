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
| Terraform modules | [#674](https://github.com/weaveplatform/artifactweave/pull/674), #631 | Merged foundations | Merge, then combined-tree qualification |
| Terraform providers / mirror | [#678](https://github.com/weaveplatform/artifactweave/pull/678), #632 | #674 | Current full gate and merge |
| NuGet v2 / symbols / retention | [#677](https://github.com/weaveplatform/artifactweave/pull/677), #629 | Merged #667 and #670 | Current full gate and merge |
| Smart Remote | [#675](https://github.com/weaveplatform/artifactweave/pull/675), #634 | Merged foundations | Full gate after assertion correction, merge |
| Upstream browsing | [#676](https://github.com/weaveplatform/artifactweave/pull/676), #635 | #675 | Current full gate and merge |
| Permission-safe mapped discovery | [#680](https://github.com/weaveplatform/artifactweave/pull/680), #672 | #676 | All-store and mapped native evidence, full gate, merge |
| Upstream version / GAVC search | [#213](https://github.com/weaveplatform/artifactweave/issues/213) | #680 | Mapped integration, native searches, full gate, merge |
| Transitive AQL | [#679](https://github.com/weaveplatform/artifactweave/pull/679), #636 | #675 | Current full gate and merge |
| Public upstream conformance | [#638](https://github.com/weaveplatform/artifactweave/issues/638) | Relevant adapters | Workflow, first real dispatch and retained evidence |
| Phase sign-off | [#639](https://github.com/weaveplatform/artifactweave/issues/639) | Every row above | Combined-tree run, documentation reconciliation and completion review |

#674, #677 and #675 can enter independently once their gates pass. #678 follows
#674; #676 and #679 follow #675; #680 follows #676; #213 follows #680. Rebase each
child onto main after its prerequisite merges and qualify its new head. Sign-off is last.

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
| NuGet | Complete `@nuget`, including `nuget_remote.feature` and `nuget_remote_symbols.feature` | .NET 10/11 and JFrog restore; real PDB bytes and SSQP HTTP proof are distinct from debugger integration |
| Classic Helm | `helm_remote.feature`, existing Helm features | Native signed cold/offline pulls and signature failure; index refresh does not prefetch charts |
| Terraform | Complete `@terraform`, including `terraform_remote.feature`, and JFrog provider fixture | Native module/provider/mirror initialization and verified offline downloads; configuration CRUD is separate content evidence |
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
| Provider integrity | #678 verifies complete installable bundles. Rotation tests prohibit mixing one metadata revision's hashes with another revision's archive; stale signed locations fail. Large advertised version/platform sets remain lazy. |
| NuGet admission | #677 retains bounded XML, response-base URL checks, package identity/hashes and complete PDB key checks. Package grants cannot authorize symbol paths; explicit empty feed contexts survive configuration. |
| Credentials / SSRF | Adapters use shared target restrictions, origin-scoped configured credentials, TLS and redirect policy. Confirm current principal, source and credential revisions before network work and final delivery; public URLs must not disclose private upstream credentials or locations. |
| Smart synchronization | Local property ownership survives refresh. Durable statistics claims precede submission and uncertain sends are not replayed. Origin absence requires an item-info miss rather than an outage; diagnostic observations/counters are per HTTP representation. |
| Search disclosure | #676/#680/#213/#679 retain exact current file/folder checks, virtual route restrictions, bounded traversal and final response checks. Failed listings return no partial results. AQL POST is closed to parsed read-only peer queries; search never fabricates cached artifact rows. |

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
- Browsing: `8796def282e67fdefbf3aa92cb563f50f8667101` passed **2 scenarios / 56 steps /
  zero skips** for HTML and Smart peer metadata on macOS.
- Transitive AQL: `77242981c34c98b8af2bc4c1bd6de2e5c3e55069` passed **1 scenario /
  29 steps / zero skips** with JFrog CLI 2.126.0 on macOS, including cold search/download
  and cached download while the peer is unavailable.

Temporary local logs are development evidence, not durable release evidence. Replace
provisional entries with retained GitHub run/artifact links before sign-off.

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

## Handoffs that remain open

| Owner | Retained capability |
|---|---|
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
