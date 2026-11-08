# API reliability implementation review

This is the source review for [#890](https://github.com/weaveplatform/artifactweave/issues/890),
starting at `0d628cbf` and updated with the implementation on this branch. It is
not a live JFrog comparison. Wire uncertainties remain owned by
[#216/P19](https://github.com/weaveplatform/artifactweave/issues/216).
[Decision 0043](../decisions/0043-api-reliability.md) defines the new guarantees,
defaults, persistence, failure modes and administrative recovery contract.

## Operation ledger

[operations.json](operations.json) joins **all seven bundled specifications** with
the tested implementation map and every binding in the production chi router.
It currently contains 1081 distinct method/path bindings: 642 specified operations
and 439 additional handwritten aliases or dispatch bindings. A wildcard binding
represents a protocol dispatcher, not a claim that every URL matching it works.
Some aliases describe the same operation through different route patterns.

Each entry includes the operation ID, served contract, actual implementation
state, handler, direct source calls where a generated operation has a concrete
method, effect/retry classification, incoming cost policy, declared pagination
controls, response collection locations, persistence review family and evidence.
The 383 generated stub operations are explicitly marked 501 with no business
query or effect. Their schemas do not establish implementation. Partial entries
retain unsupported modes and native dispatch limitations; neither repository CRUD
nor a mounted wildcard proves full native support.

`TestAccAPIReliabilityLedger` rebuilds this join and rejects drift. After reviewing
an implementation change, refresh the retained evidence with `make api-ledger`.
This refresh is not a substitute for reviewing semantics: source method presence
does not establish idempotency or bounded execution. The status and principal
matrices separately verify served behavior; `TestAccUnimplementedOperationsAnswer501`
proves the stub classification against the router.

## Effects and retry decisions

| Family | Implemented effects and decision |
| --- | --- |
| Ordinary reads | Detail reads have no intended write effect. Downloads can populate remote caches, increment statistics and perform upstream work. Retry with bounded backoff; budgets still apply. |
| Addressed replacements/deletes | Repeating an item/configuration replacement or deletion can converge on the same state, with different responses. Index hooks, retention, audit and concurrent replacements mean the HTTP verb alone is insufficient proof for every mode. Read/reconcile the addressed resource on ambiguous completion. |
| Build append | Concatenates modules and writes build-info metadata/blob content. Optional keys now bind the same credential, canonical resource and body bytes to one committed effect for 24 hours. [The service](../../../migration/artifacts/core/build/build.go) and [replay store](../../../internal/sqlstore/sqlcommon/replay.go) commit the effect and receipt together. |
| Build publish/delete/promote/rename | Publishing can run retention; deletion can select the latest run; promotion/copy/move act on artifact sets. These are not protected by append's key. Reconcile the selected run and source/destination instead of automatically repeating. See [build service](../../../migration/artifacts/core/build/build.go) and [handlers](../../../internal/server/builds.go). |
| Tokens, refresh and OIDC | Issue fresh credentials or rotate/delete token records. Responses contain secrets and are never retained in the replay store. Unsupported keys are rejected. Restriction checks also run after self-authentication, before issuing/rotating credentials. An ambiguous refresh requires credential reconciliation, not a new blind grant. |
| AQL | [The same POST handler](../../../internal/server/aql.go) serves reads and `items.delete`/`properties.update`. Only proven read queries are read-retriable. Do not infer read safety merely from the search path. |
| Reindex and asynchronous actions | Reindex updates derived indexes and can schedule background work. Existing work-key coalescing is not an HTTP replay receipt. Request admission bounds enqueue work; durable worker concurrency remains a separate control. |
| Multipart and protocol uploads | Multipart/OCI/state protocols retain their own token, part, offset and checksum contracts. Use their status/reconciliation endpoints. Restriction checks fence identified owners on self-authenticating multipart and Terraform state capabilities. |
| CRAN archive move | A GET alias performs a move. It receives expensive admission and must be reconciled on an ambiguous result. |
| Administrative restrictions | Every accepted set/clear appends an audit entry. Repeating a change may add another entry even if current restriction state converges. The recovery reserve still requires administrator authorization. |
| Stubbed products/actions | 501 performs no business operation; it is not a transient failure and should not be retried until support changes. Middleware may still verify credentials and account for admission. |

Keys are intentionally supported only for build append in this delivery. Imports,
replication, webhooks, other fan-out actions and generated product stubs do not
gain a fictional replay guarantee. A timeout/500 is not proof of rollback, and a
422 mismatch can follow a successful earlier request. See the
[generated-client retry example](retrying.md).

## Persistence and collection execution

| Surface | Wire contract and actual execution |
| --- | --- |
| Access v2 users/groups | Default/max 1000; explicit 1–1000. Existing base64 name cursor; ascending/descending strict continuation. `ListSummaries`/`ListNames` apply filters and permission-derived grant checks in SQL, select only needed columns and fetch LIMIT+1. Name indexes support keysets; substring and grant filters can still examine additional candidates. |
| Restriction audit | Default 100/max 1000; descending unique sequence, optional exact-user filter; new cursor binds filter and administrator. SQL `id < cursor ORDER BY id DESC LIMIT ?` with a user/sequence index. Current administration is rechecked each page; no snapshot guarantee. |
| Legacy users/groups, tokens, permissions, projects, roles/settings | Preserve their existing arrays/envelopes and declared pagination. Several handlers/services collect store pages. Do not equate a bounded store Page with bounded total response memory. [Identity service](../../../internal/service/identity/identity.go), [SQL identity](../../../internal/sqlstore/sqlcommon/identity.go), [SQL projects and roles](../../../internal/sqlstore/sqlcommon/operations.go). |
| Repository lists/configurations | Complete legacy arrays with current visibility filtering; batch repository requests retain the existing 100-item cap. `Repos.List` returns the selected collection; no client cursor is introduced. [Handlers](../../../internal/server/repositories.go), [repository service](../../../migration/artifacts/core/repos/repos.go), [SQL](../../../internal/sqlstore/sqlcommon/content.go). |
| Build names/numbers/runs | Complete legacy collections remain; service collection can outgrow an individual store page. Current build/artifact grants determine visible results. [Build service](../../../migration/artifacts/core/build/build.go), [SQL build queries](../../../internal/sqlstore/sqlcommon/builds.go). |
| AQL/search | Offset/limit/order contracts remain. The compiler builds parameterized SQLite/PostgreSQL queries; non-admin results are permission filtered with configured scan/result bounds and query deadlines. Convenience searches read batches and may collect results. [HTTP](../../../internal/server/aql.go), [convenience searches](../../../internal/server/searches.go), [SQL execution](../../../internal/sqlstore/sqlcommon/content_items.go), [decision 0012](../decisions/0012-search-and-aql.md). |
| Storage tree/archive/trash/statistics | Tree listing, archives and aggregate statistics have distinct complete/streaming contracts; they are not replaced with paged envelopes. Shared budgets and existing folder/archive limits bound admission, not the size of every legacy collection. [Storage](../../../internal/server/storage.go), [items](../../../internal/server/items.go), [SQL descendants](../../../internal/sqlstore/sqlcommon/content_items.go). |
| Stub list/search operations | The ledger records declared page controls and array schemas, but their runtime pagination is “schema-only”: no collection query executes. |

For the changed Access queries, retained SQLite/PostgreSQL tests seed 5000 users,
compare first and late pages, inspect plans and cover Unicode/literal substring
filters, reverse order, deleted cursor rows and changing permissions. See
[SQLite tests](../../../internal/sqlstore/sqlite/identity_query_test.go),
[PostgreSQL tests](../../../internal/sqlstore/postgres/identity_query_test.go) and
[shared contracts](../../../pkg/store/storetest/identity_query.go). A cursor is
not a snapshot: later inserts may appear, earlier inserts are not revisited and
newly denied/deleted rows disappear. Filtered keysets are not universally constant
time; only the tested indexed continuation is claimed.

## Native protocols

The complete route binding inventory is in the ledger. Native protocols retain
their established pagination and index formats. Item/package store calls use
parameterized SQL and store keysets, but many index builders consume every page
before serializing a response. This review deliberately does not silently truncate
those responses or label an in-memory slice as a scalable database cursor.

| Native surface | Reviewed execution and paging evidence |
| --- | --- |
| Docker/OCI/OCI Helm | Catalog/tags use `n` and `last` with Link continuation, but names are collected/sorted before slicing. Invalid/absent n returns the remaining collection; zero returns an empty page. Pulls are streams and upload offsets have their own replay/reconciliation rules. [tags.go](../../../migration/artifacts/ecosystem/docker/http/tags.go) |
| npm | Search defaults to 20, accepts from/size and has no new size cap; package-store batches of 500 are filtered before response selection. Packuments/dist-tags are complete documents. [search.go](../../../migration/artifacts/ecosystem/npm/http/search.go) |
| NuGet | Search uses skip/take (default 20, existing maximum in `pageOf`), scans package batches to compute totalHits and authorization-filtered pages. Registration pages hold 128 versions after collecting the package. V2 feed offsets remain protocol offsets. [search.go](../../../migration/artifacts/ecosystem/nuget/http/search.go), [registration.go](../../../migration/artifacts/ecosystem/nuget/http/registration.go) |
| PyPI | Simple project lists and per-project file listings remain complete HTML/JSON representations. Package pages are collected; native metadata and archive size limits remain. [index.go](../../../migration/artifacts/ecosystem/pypi/http/index.go), [simple.go](../../../migration/artifacts/ecosystem/pypi/http/simple.go) |
| Classic Helm | index.yaml remains a complete repository index, including virtual merges and bounded remote metadata reads. A small response page is not claimed. [helm.go](../../../internal/server/helm/helm.go), [remote_index.go](../../../internal/server/helm/remote_index.go) |
| Go modules | Version lists and module files retain their proxy wire format; no generic cursor envelope. [golang](../../../migration/artifacts/ecosystem/golang/http/) |
| Terraform registry/backend | Module/provider version metadata is a native collection; archives/state are streams or singular resources. Backend list semantics remain separate from registry metadata. Signed capabilities recheck identity, permissions and temporary restriction before access. [registry](../../../internal/server/terraform/), [backend](../../../internal/server/terraformbackend/) |
| Ansible | Versions are collected, then offset/limit is applied (default/max 100; offset≤100000). Remote pages are bounded, validated for origin/cycles and collected before local continuation. [ansible.go](../../../internal/server/ansible/ansible.go) |
| Chef | start/items paging slices a collected catalog; items≤1000 and the version collection is capped 4096. Package-store reads use pages; remote traversal has separate limits. [chef.go](../../../internal/server/chef/chef.go), [remote.go](../../../internal/server/chef/remote.go) |
| Puppet | Releases retain offset/limit (default 1000), validate bounds and slice the collected releases. Remote paging is bounded by maxVersions. [puppet.go](../../../internal/server/puppet/puppet.go), [remote.go](../../../internal/server/puppet/remote.go) |
| Hugging Face/Xet | Snapshot metadata collects descendants in store pages of 1000 before building tree/info responses. LFS/Xet transfers are bounded binary/capability operations, not general collection cursors. Opt-in Xet and supported repository modes remain explicit. [read.go](../../../internal/server/huggingface/read.go), [protocol.go](../../../internal/server/huggingface/protocol.go) |
| Git LFS | Lock list/verify has a dedicated bounded cursor implementation with current visibility filtering, including continuation across empty visible pages; SQL orders by path with LIMIT. Batch object transfer and upload/download capabilities are separate operations. [locking.go](../../../internal/server/gitlfs/locking.go), [SQL locks](../../../internal/sqlstore/sqlcommon/gitlfs_locks.go) |
| Conda | repodata/current-repodata, compressed variants and shards are complete format-specific metadata objects. Full metadata reads have a 64 MiB guard; sharded metadata is not a new SQL cursor. [conda.go](../../../internal/server/conda/conda.go), [shards.go](../../../internal/server/conda/shards.go) |
| Bower/CocoaPods | Catalog/spec indexes collect package-store pages; Git/zip source paths retain their protocol ownership. Bower registration is immutable by repository/name: duplicates return native 403, with no Idempotency-Key or response replay; reconcile registry lookup after uncertain completion. [Bower](../../../internal/server/bower/bower.go), [Pods](../../../migration/artifacts/ecosystem/cocoapods/http/pods.go) |
| Nix/Vagrant | Cache metadata, narinfo/NAR files and box metadata/artifacts retain native representations. These are not paged REST collections. [Nix](../../../internal/server/nix/), [Vagrant](../../../internal/server/vagrant/) |
| Cargo/Composer/Gems/Hex/Pub/LuaRocks/Swift | Registry metadata/index documents and downloads retain per-ecosystem formats, limits and authorization. No universal pagination or request deduplication is added. [Cargo](../../../internal/server/cargo/), [Composer](../../../internal/server/composer/), [Gems](../../../internal/server/gems/), [Hex](../../../internal/server/hex/), [Pub](../../../internal/server/pub/), [LuaRocks](../../../internal/server/luarocks/), [Swift](../../../internal/server/swift/) |
| Maven/Debian/RPM/CRAN | Index/metadata artifacts and reindex actions remain complete or streamed formats. CRAN's archive-move GET is an action; Debian snapshots/reindex can mutate indexes. The artifact route is also a native alias. [root format handlers](../../../internal/server/), [formats](../../../internal/format/) |
| Agent Packages/Plugins, AI/JetBrains editor extensions, Bazel modules, Skills, NIM/FrogML | Read metadata/files and explicit publish/transaction/index actions retain their implementation-specific contracts. Route presence does not imply support for every repository class or upstream. [protocol registration](../../../internal/server/server.go), [capability ownership](../../implementation/capability-roadmap.md) |

Native lists still using collection/slice execution are recorded limitations,
not newly “fixed” by #890. Admission bounds their invocation frequency and
simultaneous requests; format-specific execution/index improvements remain
separate compatibility work. The raw ledger repeats the exact served descriptions
and declared parameters so a consumer can distinguish a collection from an array
inside a singular business document.

## Validation and compatibility boundaries

New regression coverage includes concurrent append/response-loss/restart/process
crash, atomic budget refusal, capacity cleanup, principal/project isolation,
encoded static route segments, matrix-parameter repository aliases, expired and
cleared restrictions, audit retention, capability uploads and token refresh
without consuming a denied refresh credential. Five Gherkin scenarios exercise
the built server. The full CI profile is required because middleware/store
interfaces affect every protocol; focused tests alone are not phase qualification.

The chosen optional key, page cap, headers, rejection statuses and recovery API
are explicit extensions/decisions. Draft header syntax and source inspection do
not establish live JFrog compatibility. #216 owns captures; #791/#792 remain
separate recipe and qualification work.
