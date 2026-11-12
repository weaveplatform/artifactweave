# 0032: Package format handlers

Status: Accepted

Checked against: **Artifactory 7.161.32** on 2026-10-17 ([research 24 section 6](../24-package-format-handlers.md#6-artifactory-7x-check), [#341](https://github.com/weaveplatform/artifactweave/issues/341)); the download policy rules against 7.173.18 (SaaS 7.177.2) on 2026-11-11 ([#1251](https://github.com/weaveplatform/artifactweave/issues/1251)), as [the 7.x check](#artifactory-7x-check) records.

## Context

npm, Go and Maven (P7), then PyPI, NuGet, Helm and Terraform (P8), each react to what is
deployed. They index packages and versions, write derived files, refuse what their layout
forbids, resolve special download paths, and answer their client's protocol. The original
framework P6 built for Docker was too thin for that ([research 24](../24-package-format-handlers.md)
section 2):
- its hooks could not read content or write items;
- a format's error became a 500;
- nothing ran before a deploy, on a download or on a property change;
- there was no reindex contract;
- `/artifactory/api/<type>/<repo>/` answered 404 for every type but Docker.

The hook environment and protocol kit below now support all local package formats.

Artifactory hangs the same work off one storage-interceptor contract, with callbacks before
and after create, delete, copy, move and property changes, and a `before` callback that can
refuse the operation (research 24 section 1).

## Decision

**Formats plug in through hooks, with optional interfaces.** Every write reaches a format
through the item service, whichever API carried it: `mvn deploy`, `jf rt u`, `jf npm publish`
over REST, a copy or a move. Indexing is therefore triggered by the item that changed, not by
the protocol that changed it.

**The hook environment.** Hooks receive a `format.Env` instead of a bare store:
- `Store`, as today;
- `Blobs`, read-only, so a format can read what was deployed;
- `Now`, the clock;
- `Layouts`, the repository layouts with their resolved tokens, cached;
- `Derive`, which writes a derived item (content plus item) inside the current transaction
  *without* re-entering the hooks. The item service implements it, so formats need not import
  `item`;
- `Underive`, which removes a derived item the same way, past the trash can (#279);
- `Enqueue`, the persistent work queue's enqueue (decision
  [0033](0033-persistent-work-queue.md)), usable inside the current transaction;
- `Move`, a move of one file within a repository as the REST move makes it, with every
  hook, for a format's work, such as npm's strict-layout conversion (decision
  [0034](0034-npm.md)). It never lands over an item in the way.

**Required hooks** take `Env`: `Deployed` after a file is stored,
`Deleting` before each file is removed, and `Deleted` once after a delete or a move.

**Optional interfaces.** A format implements only what it needs. Each is named here by
its method; the Go interfaces are `Inspector`, `Admitter`, `Resolver`, `Annotator`,
`Reindexer` and `Classifier` (#267), `Worker` (#279) and `SidecarKeeper` (#533).

| Interface | Runs | Does | First used by |
|---|---|---|---|
| `Inspect` | After the content is committed to the blob store, **before** the write transaction | Parses the content (an archive entry, a manifest) into what the hooks need, so no decompression holds the database's write lock | npm, Maven |
| `Admit` | Before a deploy, a checksum upload, and the target of a copy or move | Rewrites the target path, adds properties, or refuses with a `format.RejectionError` | Maven (snapshot naming and policy), every type (include and exclude patterns, blackout) |
| `GuardTransferSource` | Before destination inspection of every copied or moved file, including folder descendants | Refuses source content that cannot safely leave its format; rejection skips and reports that file, while other errors abort planning | Conda generated CEP-16 documents (#777) |
| `Resolve` | Before a download is served (GET and HEAD) | Rewrites the requested path, such as a version token or a `-SNAPSHOT` name | Maven |
| `Annotated` | After an item's properties change | Updates the index or queues derived work | npm (`npm.disttag`) |
| `Reindexer` | On a JFrog reindex operation, and for maintenance | Rebuilds the format's index for a repository from the item tree | Docker (adopting `docker.Reindex`), npm, Maven, P8 |
| `Classifier` | When asked | Says whether a path is a unique binary, metadata, or a derived item | P9 cache expiry, P11 replication, P13 federation |
| `Work` | From the work queue, and before a download that `Owed` says waits for it | Runs the derived work the format queued, outside any transaction | Maven (#279) |
| `Sidecars` | On a delete, copy, move or trash restore of a file | Names the sidecars that go with the file, which the item service then carries | Helm's `.prov` (#533) |

`Annotated` was added after the research: Artifactory's npm metadata follows property
changes (`NpmMetadataInterceptor`, research 24 fact 1.2), and P7's npm dist-tags are
properties.

**Format errors.** A format refuses with `format.RejectionError{Status, Message}`. The item
kit's `ItemError` maps it to that status, in the envelope of the surface the request arrived
on. Formats still never import the router in `internal/app`.

**Source transfer restrictions** (amended 2026-10-28, #777). The optional
`TransferSourceGuard` belongs to the source format; target `Inspect` and `Admit`
continue to govern destination admission. Conda refuses copying or moving stored
CEP-16 documents because their records require native per-package filtering.
Generic downloads and folder archives exclude those documents, and checksum
deployment does not count them as readable content. Ordinary package transfers
are unaffected.

**Writes taken without being made** (amended 2026-10-17, #278). Artifactory answers some
uploads of what it computes itself with a success status and stores nothing: Maven's
snapshot-level `maven-metadata.xml` (202) and every metadata checksum (200). `Admit` returns
`format.AcceptedError{Status}` for those, on a deploy or a checksum upload only; the HTTP layer
answers the status with an empty body, and an exploded archive leaves the entry out. A
format that implements `format.SidecarOwner` takes every upload named as a checksum sidecar
as a checksum of its file, so `Admit` sees an `OpChecksum` (with the sidecar's declared
length in `Write.Size`) even when the file is missing, and a missing file answers 404 "Target
file to set checksum on doesn't exist", as in Artifactory. Elsewhere a sidecar name without
its file is still stored as a file of its own.

**Sidecars a format keeps** (amended 2026-10-20, #533). Some files are of use only beside
another, such as a Helm chart's provenance file, which `helm` fetches at the chart's URL plus
`.prov` (decision [0039](0039-helm.md)). A format that implements `format.SidecarKeeper` names
the sidecars of a file, and the item service keeps them with it, whichever API carried the
write:
- a delete of the file deletes its sidecars in the same operation, into the trash can at the
  same moment and by the same caller;
- a copy or move of the file takes each sidecar to its sidecar path beside the file's target,
  admitted and checked as the file is. When a sidecar cannot land, the file does not go either,
  and both are reported. A sidecar the operation names itself, as a folder's copy does, is
  carried once;
- a trash restore of the file restores the sidecars the same delete removed, beside the
  file's destination, and asks the restore's permit about them too.

On its own a sidecar is an ordinary file: deleting, copying or moving it touches nothing else,
and a deploy over its owner leaves it. It is never a version file. This is not
`SidecarOwner`, which is about checksum uploads.

**Generic deploy policy.** `includesPattern`, `excludesPattern` and `blackedOut` are checked
for every package type through `Admit`, on the generic handler that every format embeds.

**Generic download policy** (amended 2026-10-17, #328, and 2026-11-11, #1237, #1251 and
#1253). Artifactory 6.20 refuses a download before it looks for the item, whether or not the
item exists, and for administrators too (`DbLocalRepo.checkDownloadIsAllowed` calls
`RealRepoBase.assertValidPath` with `downloadRequest` set). `generic.CheckDownload` does the
same, and the [download policy rules](#download-policy-rules-for-the-native-protocols-2026-11-11-1251)
R1 to R14 apply it to every native protocol:
- **A blacked-out repository** answers 404 with `BlackedOutException`'s message, as a deploy
  does, whatever its package type (R9).
- **A path the patterns reject** answers 404 with `IncludeExcludeException`'s message, where a
  deploy answers 409. The patterns never refuse a Maven index file or a path ending `.index`;
  only the blackout does (R13).
- **The path checked is Artifactory's:** the archive of an archive entry, which its request
  parsing splits off (`ArtifactoryRequestBase.zipResourcePath`), and the file a checksum
  sidecar describes (`RepoBase.accepts`). A path is a folder when the request ends in a slash
  (`RepoPathImpl`). The root is always accepted.
- **Read permission is checked first,** where Artifactory checks it after, so a caller who may
  not read the path gets 401 or 403 and learns nothing of the repository's settings (DV-057).
- **A virtual repository** passes over a member that refuses, as it passes over one without
  the item, and answers "Could not find resource" when none serves it
  (`VirtualRepoDownloadStrategy.processStandard`). Its own patterns rejecting the path leave
  it no member to search: 404 "No repository found to serve the request for `<key>:<path>`".
  It has no `blackedOut` (`RealRepoDescriptor` carries it), so the flag is ignored there.

Where the policy applies:
- GET and HEAD of items, checksum sidecars, archive entries and folder listings. Listings
  also leave out children the patterns reject, the member's and the virtual repository's
  (`TreeBrowsingCriteriaBuilder.applyRepoIncludeExclude`,
  `RepositoryBrowsingServiceImpl.virtualRepoAccepts`).
- The folder archive download, `GET /api/archive/download/{repoKey}/{path}` (#1253). 7.x
  refuses a blacked-out repository's archive
  ([RTFACT-30253](https://jfrog.atlassian.net/browse/RTFACT-30253), fixed in 7.68.14 and
  7.71.11; [RTDEV-38679](https://jfrog.atlassian.net/browse/RTDEV-38679), SaaS 7.80.0). The
  policy is checked after read permission, the virtual repository's 400 and the 403 "Folder
  download is disabled", and before anything is listed, and a folder the patterns reject
  answers as the item route does [I]. A file the patterns reject inside an accepted folder is
  left out of the archive, as a folder listing leaves it out, and does not count towards the
  download limits [I].
- The storage API's file list (`?list`) of a blacked-out repository
  (`ArtifactResource.writeStreamingFileList` maps `BlackedOutException` to 404).
- The read paths of later formats, which find items through the same download lookup or call
  `generic.CheckDownload` themselves (#269).
- Docker, OCI and Helm OCI pulls and listings under `/v2` (R11), and Maven's version tokens
  and a remote repository's `maven-metadata.xml` (R14).
- The native package protocols, through the router kit (`pkgroute`, #1237, #1251). The file a
  download serves is found through `Kit.DownloadItem`, `ResolveDownloadItem`, `ServeItem`,
  `ServeFound` or `Read`, which check the path they are given before they look for it, so
  every protocol that finds its files there inherits the policy, as remote reads inherit it
  from the source boundary (R4). A protocol that reads a stored blob itself calls
  `Kit.CheckDownload`, as Bower's archive of a version's files does. The refusal is
  `kernel.ErrNotFound` to the protocol, so a virtual repository's search passes over a member
  that refuses, a remote member included, and `pkgroute.IsRefusal` tells it from a missing
  item (R6). A member request checks the virtual repository's own patterns first, for a remote
  member's file too. Metadata has its own helpers: `Kit.StoredItem`, `ResolveStoredItem` and
  `ReadMetadata` find what a listing names without any policy (R3), `Kit.CheckDocument`
  refuses a document at its 7.x path (R1), `Kit.CheckBlackedOut` refuses a blacked-out
  repository's computed resolution metadata (R2a), and `Kit.MergeMembers` leaves a refusing
  member out of a virtual merge (R5).

Where it does not apply, as in 6.20:
- The storage API's item info and its other views describe the item: `ArtifactResource`
  checks read permission alone.
- AQL and the search APIs do not hide a blacked-out repository's items: `AqlRestResult.canRead`
  and `SearcherBase.isResultAcceptable` check permissions and patterns only. Both already hide
  what the patterns reject from everyone but administrators (research 22 section 1.3), where
  Artifactory's search APIs also show it to a caller who may annotate it (DV-058).
- The native protocols' searches keep their answers under a blackout, for the same reason
  (R2b).

Research 24 section 5 (5.4 to 5.8) lists what stays unconfirmed until P19 (#216).

**Remote authority (P9 clarification, #613).** The
[remote retrieval contract](../../implementation/remote-retrieval-contract.md) separates
raw upstream metadata and its validators from rewritten client representations and
locally derived indexes. A remote index can list uncached packages; local reindex must
not overwrite it with the cached subset. `EnsureFresh` below governs locally derived
work, not upstream TTL. Shared retrieval owns bounded synchronous refresh, metadata-only
zap, cache aliases with zero upstream calls, status/fallback policy and no-store streams.
Format adapters #623–#633 classify mutable metadata, unique bytes and refreshable
non-unique payloads, preserving signed bytes and using the existing base-URL resolver.

**CocoaPods Git sources** (2026-10-27, #771) use a closed Git checkout descriptor
on the source request. The remote policy obtains smart HTTP(S) refs and packs
through its existing DNS, TLS, origin-credential and current-authority checks;
it never launches Git or exposes an upstream Git URL to the client. Root and
recursive submodule URLs must match the repository's external dependency policy.
Object IDs, pack/delta expansion limits and archive members are validated before
one deterministic source archive enters the ordinary cache. Gitlinks pin submodule
commits; the cache variant binds the root URL, revision selectors and submodule
option. CDN metadata remains the authoritative package index. The
[CocoaPods guide](../../testing/cocoapods.md) records the bounded checkout contract
and cold/offline native-client evidence.

Native archive admission validates the physical ZIP entry layout as well as its
central directory through `internal/platform/archivecheck`, so the server and client
extractors cannot see different filenames or hidden entries. Tar limits count
the full decompressed stream, including PAX/GNU metadata that `tar.Reader` consumes
internally. These checks apply to both uploaded and remotely cached pod content.

**Everything indexed is rebuildable from items.** What a format indexes, and every item it
derives, follows from the items, their content and their properties alone, so a reindex, a
federation member (decision [0022](0022-federated-repositories.md)) and a replication target
reach the same result. State a protocol request carries, such as a dist-tag in an npm publish
body, is stored as a property, not kept aside. A derived item is classified as derived, so
replication and federation skip it and recompute it.

**Vagrant boxes** (2026-10-27, #785) apply this contract to property-based identity:
`box_name`, `box_version` and `box_provider` identify an archive at any ordinary `.box`
item path. `Inspect` validates the archive before the write transaction; protected
properties bind the provider and optional architecture to its actual SHA-256. The
package index follows deploy, property edits, copy, move, delete and restore. Reindex
revalidates archive bytes and rebuilds these records. Metadata is computed on read,
filters each archive by the current principal and repository patterns, and links to
the existing item download route. The [client guide](../../testing/vagrant.md) records
the local and one-node federated scope and native evidence.

**Which derived work goes through the work queue** (decision 0033; amended 2026-10-20, #533):
- **Through the queue, one kind per format and kind of work:**
  - keyed by package: Maven metadata at the artifact and plugin levels, and at the snapshot
    level; Maven and NuGet `maxUniqueSnapshots` cleanup;
  - keyed by repository: Helm's `index.yaml`, since one file lists every chart in the
    repository (decision [0039](0039-helm.md)), and NuGet's extracted PDB symbols, whose
    ownership can span packages (decision [0038](0038-nuget.md)).

  A read of derived data whose package or repository is still queued calls `EnsureFresh`
  first, so a client never reads stale metadata.
- **Not queued, because the deploy keeps them current or they are computed on read:**
  - npm packuments and search, which read the package index and the per-version metadata
    cache;
  - NuGet's registration pages (P8), which read the package index and the per-version
    documents (decision [0038](0038-nuget.md));
  - Go's version list and `@latest`;
  - the assignment of a unique snapshot's build number, which `Admit` reads from the package
    index during the upload;
  - Docker tag retention, which runs inline on the pushed image (decision 0031).

**Checksums.** Stored content carries four checksums: SHA-256, SHA-512, SHA-1 and MD5,
computed in one streaming pass on every upload path (#296). `.sha512` files are served from
stored metadata and checked on upload under the checksum policy, as the others are. The APIs
expose SHA-512 only where Artifactory's do. No SHA-3 is added: no client or Artifactory API
uses one.

**The protocol router kit.** Formats with a protocol of their own (npm and Go in P7, then
PyPI, NuGet, Helm and Terraform) mount through one kit,
`internal/artifacts/adapters/http/pkgroute`, as Docker's registry does today (decision
0031):
- `Mount` claims `/artifactory/api/<type>/{repoKey}/...` before the generated servers, and
  leaves the JFrog operations at fixed literals beside it (such as `.../reindex`) to them.
- Repository lookup by package type, with a class gate through a `Source` seam. The `Source`
  answers for local content, an upstream in P9 (decision
  [0007](0007-remote-repositories.md)) and a merge of members in P10 (decision
  [0008](0008-virtual-repositories.md)).
- `Require` checks permissions and answers in the format's envelope.
- A `ServeItem` callback reuses the item download path, so redirects (decision 0029) and
  download counts apply.
- A deploy helper with the item routes' matrix and checksum parsing exported, answering 201
  with the `X-Checksum-Sha1`, `-Sha256` and `-Md5` headers.
- Raw-path parsing for encoded slashes (`@scope%2fname`).
- For P8 (amended 2026-10-20, [#532](https://github.com/weaveplatform/artifactweave/issues/532)):
  - **Encoded keys.** A protocol's `Resolve` finds the key elsewhere in the path. It returns
    the candidates in order, each with the path the protocol's routes then match, and the
    first that names a repository wins, compared without regard to case. Terraform splits
    `<repo>__<ns>` and `<repo>-rt-ns-<ns>` at each separator from the left (decision
    [0040](0040-terraform.md)). The type check and the class gate answer as for any mount.
  - **Host-root routes**, such as Terraform's `/.well-known/terraform.json`, mounted with the
    protocol's own and answering other methods 405 in its envelope. One declared
    unauthenticated is served before authentication and reads no credentials.
  - **Multipart uploads.** `ReadForm` reads twine's upload and NuGet's push as they stream: the
    chosen file part goes to the blob store under the repository's checksum policy, with no
    size limit but the deploy path's, which has none, and up to 10 MiB of the other fields
    are kept. It answers 415 for another media type, 400 for a malformed or broken-off body
    or a missing part, and 413 for fields over the limit.
  - **A challenge.** A protocol's `Challenge` answers an anonymous request to a repository 401
    with the Basic challenge before routing, which NuGet's force authentication needs
    (decision [0038](0038-nuget.md)).

Maven needs no protocol router: it is served by the item routes plus the `calculateMetadata`
and `generatePom` operations.

**Test tables.** The status table, the route × principal matrix and their coverage guard
split into one file per format, with fixture repositories of each type. A format's pull
request then touches only its own files.

**Remote and virtual repositories before P9 and P10.** npm, Go, PyPI, NuGet, classic Helm
and Terraform answer 501 in their own envelope, naming the repository class
(decision [0003](0003-repository-model.md)). Docker keeps 405 `UNSUPPORTED` (decision 0031). Maven, served by the item routes, keeps the generic
behaviour: a remote refuses deploys and a virtual deploys to its default deployment
repository.

## Rationale

The hook model mirrors Artifactory's interceptors (research 24 section 1), which is how
Artifactory keeps format behaviour the same whichever API wrote the item.

**The download policy** follows 7.x's documentation and issue tracker where they speak, and
Artifactory 6.20's source elsewhere, with two departures:
- **Permission before configuration.** Artifactory tells any caller that a repository is
  blacked out or that a path is excluded; checking read permission first answers a caller
  who may not read the path as every other refusal does (DV-057).
- **One visibility rule for searches.** artifactweave answers the search APIs from the AQL
  engine (decision [0012](0012-search-and-aql.md)), so they hide what AQL hides; Artifactory's
  search APIs filter their results apart from AQL and exempt a caller who may annotate
  (DV-058).

**Alternatives considered:**
- *Per-format write services that the protocol handlers call:* every format would also need
  to catch writes from `jf rt u`, copy and move, and the generic deploy routes. Hooks on the
  item service catch them once.
- *Synchronous derived metadata in the write transaction:* rejected in decision 0033 for
  contention and SQLite's single writer.
- *Reading content inside the transaction:* a large tarball would hold every other writer;
  hence `Inspect` before it.

## Artifactory 7.x check

The decision stands against 7.161.32
([research 24 section 6](../24-package-format-handlers.md#6-artifactory-7x-check)).
- **The hooks and the generic deploy policy** are artifactweave's design, and 6.20's
  interceptors only informed them.
- **The download policy** (amended for #328, #1237, #1251 and #1253) rests on 7.x's
  documentation, knowledge base, release notes and issue tracker where they speak, read on
  2026-11-11 against 7.173.18 (SaaS 7.177.2), and on 6.20's source elsewhere. Each
  [download policy rule](#download-policy-rules-for-the-native-protocols-2026-11-11-1251)
  names its sources and marks with [I] every answer that none shows; #216 lists those for
  P19.
- **The search filter.** DV-058 hides pattern-rejected items from search results as AQL does,
  and 7.146.7 moved AQL to that rule for non-admins.

## Constraints

- `Derive` writes without hooks. A format must not derive an item that another format would
  need to index.
- A rolled-back transaction can leave content that `Inspect` committed to the blob store;
  garbage collection already reaps unreferenced content (decision
  [0010](0010-retention-trash-and-gc.md)).
- What Artifactory does in research 24 section 5 is unconfirmed until the live comparison
  (P19, #216).

## Owned follow-up

P28 extends the existing formats and P29–P33 add 28 more native package types;
release bundles remain P15. New handlers use the same lifecycle, derived-work,
permission and reconstruction contracts. P9/P10/P13 provide source adapters and
peer behavior where the type permits them. Each format phase must close its native
protocol and client exits; falling back to generic storage is the present boundary,
not a permanent support decision.

The [capability roadmap](../../implementation/capability-roadmap.md) records the
phase boundaries under decision [0042](0042-capability-scope-and-supersession.md).
These assignments describe planned work, not current runtime support.

## Verification

- `internal/artifacts/core/formattest` runs every format against the same contract on the
  in-memory, SQLite and PostgreSQL stores: the index is right after deploy, overwrite, delete, copy,
  move and restore, and a reindex reproduces it (#268). Two options of the fixture cover P8
  (#533):
  - `Format.RepoWork` lists the kinds a format keys by repository. Every write that changes a
    repository's versions (a deploy, an overwrite, a copy's target, a move's two repositories,
    a delete and a restore) must leave exactly one item of each, keyed by the repository's
    key, and none for a repository it left alone. `Format.Work` keeps the kinds keyed by
    package, one item per package after a deploy, as for npm, Go and Maven.
  - `File.SidecarOf` marks a file as a sidecar its format keeps. It must never be linked to
    a version, and must follow its owner through a delete, a copy, a move, a trash restore
    and a remote's cache. On its own it may arrive before its owner, and deleting, copying
    or moving it alone touches nothing else. A format that notes its sidecars in a version's
    metadata has that rebuilt by a reindex from empty, as all metadata is.

  The suite's own tests run a test format with each option and with both, and show that it
  catches a format that keys per-repository work by package, forgets it on a move, leaves a
  sidecar behind, or links one.
- `internal/artifacts/core/item` and `internal/artifacts/core/trash` test `SidecarKeeper`
  through each write, with store faults, and a sidecar that cannot land holding its owner
  back (#533).
- Unit and fault tests for each optional interface, `Derive` and the `RejectionError` mapping
  (#267).
- A test-only format exercises every part of the router kit (#269).
- `internal/app`'s `TestAccP8ProtocolPaths` holds a path of each P8 protocol to the
  catch-all's 404 until the format mounts the protocol, and then to the kit's 404 in the
  protocol's own envelope, so no format's pull request edits it (#533).
- Every read path refuses a blacked-out repository and an excluded path, in unit tests and
  in `download_policy.feature` with `jf rt dl` (#328).
- `test/server/artifacts/protocol_download_policy_test.go` holds every native protocol to the
  policy in tables: a file download of each (`TestAccProtocolDownloadPolicy`,
  `TestAccProtocolDownloadPolicyVirtual`) and one metadata document of each
  (`TestAccProtocolMetadataPolicy`, `TestAccProtocolMetadataPolicyVirtual`). A row checks the
  blackout's and the patterns' 404 for an administrator and a reader, that a caller who may not
  read learns nothing of the setting, and the protocol's answer once the setting is cleared.
  Each format's own tests pin its routes under each rule (#1251).
- `test/server/artifacts/folder_archive_policy_test.go` holds the folder archive download to
  the policy (#1253).

## References

- [24-package-format-handlers](../24-package-format-handlers.md)
- [0018-package-index](0018-package-index.md), [0022-federated-repositories](0022-federated-repositories.md),
  [0031-docker-oci-registry](0031-docker-oci-registry.md), [0033-persistent-work-queue](0033-persistent-work-queue.md)
- Artifactory OSS 6.20, `base/capi/src/main/java/org/artifactory/sapi/interceptor/StorageInterceptor.java`
  at `b99e5649bcad`
- Artifactory OSS 6.20 at `b99e5649bcad`, for the download policy:
  `backend/core/src/main/java/org/artifactory/repo/RealRepoBase.java`,
  `backend/core/src/main/java/org/artifactory/repo/db/DbLocalRepo.java`,
  `backend/core/src/main/java/org/artifactory/repo/virtual/VirtualRepoDownloadStrategy.java`,
  `backend/core/src/main/java/org/artifactory/repo/service/RepositoryBrowsingServiceImpl.java`,
  `backend/core/src/main/java/org/artifactory/repo/service/RepositoryServiceImpl.java`
  (`isRepoPathVisible`), `backend/core/src/main/java/org/artifactory/search/SearcherBase.java`,
  `base/api/src/main/java/org/artifactory/aql/result/AqlRestResult.java` and
  `web/rest/src/main/java/org/artifactory/rest/resource/artifact/ArtifactResource.java`

## CRAN archive migration (2026-10-28, #778)

The opt-in legacy archive endpoint uses the existing item transfer lifecycle
inside one outer transaction. Source rows are locked before validation, and
`Transfer.IfAbsent` uses `ItemStore.Create` so a destination inserted after
preflight is a conflict rather than an overwrite. Create has the same item
normalization as Put and is atomic across all storage backends. Existing
transfers retain their established overwrite policy. The migration keeps
empty source folders and their properties, moves only the recognized source
archive layout and rolls back item, property, package-index, blob-reference
and queued-work changes together. See [the CRAN guide](../../testing/cran.md#migrate-legacy-source-archives)
for the documented wire contract and explicitly inferred conventions.

## P9c Ansible collection adapter (#782)

The `ansible` format now validates collection archive identity, dependency ranges,
manifest/file checksums and bounded tar content in Inspect/Admit before publication.
It synchronizes package versions and properties through the existing item hooks;
delete, restore and reindex retain the native metadata view. Galaxy v3 publication
uses the shared package router and bounded decoded multipart upload. No additional
store schema or generic fallback is introduced.

The remote adapter uses source/cache metadata and archive validation, rewrites
download URLs, and retains offline, refresh, redirect and no-store behavior.
Ansible virtual metadata unions readable concrete members in order, with the first
member winning duplicate versions. Nested virtuals are rejected by MemberRequest;
P10 owns the shared nested graph. One-node federated publication is proven; P13
owns cross-peer convergence. See the [connection recipe and checked sources](../../operations/ansible.md)
and [native acceptance](../../../test/acceptance/features/ansible.feature), pinned
to ansible-core 2.21.5 and checked against current published guides on 2026-10-28.

## P9c Nix binary-cache adapter (#788)

The Nix handler inspects bounded narinfo and NAR content at the common item
admission boundary, including generic deployment, copy and restore. Complete
metadata/archive pairs derive the package index. NAR URLs are content-addressed
and immutable. The native binary-cache adapter generates hosted Ed25519
signatures using sealed keypairs, checks remote signatures when trust roots are
configured, and rewrites archive URLs while preserving the signed fingerprint.
It omits publisher content-address assertions it has not independently proved.

Remote metadata and archives use P9 source-policy capabilities and current
read checks; redirects stay beneath the configured origin and path. Generic
remote item downloads cannot bypass this protocol validation. Virtual resolution
keeps ordered concrete members and rechecks outer and member authority. Actual
Nix 2.35.4 copy/substitution in fresh stores provides L/R/V/F evidence. The
[contract and limits](../../testing/nix.md) document trust configuration, bounded
parsing, lifecycle behavior and the binary-cache scope of this inventory row.

## P9c Machine Learning adapter (#780)

FrogML publication uses the existing item service and metadata transaction: stage
files under a principal-owned transaction, verify the bounded manifest's file
sizes and SHA-256 digests, then commit canonical model items and the package index
atomically. Raw deploy cannot publish a version or replace its immutable files.
Private staging and manifest records remain behind the native authorization
checks across raw download, checksum reuse, folder archive and transfer routes.
Delete, restore and reindex expose only complete manifest-backed versions.

The adapter serves existing L/F configurations; R/V remain rejected. There is no
new database schema or external model-training service. The [FrogML recipe and
wire evidence](../../operations/machinelearning.md) records SDK 2.1.153, the reserved
layout words, transaction lifetime, integrity limits and Artifactory-only mode.

### Hugging Face Xet current layout (#779, 2026-10-28)

Hugging Face keeps model/dataset snapshots and refs in the existing format-owned
layout. The opt-in `huggingface.xet_enabled` switch enables native Xet tokens,
xorb uploads, MDB shards and file reconstruction. Verified model-scoped records
live under `<identity>/.xet/{xorbs,files,sha256,stages}/<hash>` using the item/blob
services; no shared MachineLearning schema is introduced. The separate legacy
layout migration remains #789.

The Xet codec verifies keyed BLAKE3 chunk/root hashes, grouped or ordinary LZ4,
shard section bounds, reconstruction lengths, segment verification hashes and
file SHA256 before publishing mappings. Uploads permit bare native chunk streams
and minimal version-2 MDB shards, as sent by released `hf_xet` 1.7.0. Decoded
chunks are bounded to 128 KiB, xorbs to 64 MiB, encoded xorbs to 80 MiB, shards to
64 MiB and files to 10 GiB. Repeated chunk references share disjoint fetch ranges.

Five-minute signed capabilities bind the current caller/credential, public
repository, selected member, model/dataset and revision. Every CAS read restores
and reauthorizes the caller and resolves a published readable file. Transfer URLs
extract only chunks that belong to that file; a bundled xorb never grants access
to its other files. The ordinary item, checksum, archive, copy/move and non-admin
trash relocation paths cannot expose private records. Both Xet and Git LFS
uploads record a verified caller/model grant before a Hub commit can reuse the
blob; a global checksum alone, including an unreadable or trashed blob, is not
publication authority.

Remotes retain P9's verified Hub artifact and metadata cache as the source of
truth. The edge derives its Xet representation from those bytes with bounded
memory; it does not forward caller credentials to an upstream CAS or create a
second cache that can survive source invalidation. A 128-entry memory cache stores derived metadata only, keyed by source generation,
live item identity and digest. Every hit follows a current P9 read; a deleted
cache item cannot be served from derived metadata. Chunk requests read a bounded
segment instead of rehashing a whole model. Transient/no-store sources retain the
existing Hub HTTP path without advertising Xet, preventing repeated full-stream
transfers. Offline, source refresh and final authority fences remain owned by P9. Cross-peer federation remains P13.

Primary references: [Hugging Face Xet API](https://huggingface.co/docs/xet/api),
[xorb format](https://huggingface.co/docs/xet/xorb),
[shard format](https://huggingface.co/docs/xet/shard),
[released client implementation](https://github.com/huggingface/xet-core), and
[JFrog's Hugging Face guide](https://docs.jfrog.com/artifactory/docs/hugging-face-repositories).
The guide's older client support table and its statement that newer Hub clients
always use Xet conflict as version guidance. The qualification therefore pins
and executes the newest released Hub 2.3.0 and hf_xet 1.7.0 directly, with a fresh
cache and large-file HTTP fallback blocked.

## Download policy rules for the native protocols (2026-11-11, #1251)

These rules say what every native protocol route answers under a blackout and under the
include and exclude patterns, so that each mirrors Artifactory 7.x. Code comments cite them as
R1 to R14. Each rule names its sources: 7.x's documentation, knowledge base, release notes and
issue tracker where they speak, read on 2026-11-11 against 7.173.18 (SaaS 7.177.2), and 6.20's
source elsewhere. **[I]** marks an answer that no source shows for 7.x; #216 lists each one for
the comparison in P19. "The blackout's 404", "the patterns' 404" and "No repository found to
serve the request" are the three refusals of the generic download policy above.
[By protocol](#by-protocol) gives each protocol's paths.

### R1. Stored metadata documents

Where 7.x stores a metadata document at a path in the repository, it reads the document
through the checked download path:
[RTFACT-13337](https://jfrog.atlassian.net/browse/RTFACT-13337) (4.15) shows npm's stored
`package.json` refused so. artifactweave checks the blackout and the patterns at that path with
`Kit.CheckDocument`, whether it stores the document there or computes it:
- A blacked-out repository refuses the document with the blackout's 404 whether or not it is
  stored, as 6.20's `LocalRepoBase.getInfo` checks the path before it reads storage.
- A pattern on the document's own path refuses it with the patterns' 404.
- A pattern on one release's file leaves the document as it was (R3).
- A protocol whose download finds its file through metadata reads checks the file with
  `CheckDocument` before those reads, so the refusal names the file, a remote repository's own
  policy included: Pub's archive, CocoaPods' source archive, Composer's dist, Cargo's crate and
  Hex's tarball.

Sources: RTFACT-13337; the knowledge base's "How to use Include/Exclude patterns?" (a pattern on
`.npm/@my/**` blocks the metadata); the Hex, Pub, CocoaPods and Cargo documentation;
RTFACT-30356, RTFACT-29873, RTFACT-17256, RTFACT-20646, RTFACT-27283 and RTDEV-60689 (Go's
`.info`). A local PyPI repository's page paths are [I].

### R2. Native metadata of a blacked-out repository

#### R2a. Resolution metadata

What a client reads to resolve and install, where 7.x's storage path for it is not documented,
is refused with the blackout's 404 before anything is computed, naming the request's own path
(`Kit.CheckBlackedOut`); the patterns never refuse it. The documentation says that 7.x ignores
a blacked-out repository "when trying to resolve artifacts" and that it is "not available for
download or deployment of artifacts", and 6.20 answers its file list with 404
(`ArtifactResource.writeStreamingFileList`). Each such route is [I], one #216 item per
protocol. A route that resolves nothing keeps its answer [I]: Ansible's API root and Conda's
`notices.json`. RTFACT-4274 does not bear on it: it is a null-pointer fix of 2011, in 2.3.4.

#### R2b. Native search routes

npm's `/-/v1/search`; NuGet's `/query`, `/autocomplete` and V2 `Search()`; Conan's search;
Chef's search and its `/api/v1/cookbooks` without `q`; Bower's `/packages`; Puppet's module
search; Skills' search; RubyGems' `api/v1/gems`, `api/v1/versions` and `search.json`; and a
blacked-out Docker repository's `_catalog` keep their answers. 6.20's searchers
(`SearcherBase.isResultAcceptable`, `AqlRestResult.canRead`) check permissions and patterns,
not the blackout, and 7.146.7 has AQL results respect the patterns for users who are not
administrators. Each is [I]: #216 asks for each under a blackout and under patterns, as an
administrator and as another user.

### R3. A release whose file the patterns reject stays listed

Only the file is refused, and a client then fails rather than falls back to another release.
Every listing, index and document keeps the release, local, remote and virtual: the listings
find releases through `Kit.StoredItem`, `ResolveStoredItem` and `ReadMetadata`, which apply no
policy. Sources: the Hex documentation (to hide a package, set a pattern on each of its layout
folders), RTFACT-24511, RTFACT-29715, RTFACT-8218, RTFACT-6205, RTFACT-30307, RTFACT-25571,
RTFACT-19925, RTFACT-29572 and RTFACT-24940. So:
- The Agent Plugins marketplace, a stored file that lists each plugin's latest version, keeps
  that version when the patterns reject its ZIP [I].
- Chef's records name the highest version the caller may read even when the patterns reject
  its archive, and its `latest/download` is refused rather than falling back to an older
  version [I].
- Only Maven's version tokens skip a rejected version (R14).

### R4. Remote repositories and remote members

- **The policy path is apart from the permission path.** A remote read checks read permission
  where it always did, and the policy at the path 7.x caches the document or file at, the
  source boundary's `Resource.PolicyPath`: a metadata document at its R1 path, a file at its
  7.x storage path. A PyPI file is checked at `packages/<a>/<b>/<hash>/<file>`, the hash path
  of its upstream URL [I]; RTFACT-31147 suggests that 7.x may cache it without the
  `packages/` prefix.
- **The lookups a remote file download makes on its way to the file** check the blackout alone
  (`Resource.BlackoutOnly`), so a pattern on a document does not refuse the files it lists.
- **A refusal is the policy's own 404.** The boundary hands back its `*format.RejectionError`,
  and `pkgroute.SourceError` keeps it recognisable, so no remote refusal answers 500 or 502 and
  a virtual repository passes over a refusing remote member. A listing keeps a row on a policy
  refusal and drops it only when read permission is denied (`Result.CheckPermission` checks
  permission alone).

### R5. Virtual repositories

- **A merge leaves out a blacked-out member,** local or remote, since the documentation says
  resolution ignores it (`Kit.MergeMembers`).
- **A member that refuses only a release's file stays in the merge,** with the release listed
  (RTFACT-25571, RTFACT-6205).
- **A member that refuses the document's own path is left out** [I], as
  [RTFACT-17256](https://jfrog.atlassian.net/browse/RTFACT-17256) shows for PyPI.
- **A file request passes over a refusing member** and tries the next
  (`VirtualRepoDownloadStrategy.processStandard`). When every member refuses, or none holds the
  file, the protocol answers its own not-found [I for the body].
- **Each member is checked at the file's own path,** never at an index the implementation reads
  to find the file: the documentation says that an artifact matching an exclude pattern is "not
  resolved from that repository", and 6.20 checks each member at the requested path [I]. A
  Conda member whose patterns reject only `repodata.json` still serves a package through the
  virtual repository, while the virtual repository's `repodata.json` merge leaves that member
  out.
- **The virtual repository's own patterns** refuse a file with "No repository found to serve
  the request for `<virtual>:<path>`" (6.20 `VirtualRepoDownloadStrategy`;
  [RTFACT-29600](https://jfrog.atlassian.net/browse/RTFACT-29600) fixed a PyPI virtual
  repository that ignored its exclude pattern), and a metadata document at its R1 path the same
  way [I].

### R6. Refusals pass through unchanged

A download that the patterns refuse says why:
[RTDEV-47144](https://jfrog.atlassian.net/browse/RTDEV-47144) (SaaS 7.98.2) fixed a download
from a VCS remote repository that answered a bare 404 when the patterns refused it. A handler
that answers its own not-found for a missing item passes the policy's refusal on instead
(`pkgroute.IsRefusal`), with the policy's message in the protocol's envelope [I for the body].
NuGet's symbols, PyPI's core metadata, Skills' single-file read, Swift's archives, Terraform's
signed module and provider files, Puppet's archives, and the Chef, Bower, Conan, Ansible and
Bazel modules downloads answer so. A virtual repository of Chef, Bower, Conan, Ansible, Bazel
modules, LuaRocks, Swift, Nix or Puppet passes over a member that refuses (R5).

### R7. No success headers on a refusal

The policy is checked before any response header is set, so a refusal carries none of a served
file's headers: Hugging Face's resolve sets no `X-Repo-Commit`, `ETag` or `X-Xet-*`, and Agent
Packages' download no immutable `Cache-Control`.

### R8. Archive entries and sidecars take their owner's check

As an archive entry takes its archive's check and a checksum sidecar its file's, a PyPI
`<wheel>.metadata` takes the wheel's, a NuGet `.nuspec` the `.nupkg`'s, a Skills single-file
read the bundle's, and Hugging Face's Xet reconstruction and data the file's.

### R9. Blackout applies to every type, on every route

The Local Repositories page's note on the blackout field says "The following package types do
not support this field: HuggingFace ML, Debian, Terraform, Terraform BE, Chef, Git LFS, Puppet,
and Vagrant." Yet the `blackedOut` field is stored and returned for every type: the JFrog
Terraform provider, jfrog-client-go and the REST, YAML and configuration JSON documentation
round-trip it. No source shows what 7.x does with it at runtime, and the same page's notes do
not describe the server reliably: its priority resolution note names Debian, yet RTDEV-90957
(7.161.16) fixed a Debian virtual merge with priority resolution on. artifactweave therefore
refuses every download and every native read route (R1, R2a) of a blacked-out repository of
any type, local, federated or remote, which is what the field is documented to do. Terraform's
module `/download` answers as a GET of the URL it returns would [I]. #216 asks for the eight
types blacked out in P19 [I], and a deviation is registered only if 7.x serves them.

### R10. Status, message and order of checks

- **Read permission first, then the policy** (DV-057). Bazel modules, Git LFS's download batch,
  Nix's `nix-cache-info` and Terraform's signed URLs check it first too.
- **A request that fails the route's own validation answers its 400 before the policy is
  checked** [I]. Skills' single-file read is the case: under a blackout, a malformed `path`
  answers 400 "invalid skill file path" ahead of the refusal; without a blackout, a missing
  version keeps its 404 first (`skills/http/reads.go`; #216 G-SK-1).
- **A policy refusal is 404 with the policy's message,** never 401 or 403 (RTDEV-47144).
  Debian, RPM, Alpine, Opkg, CRAN and p2 answered it with 401 or 403 before #1251, and Conda
  with 403 "Conda index is not readable".
- **The refusal names the path Artifactory checks:** a document's R1 path, a VCS remote
  repository's `<owner>/<name>/gitrefs` (RTFACT-30171, RTFACT-30743), a Docker tag's
  `<image>/<tag>/manifest.json` (R11), and [I] a Nix NAR's own `nar/<file>` and a Helm remote
  repository's `index.yaml`.
- **Cargo answers every refusal in Artifactory's envelope,** remote refusals included, as 7.x
  refused `index/config.json` in it
  ([RTFACT-29873](https://jfrog.atlassian.net/browse/RTFACT-29873)).

### R11. Docker and OCI

A pull under `/v2` that the policy refuses answers 404 with the policy's message in
Artifactory's envelope, `{"errors":[{"status":404,"message":...}]}`, under no registry code, for
local and remote repositories alike (decision [0031](0031-docker-oci-registry.md)). The Docker
client printed `unknown: <message>` for 7.x's pattern refusal
([RTFACT-27234](https://jfrog.atlassian.net/browse/RTFACT-27234)), so 7.x sent no
`MANIFEST_UNKNOWN`. The Docker addon is not open source, so the exact body is [I] (research 24,
5.5 and 5.6).
- The blackout is checked before the lookup and names the file the pull reads:
  `<image>/<tag>/manifest.json` for a tag, `<image>/sha256:<hex>/manifest.json` for a digest
  and `<image>/_uploads/sha256__<hex>` for a blob, the paths a remote repository checks [I].
- The patterns are checked on the copy a pull serves, and a blob is served from any copy in the
  image the patterns accept ([RTFACT-30134](https://jfrog.atlassian.net/browse/RTFACT-30134)).
  A missing manifest or blob keeps `MANIFEST_UNKNOWN` or `BLOB_UNKNOWN`.
- `_catalog` leaves out images whose folder the patterns reject
  ([RTFACT-26524](https://jfrog.atlassian.net/browse/RTFACT-26524)). A local repository's tags
  lists and referrers keep what the patterns reject, since RTFACT-27234 is an open request to
  filter tags; a remote repository's tags list still leaves out a tag whose
  `<image>/<tag>/manifest.json` the patterns reject [I]. A blacked-out repository refuses an
  image's tags list and referrers, naming `<repo>:<image>` as a blacked-out remote repository
  does [I], because helm resolves unversioned or ranged charts from the tags list and cosign and
  notation find signatures through the referrers. Its catalog still lists every image (R2b).
- A cross-repository mount from a source that is blacked out, or whose patterns accept no copy
  of the blob, opens an upload session (202), as for a source the caller may not read [I].
- Virtual Docker repositories are P10.

### R12. Indexes of the Debian, RPM, Alpine, Opkg and CRAN families

These indexes keep a package whose file the patterns reject (R3). Sources: RTFACT-30830 (an
open request for Debian, so the index is unchanged by intent), RTFACT-30656 (a virtual RPM
repository answers 404 for the file and still lists it in `primary.xml.gz`), RTFACT-26619 and
RTFACT-5000. An index row checks read permission alone. A virtual GET walks the members and
passes over one that refuses (R5), and CRAN's virtual merge no longer puts another member's
version in place of a refused one. A Debian snapshot copies what its source's indexes list,
packages the source's patterns reject included [I], and answers its source's refusal of a
`Release`, its signatures or an index with the policy's 404, creating no snapshot.

### R13. System paths

6.20's `RepoBase.accepts` exempts Maven index files (a name starting
`nexus-maven-repository-index`) and any path ending `.index` from the patterns, not from the
blackout. `generic.CheckDownload` does the same for downloads [I], so a Maven repository's
`.index/` files and a p2 repository's `p2.index` stay readable under patterns. Deploys keep
checking every path.

### R14. Maven

`[RELEASE]` and `[INTEGRATION]` skip a version whose file the patterns reject, as `-SNAPSHOT`
resolution already did, and a remote repository's `maven-metadata.xml` that its own policy
refuses counts as absent when it would resolve a path (6.20
`RemoteLatestMavenVersionResolver`). Decision [0036](0036-maven.md) has both [I].

### By protocol

Each native protocol's paths under the rules (#1251). A document in the second column answers
the blackout's 404 and, when the patterns reject its path, theirs. The third column is refused
by the blackout alone (R2a), naming the request's path unless the cell names another. A listing
keeps a release whose file the patterns reject (R3).

| Protocol | Documents refused at their 7.x path (blackout and patterns) | Refused only when blacked out (the request's path) | Notes |
|---|---|---|---|
| npm | `.npm/<name>/package.json`: packument, version, dist-tags; local and remote | none | Search kept. A remote tarball's packument lookup checks the blackout alone. npm's envelope. |
| PyPI | `.pypi/<p>.html`, `.pypi/simple.html` (local [I]); remote files at `packages/<a>/<b>/<hash>/<file>` [I] | `pypi/<p>/json`, `pypi/<p>/<v>/json` (local) | `<wheel>.metadata` takes the wheel's check. |
| NuGet | none stored; `.nuspec` takes the `.nupkg`'s check; symbols at `.symbols/<file>/<key>/<file>`; remote `.nupkg` at `<id>/<v>/<id>.<v>.nupkg` | flat index; registration index, page, leaf; v2 `FindPackagesById()`, `$count`, `Packages(Id=,Version=)` | `query`, `autocomplete`, v2 `Packages()`, `Search()` and service documents kept. |
| RubyGems | `specs.4.8.gz`, `latest_specs.4.8.gz`, `prerelease_specs.4.8.gz`, `quick/Marshal.4.8/<g>-<v>.gemspec.rz` | `names`, `versions`, `info/<g>` | JSON API and `search.json` kept as search [I]. A virtual `.gem` asks each member for its own path. |
| Go | `<m>/@v/<v>.info`, stored or made | `<m>/@v/list`, `<m>/@latest`, `<m>/@v/latest.info` | `@latest` of a version with no stored `.info` takes that `.info`'s patterns. Plain text. |
| Pub | `.pub/<p>.json` | none | Archive checked at `<p>/<p>-<v>.tar.gz` before the reads that find it. |
| CocoaPods | `.pod/cdn/<path>`, every CDN file (remote [I]) | none | Remote source archive at `<pod>/<v>/source.tar.gz`. |
| Composer | none (storage not documented) | `packages.json`, `p2/<v>/<n>.json`, `~dev.json` | Remote dist at `<v>/<n>/<ver>.zip`. |
| Cargo | `config.json`, `index/<prefix>/<name>` | `api/v1/crates/<name>` | Crate at `crates/<name>/<name>-<v>.crate`. |
| Hex | `names`, `versions`, `packages/<name>` | none | `public_key` not refused. |
| Helm | `index.yaml`, local and remote | none | A `.prov` is checked at its own path. |
| Conda | each repodata document at its own path | none | `notices.json` not refused [I]. |
| Conan | every revision file at its own path | revisions and latest (recipe and package), file lists | Search kept. |
| Ansible | none (computed) [I] | collection, versions, version | API root not refused [I]. Archive at `collections/<ns>/<n>/<v>/<ns>-<n>-<v>.tar.gz`. |
| Bazel modules | `bazel_registry.json`, `modules/<n>/metadata.json`, `.../<v>/MODULE.bazel`, `.../<v>/source.json` | none | Archive at `modules/<n>/<v>/<file>`; a remote's cache at `archives/<n>/<v>/<file>` [I]. |
| Bower | `<n>/<v>/bower.json` (`/binaries`) | `/packages/<n>` (`packages/<n>`), `/refs` (`refs/<key>/<n>`) | `/packages` and search kept. Remote archive at `<n>/<v>/bower-archive.tar.gz` [I]. A client download through the dependency rewrite reads with `Read`, so the virtual repository's patterns refuse an external archive; only the rewrite's pre-cache uses `ReadMetadata`. |
| Chef | none | cookbook and version records, `latest/download` | Catalog and search kept. Archive at `<n>/<n>-<v>.tar.gz`; `latest` never falls back [I]. |
| LuaRocks | `manifest`, `manifest-5.x` at the root, local and remote | none | A remote rock's manifest lookup checks the blackout alone (R4). |
| Swift | none | release list `<scope>/<name>`; release info `<v>`, `<v>.json`, `<v>/Package.swift` | Archive at `<scope>/<name>/<name>-<v>.zip`, refused with no archive headers. A remote repository names the package folder `<scope>/<name>` [I]. |
| Terraform | a remote repository's documents under `.terraform/modules/` and `.terraform/providers/` [I] | module `versions`; provider `versions`, package metadata, mirror `index.json` and `<v>.json` | A module's `<v>/download` answers as a GET of its archive `<ns>/<name>/<system>/<v>.zip` would (R9). Signed files pass the policy's message on. |
| Git LFS | none | none | A transfer is checked at `objects/<xx>/<yy>/<oid>`. A download batch answers a caller who may not read 403 per object. Locks are unchanged. |
| Nix | `nix-cache-info`, `<hash>.narinfo` | none | A NAR is checked at its own `nar/<file>`, so patterns on the NARs leave the narinfo served. |
| VCS | a remote repository's refs at `<owner>/<name>/gitrefs` | none | Read permission stays on the project folder. `downloadCommit` reads no refs. |
| Puppet | a remote repository's `.puppet/<m>/releases.json` [I] | `v3/releases`, with or without `module`; `v3/releases/<slug>`; `v3/modules/<m>`; `v3/modules` without `query` | Module search (`v3/modules?query=`) kept (R2b). The lists of every release and module keep a release whose archive the patterns reject (R3); 7.x writes Puppet's listings as stored documents, an `all.json` among them (RTFACT-26143). Archive at `<m>/<m>-<v>.tar.gz`. |
| Vagrant | none | the box catalog `<box>`, to a caller who could see one of its boxes | Other callers get "Vagrant box not found" as before. The catalog leaves out an entry whose box the patterns reject until P19 [I]. |
| Maven | `maven-metadata.xml` and files, through the item routes | none | Version tokens skip rejected releases; a remote's refused `maven-metadata.xml` counts as absent [I]. `.index/` files take the blackout alone. |
| Docker, OCI, Helm OCI | `<image>/<tag>/manifest.json`, `<image>/sha256:<hex>/manifest.json`, `<image>/_uploads/sha256__<hex>` [I] | `tags/list`, referrers (`<image>`) | `_catalog` leaves out rejected images; a local repository's tags lists keep rejected tags. |
| p2 | composites and files, through the item routes; a remote repository over a `site.zip!/` URL at the entry's path | none | `p2.index` takes the blackout alone. A virtual leaves a blacked-out member out. |
| Debian | `Packages*`, `Sources*`, by-hash, `Release`, `InRelease`, `Release.gpg`; a snapshot's `snapshots/<tag>/<path>` | none | |
| RPM | `repodata/repomd.xml` and the other repodata | none | |
| Alpine | `<branch>/<repo>/<arch>/APKINDEX.tar.gz` | none | |
| Opkg | `<feed>/Packages`, `Packages.gz` | none | |
| CRAN | `src/contrib/PACKAGES[.gz]`, `bin/<os>/contrib/<r>/PACKAGES[.gz]` | none | A virtual merge keeps the first member's row. |
| Machine Learning | `models/<n>/<v>/model-manifest.json` | none | |
| Agent Packages | none | version list (`<o>/<n>`) | No archive headers on a refusal. Search kept. |
| Agent Plugins | the marketplace files at the root | none | Lists the latest version though its ZIP is refused [I]. |
| AI Editor Extensions | none | none | The gallery checks an extension folder as a folder [I]. |
| Hugging Face | Xet reconstruction and data at the file's snapshot path | info, tree, `xet-read-token` (`models--<o>--<n>`) | `resolve` refuses before it sets its headers. |
| Skills | none | detail, versions, version, resolve; `latest` downloads (`<slug>`) | A single file takes its bundle's check. `xrayStatus`, list and search kept. |
| NVIDIA NIM, JetBrains Plugins | remote only: the source boundary checks each read | none | |

- **When every member refuses.** A virtual file request that no member serves answers the
  protocol's own not-found [I for the body]: Composer's "Composer package not found", Pub's
  "version not found", CocoaPods' "CocoaPods resource not found", Conda's "Conda package not
  found", Chef's "Chef cookbook or version not found" and Puppet's "Puppet release not found".
  The item routes answer "Could not find resource".
