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
It currently contains 1084 distinct method/path bindings: 643 specified operations
and 441 additional handwritten aliases or dispatch bindings. A wildcard binding
represents a protocol dispatcher, not a claim that every URL matching it works.
Some aliases describe the same operation through different route patterns.

Each entry includes the operation ID, served contract, actual implementation
state, handler, direct source calls where a generated operation has a concrete
method, effect/retry classification, incoming cost policy, declared pagination
controls, response collection locations, persistence review family and evidence.
The 379 stub bindings are explicitly marked 501 with no business query or effect:
363 generated operations, and 10 hand-written operations and 6 of their aliases
that the router mounts with `handwrittenStub`. Their schemas do not establish
implementation. Partial entries retain unsupported modes and native dispatch
limitations; neither repository CRUD nor a mounted wildcard proves full native
support.

A specified operation's `retry`, `admission` and `pagination_execution` are derived
from the classes its spec declares
([decision 0043](../decisions/0043-api-reliability.md#rules-for-every-operation)),
so the ledger states what admission, the key gate and paging read:

- `retry` starts with the `x-idempotency` class: a `natural` operation's
  identifiers, a `none` operation's reason, and for `key` whether the operation
  references the shared `IdempotencyKey` (one that does not yet answers a key
  with 400). The advice that follows is the class's.
- `admission` starts with the `x-cost` class and gives its policy and limits, plus
  each `x-cost-expensive-query` parameter that selects the expensive policy.
- `pagination_execution` gives the `x-pagination` class with its position, size
  and next pointer, then the persistence family's review of how the store executes
  it, since the class describes the wire, not the store. An operation that declares
  its other classes but no `x-pagination` is "not a collection".
- A stub keeps its 501 advice ("501 is not transient", "schema-only") and appends
  its intended idempotency and paging classes; it is admitted like any operation,
  so its `admission` is derived too.

A class not declared yet, or still `TODO(author)` on a draft, leaves that field to
the route heuristics, as do the hand-written aliases and dispatch bindings, which
have no spec operation. The guard `TestAccAPIPrinciples` holds the classes
themselves to the decision's rules
([conventions](../../implementation/api-description-conventions.md#retry-cost-and-paging-classes)).

`TestAccAPIReliabilityLedger` rebuilds this join and rejects drift. After reviewing
an implementation change, refresh the retained evidence with `make api-ledger`.
This refresh is not a substitute for reviewing semantics: source method presence
does not establish idempotency or bounded execution. The status and principal
matrices separately verify served behavior; `TestAccUnimplementedOperationsAnswer501`
proves the stub classification against the router. It calls every generated operation the
ledger records as a stub, and every hand-written operation, as an administrator of a seeded
server. A served operation left out of the implemented table answers its own status and
fails it, where a router without a store would answer 501 for it too; a hand-written
operation fails it when whether it answers `handwrittenStub`'s 501 disagrees with the
ledger. The ledger test fails when an operation it records as served carries
`x-tier: stub` in its spec, and when a hand-written operation it records as a stub does not.

## Effects and retry decisions

| Family | Implemented effects and decision |
| --- | --- |
| Ordinary reads | Detail reads have no intended write effect. Downloads can populate remote caches, increment statistics and perform upstream work. Retry with bounded backoff; budgets still apply. |
| Addressed replacements/deletes | Repeating an item/configuration replacement or deletion can converge on the same state, with different responses. Index hooks, retention, audit and concurrent replacements mean the HTTP verb alone is insufficient proof for every mode. Read/reconcile the addressed resource on ambiguous completion. |
| Build append | Concatenates modules and writes build-info metadata/blob content. Optional keys now bind the same credential, canonical resource and body bytes to one committed effect for 24 hours. [The shared replay](../../../internal/platform/idempotency/run.go) runs [the handler](../../../internal/artifacts/adapters/http/builds.go) and [append's reload](../../../internal/artifacts/adapters/http/idempotency.go) in one transaction with the receipt the [replay store](../../../internal/platform/store/sql/replay.go) keeps. |
| Build publish/delete/promote/rename | Publishing can run retention; deletion selects runs as it runs (the earliest-started of a number first); promotion/copy/move act on artifact sets. Delete in both forms and promotion accept `Idempotency-Key` like append, and a replay answers with the runs deleted or the promotion's messages as committed; without a key, or for publish and rename, reconcile the selected run and source/destination instead of automatically repeating. See [build service](../../../internal/artifacts/core/build/build.go) and [handlers](../../../internal/artifacts/adapters/http/builds.go). |
| Tokens, refresh and OIDC | Issue fresh credentials or rotate/delete token records. Responses contain secrets and are never retained in the replay store. Unsupported keys are rejected. Restriction checks also run after self-authentication, before issuing/rotating credentials. An ambiguous refresh requires credential reconciliation, not a new blind grant. |
| AQL | [The same POST handler](../../../internal/artifacts/adapters/http/aql.go) serves reads and `items.delete`/`properties.update`. Only proven read queries are read-retriable. Do not infer read safety merely from the search path. |
| Reindex and asynchronous actions | Reindex updates derived indexes and can schedule background work. Existing work-key coalescing is not an HTTP replay receipt. Request admission bounds enqueue work; durable worker concurrency remains a separate control. |
| Multipart and protocol uploads | Multipart/OCI/state protocols retain their own token, part, offset and checksum contracts. Use their status/reconciliation endpoints. Restriction checks fence identified owners on self-authenticating multipart and Terraform state capabilities. |
| CRAN archive move | A GET alias performs a move. It receives expensive admission and must be reconciled on an ambiguous result. |
| Administrative restrictions | Every accepted set/clear appends an audit entry. Repeating a change may add another entry even if current restriction state converges. The recovery reserve still requires administrator authorization. |
| Stubbed products/actions | 501 performs no business operation; it is not a transient failure and should not be retried until support changes. Middleware may still verify credentials and account for admission. |

A key is accepted exactly where an operation is declared `x-idempotency: key` and
declares the shared `IdempotencyKey` parameter, as decision 0043's
[Idempotency classes](../decisions/0043-api-reliability.md#idempotency-classes)
set out; today those are build append, build delete in both forms and build
promotion, and no Access operation. [Adding a keyed
operation](retrying.md#adding-a-keyed-operation) lists the steps for the next. Every other operation is
`safe`, `natural` (its request names the target it converges on), or `none` with a
listed reason, and a key sent to it answers 400 before authentication. Imports,
replication, webhook tests and other actions that wait on another service are
`none` with `external-effect`, and token issuance is `none` with
`secret-response`, so none of them gains a fictional replay guarantee. A generated
product stub declares its intended class but accepts no key while it answers 501.
A timeout/500 is not proof of rollback, and a 422 mismatch can follow a successful
earlier request. See the [generated-client retry example](retrying.md).

## Persistence and collection execution

| Surface | Wire contract and actual execution |
| --- | --- |
| Access v2 users/groups | Default/max 1000; explicit 1 to 1000. Existing base64 name cursor; ascending/descending strict continuation. `ListSummaries`/`ListNames` apply filters and permission-derived grant checks in SQL, select only needed columns and fetch LIMIT+1. Name indexes support keysets; substring and grant filters can still examine additional candidates. |
| Restriction audit | Default 100/max 1000; descending unique sequence, optional exact-user filter; new cursor binds filter and administrator. SQL `id < cursor ORDER BY id DESC LIMIT ?` with a user/sequence index. Current administration is rechecked each page; no snapshot guarantee. |
| Access v2 permissions | Default 100/max 99999; the cursor is the previous page's last name in the clear, and a page starts strictly after it. `PermissionStore.Page` seeks on the name primary key and fetches LIMIT+1; a user or group filter is a subquery on `permission_grants`' principal index, so a filtered page reads the filter principals' grant rows and the page's documents, never every permission (#1408). [Handler](../../../internal/access/adapters/http/permissions.go), [service](../../../internal/access/core/permission/page.go), [SQL](../../../internal/access/adapters/sql/permission_page.go). |
| Get Repositories with `limit` | The class, package type and project filters and the caller's visibility (`Decider.VisibleRepos`, the sets `IsRepoVisible` admits) go into the SQL WHERE clause. A COUNT answers `X-Total-Count`, and the window is read by key with LIMIT+1, which decides the `Link: <...>; rel="next"` header. Count and window are two statements, so a concurrent write can make them disagree by one. Without `limit` the whole visible filtered list is written from windows of up to 1000 repositories as they are read, each window continuing after the last key of the one before, so a repository created or removed during the walk shifts no later window (#1409). [Handler](../../../internal/artifacts/adapters/http/repositories.go), [SQL](../../../internal/artifacts/adapters/sql/repo_page.go), [visibility](../../../internal/access/core/authz/authz.go). |
| Legacy users/groups, tokens, permissions, projects, roles/settings | Preserve their existing arrays/envelopes and declared pagination. The users, groups, locked users, permission targets (v1 and v2) and tokens lists are written from store pages as they are read, so the server holds one page however long the list (#1409). Roles, environments, OIDC configurations, key pairs, certificates and the v2 permissions of one user or group still collect; the ledger says why for each. [Identity service](../../../internal/access/core/identity/identity.go), [SQL identity](../../../internal/access/adapters/sql/identity.go), [SQL projects and roles](../../../internal/access/adapters/sql/projects.go), [the streamed writer](../../../internal/platform/httpkit/stream.go). |
| Repository lists/configurations | Complete legacy arrays with current visibility filtering; batch repository requests retain the existing 100-item cap. `Repos.List` returns the selected collection; no client cursor is introduced. [Handlers](../../../internal/artifacts/adapters/http/repositories.go), [repository service](../../../internal/artifacts/core/repos/repos.go), [SQL](../../../internal/artifacts/adapters/sql/content.go). |
| Build names/numbers/runs | Complete legacy collections remain. Build names and a build's numbers are written from store keyset pages as they are read (#1409); a run's files are collected from that one run. Current build/artifact grants determine visible results. [Build service](../../../internal/artifacts/core/build/build.go) and [its pages](../../../internal/artifacts/core/build/pages.go), [SQL build queries](../../../internal/artifacts/adapters/sql/builds.go). |
| AQL/search | Offset/limit/order contracts remain. The compiler builds parameterized SQLite/PostgreSQL queries; non-admin results are permission filtered with configured scan/result bounds and query deadlines. The artifact, property, checksum, bad checksum, creation, dates, usage and pattern searches write their results from batches as they are read (#1409). So does the dependency search, from keyset pages of the checksum's dependencies in run, module and file order (index `build_dependencies_sha1_order`), naming each build once; gavc, versions and build artifacts searches collect, since a merge, a sort or a deduplication needs every result first. [HTTP](../../../internal/artifacts/adapters/http/aql.go), [convenience searches](../../../internal/artifacts/adapters/http/searches.go) and [their paging](../../../internal/artifacts/core/search/search.go), [SQL execution](../../../internal/artifacts/adapters/sql/content_items.go), [decision 0012](../decisions/0012-search-and-aql.md). |
| Storage tree/archive/trash/statistics | Tree listing, archives and aggregate statistics have distinct complete/streaming contracts; they are not replaced with paged envelopes. Every list the storage API answers is written from store pages as they are read (#1409). A repository's file list (`?list`, deep or not) walks each tree the view reads, a virtual repository's members included, in full path byte order (index `items_full_path` for a deep list, name order for a folder's children), and the walks are merged, so the entries keep their uri order and the first readable member's item at a path. Folder information walks each tree's children in name order in turn and lists a name only from the first tree where the caller may read it, the order it had when it was collected. A folder's last-modified view folds the merged walk of everything below it rather than reading it whole, and among equally new items reports the one the collected read met first: the folder itself, then the earlier tree, then the lesser (path, name). The trash can's file list, folder information and last-modified view walk its tree in path order (index `trash_path`), looking ahead past siblings such as `a-b` for the folders `a` that sort before them. Shared budgets and existing folder/archive limits bound admission, not the size of every legacy collection. [Storage](../../../internal/artifacts/adapters/http/storage.go), [items](../../../internal/artifacts/adapters/http/items.go), [SQL descendants](../../../internal/artifacts/adapters/sql/content_items.go), [full path, dependency and trash pages](../../../internal/artifacts/adapters/sql/streamed_lists.go), [trash can walk](../../../internal/artifacts/core/trash/walk.go). |
| Stub list/search operations | The ledger records declared page controls and array schemas, but their runtime pagination is “schema-only”: no collection query executes. |

For the changed Access queries, retained SQLite/PostgreSQL tests seed 5000 users,
compare first and late pages, inspect plans and cover Unicode/literal substring
filters, reverse order, deleted cursor rows and changing permissions. See
[SQLite and PostgreSQL tests](../../../internal/access/adapters/sql/identity_query_test.go) and
[shared contracts](../../../internal/access/adapters/storetest/identity_query.go). A cursor is
not a snapshot: later inserts may appear, earlier inserts are not revisited and
newly denied/deleted rows disappear. Filtered keysets are not universally constant
time; only the tested indexed continuation is claimed.

The lists paged in the store by #1408 have their own 5000-row tests on SQLite
and PostgreSQL, which log timings and plans: the first and a late window of
Get Repositories as an administrator and as a partial reader
([repo_page_test.go](../../../internal/artifacts/adapters/sql/repo_page_test.go)),
and the first and a late page of the Access v2 permissions (every one and one
user's), the group names and the restriction audit
([list_pages_test.go](../../../internal/access/adapters/sql/list_pages_test.go)).
A late unfiltered page seeks through the primary key. Server tests follow each
list's next pointer from the first page to the last and check that every item
is visited once
([repositories_walk_test.go](../../../test/server/artifacts/repositories_walk_test.go),
[permissions_walk_test.go](../../../test/server/access/permissions_walk_test.go)).

## Native protocols

The complete route binding inventory is in the ledger. Native protocols retain
their established pagination and index formats. Item/package store calls use
parameterized SQL and store keysets, but many index builders consume every page
before serializing a response. This review deliberately does not silently truncate
those responses or label an in-memory slice as a scalable database cursor.

| Native surface | Reviewed execution and paging evidence |
| --- | --- |
| Docker/OCI/OCI Helm | Catalog/tags use `n` and `last` with Link continuation, but names are collected/sorted before slicing. Invalid/absent n returns the remaining collection; zero returns an empty page. Pulls are streams and upload offsets have their own replay/reconciliation rules. [tags.go](../../../internal/artifacts/ecosystem/docker/http/tags.go) |
| npm | Search defaults to 20, accepts from/size and has no new size cap. The package store returns candidates in name order (`SearchNpm`, a seek of `packages_name`), each with its versions in one query; the terms narrow the read in the store ([npm_search.go](../../../internal/artifacts/core/port/npm_search.go)). Reads ask for from+size candidates, at most 500, doubling while the page is short. `from` is still counted over what the caller may read, so a late page reads every earlier candidate ([#1425](https://github.com/weaveplatform/artifactweave/issues/1425), open). Packuments/dist-tags are complete documents. [search.go](../../../internal/artifacts/ecosystem/npm/http/search.go), [SQL](../../../internal/artifacts/adapters/sql/npm_search.go) |
| NuGet | Search uses skip/take (default 20, existing maximum in `pageOf`). Search, autocomplete and the V2 feeds read candidates in name order (`SearchNuGet`), each with its versions in one query; terms and an autocomplete's prefix narrow the read in the store, and `FindPackagesById()` reads one package. totalHits, skip and the V2 offsets are still counted over what the caller may read, so totalHits reads every match ([#1425](https://github.com/weaveplatform/artifactweave/issues/1425), open). Registration pages hold 128 versions after collecting the package. V2 feed offsets remain protocol offsets. [search.go](../../../internal/artifacts/ecosystem/nuget/http/search.go), [registration.go](../../../internal/artifacts/ecosystem/nuget/http/registration.go) |
| PyPI | Simple project lists and per-project file listings remain complete HTML/JSON representations. Package pages are collected; native metadata and archive size limits remain. [index.go](../../../internal/artifacts/ecosystem/pypi/http/index.go), [simple.go](../../../internal/artifacts/ecosystem/pypi/http/simple.go) |
| Classic Helm | index.yaml remains a complete repository index, including virtual merges and bounded remote metadata reads. A small response page is not claimed. [helm.go](../../../internal/artifacts/ecosystem/helm/http/helm.go), [remote_index.go](../../../internal/artifacts/ecosystem/helm/http/remote_index.go) |
| Go modules | Version lists and module files retain their proxy wire format; no generic cursor envelope. [golang](../../../internal/artifacts/ecosystem/golang/http/) |
| Terraform registry/backend | Module/provider version metadata is a native collection; archives/state are streams or singular resources. Backend list semantics remain separate from registry metadata. Signed capabilities recheck identity, permissions and temporary restriction before access. [registry](../../../internal/artifacts/ecosystem/terraform/http/), [backend](../../../internal/artifacts/ecosystem/terraformbackend/http/) |
| Ansible | offset/limit (default/max 100; offset≤100000). A local or federated collection counts and orders its versions by their names alone (`PackageVersionNames.VersionNames`, filtered by read permission) and reads only the page's versions' records and stored collections (#1404); reading every version name to count and order the page is [#1426](https://github.com/weaveplatform/artifactweave/issues/1426) (open). Remote and virtual lists still collect their versions before the page; remote pages are bounded, validated for origin/cycles and collected before local continuation. [ansible.go](../../../internal/artifacts/ecosystem/ansible/http/ansible.go) |
| Chef | start/items paging, items≤1000; a cookbook's versions are capped 4096. A local or federated catalog without `q` counts its cookbooks from their version names alone (`PackageVersionNames.VersionNames`) and reads only the highest version's record of each cookbook on the page (#1404); reading every version name to count and order the page is [#1426](https://github.com/weaveplatform/artifactweave/issues/1426) (open). A search, and remote and virtual catalogs, still slice a collected catalog. Package-store reads use pages; remote traversal has separate limits. [catalog.go](../../../internal/artifacts/ecosystem/chef/http/catalog.go), [chef.go](../../../internal/artifacts/ecosystem/chef/http/chef.go), [remote.go](../../../internal/artifacts/ecosystem/chef/http/remote.go) |
| Puppet | offset/limit (default 20, at most 100), bounds validated. A local or federated module's release list, and the whole-repository release and module lists, count and order releases by their version names alone (`PackageVersionNames.VersionNames`, capped by maxVersions) and read only the records of the releases their page answers (#1404); reading every version name to count and order the page is [#1426](https://github.com/weaveplatform/artifactweave/issues/1426) (open). Remote and virtual release lists still collect their releases and slice them; remote paging is bounded by maxVersions. [puppet.go](../../../internal/artifacts/ecosystem/puppet/http/puppet.go), [list.go](../../../internal/artifacts/ecosystem/puppet/http/list.go), [remote.go](../../../internal/artifacts/ecosystem/puppet/http/remote.go) |
| Hugging Face/Xet | Snapshot metadata collects descendants in store pages of 1000 before building tree/info responses. LFS/Xet transfers are bounded binary/capability operations, not general collection cursors. Opt-in Xet and supported repository modes remain explicit. [read.go](../../../internal/artifacts/ecosystem/huggingface/http/read.go), [protocol.go](../../../internal/artifacts/ecosystem/huggingface/http/protocol.go) |
| Git LFS | Lock list/verify has a dedicated bounded cursor implementation with current visibility filtering, including continuation across empty visible pages; SQL orders by path with LIMIT. Batch object transfer and upload/download capabilities are separate operations. [locking.go](../../../internal/artifacts/ecosystem/gitlfs/http/locking.go), [SQL locks](../../../internal/artifacts/adapters/sql/gitlfs_locks.go) |
| Conda | repodata/current-repodata, compressed variants and shards are complete format-specific metadata objects. Full metadata reads have a 64 MiB guard; sharded metadata is not a new SQL cursor. [conda.go](../../../internal/artifacts/ecosystem/conda/http/conda.go), [shards.go](../../../internal/artifacts/ecosystem/conda/http/shards.go) |
| Bower/CocoaPods | Catalog/spec indexes collect package-store pages; Git/zip source paths retain their protocol ownership. Bower registration is immutable by repository/name: duplicates return native 403, with no Idempotency-Key or response replay; reconcile registry lookup after uncertain completion. [Bower](../../../internal/artifacts/ecosystem/bower/http/bower.go), [Pods](../../../internal/artifacts/ecosystem/cocoapods/http/pods.go) |
| Nix/Vagrant | Cache metadata, narinfo/NAR files and box metadata/artifacts retain native representations. These are not paged REST collections. [Nix](../../../internal/artifacts/ecosystem/nix/http/), [Vagrant](../../../internal/artifacts/ecosystem/vagrant/http/) |
| Cargo/Composer/Gems/Hex/Pub/LuaRocks/Swift | Registry metadata/index documents and downloads retain per-ecosystem formats, limits and authorization. No universal pagination or request deduplication is added. [Cargo](../../../internal/artifacts/ecosystem/cargo/http/), [Composer](../../../internal/artifacts/ecosystem/composer/http/), [Gems](../../../internal/artifacts/ecosystem/gems/http/), [Hex](../../../internal/artifacts/ecosystem/hex/http/), [Pub](../../../internal/artifacts/ecosystem/pub/http/), [LuaRocks](../../../internal/artifacts/ecosystem/luarocks/http/), [Swift](../../../internal/artifacts/ecosystem/swift/http/) |
| Maven/Debian/RPM/CRAN | Index/metadata artifacts and reindex actions remain complete or streamed formats. CRAN's archive-move GET is an action; Debian snapshots/reindex can mutate indexes. The artifact route is also a native alias. [root format handlers](../../../internal/artifacts/ecosystem/), [formats](../../../internal/artifacts/core/format/) |
| Agent Packages/Plugins, AI/JetBrains editor extensions, Bazel modules, Skills, NIM/FrogML | Read metadata/files and explicit publish/transaction/index actions retain their implementation-specific contracts. Route presence does not imply support for every repository class or upstream. [protocol registration](../../../internal/app/router.go), [capability ownership](../../implementation/capability-roadmap.md) |

Native lists still using collection/slice execution are recorded limitations,
not newly “fixed” by #890. Admission bounds their invocation frequency and
simultaneous requests; format-specific execution/index improvements remain
separate compatibility work. The raw ledger repeats the exact served descriptions
and declared parameters so a consumer can distinguish a collection from an array
inside a singular business document.

## 7.x evidence for the operation rules

Research for [#1401](https://github.com/weaveplatform/artifactweave/issues/1401)
on 2026-11-12, in the project's order: JFrog's documentation pages fetched as
`.md`, the release notes, the KB, public Jira. The documentation search covered the
1696 pages that the Artifactory, Administration, Integrations, Projects,
Installation, Releases, User Management and Setup sections of
[llms.txt](https://docs.jfrog.com/llms.txt) list.
[Decision 0043](../decisions/0043-api-reliability.md#rules-for-every-operation)
holds the rules built on it. [I] marks what no source settles; each is for
[#216/P19](https://github.com/weaveplatform/artifactweave/issues/216).

**Idempotency keys.** No page, release note or KB article documents
`Idempotency-Key` or any other request deduplication header. Where pages call
something idempotent, they mean one operation or command:
[Delete Bridge](https://docs.jfrog.com/administration/reference/deletebridge.md)
answers 204 "even if bridge does not exist", and
[managing configuration entities](https://docs.jfrog.com/artifactory/docs/managing-configuration-entities.md)
warns that `jf rt repo-create` "is not idempotent" because Artifactory refuses an
existing key. Public Jira has no request for the header;
[RTFACT-31371](https://jfrog.atlassian.net/browse/RTFACT-31371) (open) reports a
409 when two promotions of one Release Bundle v2 overlap, in a pipeline that relies
on promotion converging. So 7.x's retry safety comes from requests that name their
target. [I]: whether 7.x ignores or refuses an `Idempotency-Key` it does not
support; artifactweave refuses it with 400.

**429, 503 and their headers.**

- [AQL performance](https://docs.jfrog.com/artifactory/docs/aql-performance.md),
  "How Do I Configure AQL Rate Limiting?": from 7.84 self-managed Artifactory limits
  how many search and AQL requests run at once, and from 7.146.7 (7.136.0 on SaaS)
  how many one user runs. At either limit, after waiting
  `artifactory.aql.queries.pending.timeout.millis` (10000), it "returns HTTP 429
  Too Many Requests until a query finishes". `artifactory.aql.queries.limit.enabled`
  defaults to `false` from 7.84.16.
  [AQL query execution](https://docs.jfrog.com/artifactory/docs/aql-query-execution.md)
  lists the 429 with "Retry after a brief delay". The KB article
  [Protecting Self-Hosted Artifactory from AQL Issues with Rate Limiting](https://jfrog.com/help/r/artifactory-protecting-self-hosted-artifactory-from-aql-issues-with-rate-limiting)
  (first published 2025-04-21, modified 2026-10-22) describes the same 429 and
  properties. None names a response header. [Decision 0012](../decisions/0012-search-and-aql.md)
  records that artifactweave sets no such limit.
- The [7.125.4 self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases.md)
  (2025-10-30) add "a new rate-limiting mechanism for search APIs" for NuGet, with
  no status, header or limit documented.
- 503 appears only on the JFConnect probes
  ([readiness](https://docs.jfrog.com/administration/reference/checkreadiness.md),
  [liveness](https://docs.jfrog.com/administration/reference/checkliveness.md)) and
  on [Get Cluster Locks](https://docs.jfrog.com/administration/reference/getclusterlocks.md)
  without PostgreSQL advisory locking.
- No page documents `Retry-After`, `RateLimit`, `RateLimit-Policy` or
  `X-RateLimit-*` on a JFrog answer. The other 429s in the reference pages belong to
  MyJFrog's cloud APIs at `my.jfrog.com`
  ([usage data](https://docs.jfrog.com/administration/reference/getusagedata.md),
  [IP allowlist](https://docs.jfrog.com/administration/reference/addiptoallowlist.md)),
  not to self-managed Artifactory. The `rateLimit` block of
  [system.yaml](https://docs.jfrog.com/installation/docs/artifactory-system-yaml.md)
  limits outgoing webhook events, not incoming requests. Public Jira's 429 issues
  concern Docker Hub's upstream limits
  ([RTFACT-23250](https://jfrog.atlassian.net/browse/RTFACT-23250)).
- [I]: the headers and body of 7.x's AQL and NuGet 429s.

**IETF drafts.** On 2026-11-12 the IETF datatracker lists
`draft-ietf-httpapi-idempotency-key-header-08` (2026-10-22, expires 2027-04-25)
and `draft-ietf-httpapi-ratelimit-headers-11` (2026-05-23, expires 2026-11-24) as
the current revisions; both are working-group documents not yet with the IESG.
Neither has a revision after the ones decision 0043 cites.

**List paging.** The reference pages' own OpenAPI definitions, for each list
operation in the seven specs whose page documents paging. The class is the one
decision 0043's [paging classes](../decisions/0043-api-reliability.md#paging-classes-and-the-next-pointer)
give it.

| Operation | 7.x paging | Next pointer 7.x gives | Class |
| --- | --- | --- | --- |
| [Get Repositories](https://docs.jfrog.com/artifactory/reference/getrepositoriesbytypeandproject.md) (`getRepositoriesByTypeAndProject`) | `limit` 1 to 1000 and `offset` 0 or more, `offset` only with `limit`, "introduced in 7.173.x"; with `limit`, `X-Total-Count`, `X-Result-Offset` and `X-Result-Limit` ([#1367](https://github.com/weaveplatform/artifactweave/issues/1367)) | None | `offset` |
| [Access v2 users](https://docs.jfrog.com/administration/reference/getuserlist.md) and [groups](https://docs.jfrog.com/administration/reference/getgrouplist.md) (`getUserList`, `getGroupList`) | `cursor`, `limit` (default 1000) | Envelope `cursor` | `cursor` |
| [Access v2 permissions](https://docs.jfrog.com/administration/reference/getpermissions.md) (`getPermissions`) | `cursor` (a permission name), `limit` (1 to 99,999) | Envelope `cursor` | `cursor` |
| [Projects for a global role](https://docs.jfrog.com/projects/reference/getprojectsforrole.md) (`getProjectsForRole`, stub) | `cursor`, `limit` (default 1000) | Envelope `cursor` | `cursor` |
| [Grid monitoring entities](https://docs.jfrog.com/administration/reference/getmonitoringentitylist.md) (`getMonitoringEntityList`, stub) | `page_token` | Envelope `next_page_token`, empty after the last page | `cursor` |
| [Retention history](https://docs.jfrog.com/artifactory/reference/getretentionoperationshistory.md) and [policy report](https://docs.jfrog.com/artifactory/reference/getconfiguredretentionpolicieslist.md) (stubs) | `offset` (default 0), `limit` (default 100), `order_by`, `direction` | Body `total` only | `offset` |
| [Release bundles](https://docs.jfrog.com/artifactory/reference/getallbundles-1.md) and [their versions](https://docs.jfrog.com/artifactory/reference/getreleasebundlev1versionsbyname.md) (`getAllBundles`, `getReleaseBundleV1VersionsByName`, stubs) | `start_pos`, no size | None | `offset` |
| [SCIM users](https://docs.jfrog.com/administration/reference/getscimusers.md) and [groups](https://docs.jfrog.com/administration/reference/getscimgroups.md) (stubs) | `startIndex`, `count` | Body `totalResults`, `itemsPerPage` | `offset` |
| [Failed federation binary tasks](https://docs.jfrog.com/artifactory/reference/getlistoffailedbinarytasks.md) (stub) | `page` from 1, `limit` (default 1000) | None | `page` |
| [Cleanup policy runs](https://docs.jfrog.com/administration/reference/getcleanuppolicyrunsview.md) (stub) | `page`, `pageSize`, `sortBy` | None | `page` |
| [Distribution edge nodes](https://docs.jfrog.com/artifactory/reference/getdistributionedgenodes.md) (stub) | `limit` only, a cap | None | `complete` |
| [Webhook troubleshooting](https://docs.jfrog.com/integrations/reference/streamwebhooktroubleshooting.md) and [Worker execution history](https://docs.jfrog.com/administration/reference/getworkerexecutionhistory.md) (stubs) | `start` is a Unix-time filter; the webhook list's `count` caps it | None | `complete` |
| [Docker catalog](https://docs.jfrog.com/artifactory/reference/listdockerrepositories.md) and [tags](https://docs.jfrog.com/artifactory/reference/listdockertags.md) | `n`, `last` | `Link`, as the [OCI distribution specification](https://github.com/opencontainers/distribution-spec/blob/main/spec.md#listing-tags) defines it | `native` |
| [Puppet modules](https://docs.jfrog.com/artifactory/reference/getpuppetmodules.md) and releases | No parameter on the page; the Forge v3 fields `offset`, `limit`, `total` and `next` in the body, at the top level in the reference schema and under `pagination` in the Forge's shape that artifactweave answers (`api/parity-baseline.json` records the difference) | Body `next` | `native` |
| [AQL](https://docs.jfrog.com/artifactory/docs/aql-query-output.md) (`searchAql`), "Display Limits and Pagination" | `.offset()` and `.limit()` in the query; `range` in the response | None: the next request needs a body | `native` |
| Every other list in the ledger: legacy users, groups and permission targets, builds and runs, tokens, roles, environments, the convenience searches, certificates, key pairs, VCS branches and tags | None | None | `complete` |

Public Jira shows that 7.x left those last lists whole:
[RTFACT-27073](https://jfrog.atlassian.net/browse/RTFACT-27073) (Get Users slow
with 18,000 users, closed as deferred),
[RTFACT-30933](https://jfrog.atlassian.net/browse/RTFACT-30933) (group members,
open) and [RTFACT-29669](https://jfrog.atlassian.net/browse/RTFACT-29669) (SCIM,
open). [I]: whether 7.x's `complete` lists stream or build in memory, and what they
answer when the store fails part way.

artifactweave writes a `complete` list from store pages wherever that leaves the
bytes as they were (#1409, decision 0043, "Streaming complete lists"). Each
operation's `pagination_execution` in the ledger says "streamed from store pages"
or "collected before it is written" with the reason, and the ledger test fails a
served `complete` operation that collects without one.

## Validation and compatibility boundaries

New regression coverage includes concurrent append/response-loss/restart/process
crash, atomic budget refusal, capacity cleanup, principal/project isolation,
encoded static route segments, matrix-parameter repository aliases, expired and
cleared restrictions, audit retention, capability uploads and token refresh
without consuming a denied refresh credential. Six Gherkin scenarios exercise
the built server; the one in
[api_principles.feature](../../../test/acceptance/features/api_principles.feature)
retries a dropped answer with its key, walks a list by its next links as a reader
of part of it, and spends an allowance to its 429. The full CI profile is required because middleware/store
interfaces affect every protocol; focused tests alone are not phase qualification.

The chosen optional key, page cap, headers, rejection statuses and recovery API
are explicit extensions/decisions. Draft header syntax and source inspection do
not establish live JFrog compatibility. #216 owns captures; #791/#792 remain
separate recipe and qualification work.
