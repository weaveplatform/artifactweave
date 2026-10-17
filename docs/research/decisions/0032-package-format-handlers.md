# 0032: Package format handlers

Status: Accepted

## Context

npm, Go and Maven (P7), then PyPI, NuGet, Helm and Terraform (P8), each react to what is
deployed. They index packages and versions, write derived files, refuse what their layout
forbids, resolve special download paths, and answer their client's protocol. The framework
P6 built for Docker is too thin for that ([research 24](../24-package-format-handlers.md)
section 2):
- its hooks cannot read content or write items;
- a format's error becomes a 500;
- nothing runs before a deploy, on a download or on a property change;
- there is no reindex contract;
- `/artifactory/api/<type>/<repo>/` answers 404 for every type but Docker.

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
- `Enqueue`, the persistent work queue's enqueue (decision
  [0033](0033-persistent-work-queue.md)), usable inside the current transaction.

**Required hooks** stay as on main, now taking `Env`: `Deployed` after a file is stored,
`Deleting` before each file is removed, and `Deleted` once after a delete or a move.

**Optional interfaces.** A format implements only what it needs. Each is named here by
its method; the Go interfaces are `Inspector`, `Admitter`, `Resolver`, `Annotator`,
`Reindexer` and `Classifier` (#267).

| Interface | Runs | Does | First used by |
|---|---|---|---|
| `Inspect` | After the content is committed to the blob store, **before** the write transaction | Parses the content (an archive entry, a manifest) into what the hooks need, so no decompression holds the database's write lock | npm, Maven |
| `Admit` | Before a deploy, a checksum upload, and the target of a copy or move | Rewrites the target path, adds properties, or refuses with a `format.Rejection` | Maven (snapshot naming and policy), every type (include and exclude patterns, blackout) |
| `Resolve` | Before a download is served (GET and HEAD) | Rewrites the requested path, such as a version token or a `-SNAPSHOT` name | Maven |
| `Annotated` | After an item's properties change | Updates the index or queues derived work | npm (`npm.disttag`) |
| `Reindexer` | On a JFrog reindex operation, and for maintenance | Rebuilds the format's index for a repository from the item tree | Docker (adopting `docker.Reindex`), npm, Maven, P8 |
| `Classifier` | When asked | Says whether a path is a unique binary, metadata, or a derived item | P9 cache expiry, P11 replication, P13 federation |

`Annotated` was added after the research: Artifactory's npm metadata follows property
changes (`NpmMetadataInterceptor`, research 24 fact 1.2), and P7's npm dist-tags are
properties.

**Format errors.** A format refuses with `format.Rejection{Status, Message}`. `itemError`
maps it to that status, in the envelope of the surface the request arrived on. Formats still
never import `internal/server`.

**Generic deploy policy.** `includesPattern`, `excludesPattern` and `blackedOut` are checked
for every package type through `Admit`, on the generic handler that every format embeds.

**Generic download policy** (amended 2026-10-17, #328). Artifactory 6.20 refuses a download
before it looks for the item, whether or not the item exists, and for administrators too
(`DbLocalRepo.checkDownloadIsAllowed` calls `RealRepoBase.assertValidPath` with
`downloadRequest` set). `generic.CheckDownload` does the same:
- **A blacked-out repository** answers 404 with `BlackedOutException`'s message, as a deploy
  does.
- **A path the patterns reject** answers 404 with `IncludeExcludeException`'s message, where a
  deploy answers 409.
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
- Docker manifest and blob pulls: a blacked-out repository answers `NAME_UNKNOWN`, as a push
  does, and a manifest or blob the patterns reject answers `MANIFEST_UNKNOWN` or
  `BLOB_UNKNOWN`, as a missing one does. The Docker addon is not open source, so these codes
  are inferred (research 24, 5.5).
- The storage API's file list (`?list`) of a blacked-out repository
  (`ArtifactResource.writeStreamingFileList` maps `BlackedOutException` to 404).
- The read paths of later formats, which find items through the same download lookup or call
  `generic.CheckDownload` themselves (#269).

Where it does not apply, as in 6.20:
- The storage API's item info and its other views describe the item: `ArtifactResource`
  checks read permission alone.
- AQL and the search APIs do not hide a blacked-out repository's items: `AqlRestResult.canRead`
  and `SearcherBase.isResultAcceptable` check permissions and patterns only. Both already hide
  what the patterns reject from everyone but administrators (research 22 section 1.3), where
  Artifactory's search APIs also show it to a caller who may annotate it (DV-058).

Research 24 section 5 (5.4 to 5.8) lists what stays unconfirmed until P19 (#216).

**Everything indexed is rebuildable from items.** What a format indexes, and every item it
derives, follows from the items, their content and their properties alone, so a reindex, a
federation member (decision [0022](0022-federated-repositories.md)) and a replication target
reach the same result. State a protocol request carries, such as a dist-tag in an npm publish
body, is stored as a property, not kept aside. A derived item is classified as derived, so
replication and federation skip it and recompute it.

**Which derived work goes through the work queue** (decision 0033):
- **Through the queue, one kind per format and kind of work, keyed by package:**
  - Maven metadata at the artifact and plugin levels, and at the snapshot level;
  - `maxUniqueSnapshots`;
  - later, Helm's `index.yaml` and NuGet's registration pages (P8).

  A read of derived data whose package is still queued calls `EnsureFresh` first, so a client
  never reads stale metadata.
- **Not queued, because the deploy keeps them current or they are computed on read:**
  - npm packuments and search, which read the package index and the per-version metadata
    cache;
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
PyPI, NuGet, Helm and Terraform) mount through one kit in `internal/server`, as Docker's
registry does today (decision 0031):
- `Mount` claims `/artifactory/api/<type>/{repoKey}/...` before the generated servers, and
  leaves the JFrog operations at fixed literals beside it (such as `.../reindex`) to them.
- Repository lookup by package type, with a class gate through a `Source` seam. The `Source`
  answers for local repositories in P7, an upstream in P9 (decision
  [0007](0007-remote-repositories.md)) and a merge of members in P10 (decision
  [0008](0008-virtual-repositories.md)).
- `Require` checks permissions and answers in the format's envelope.
- A `ServeItem` callback reuses the item download path, so redirects (decision 0029) and
  download counts apply.
- A deploy helper with the item routes' matrix and checksum parsing exported, answering 201
  with the `X-Checksum-Sha1`, `-Sha256` and `-Md5` headers.
- Raw-path parsing for encoded slashes (`@scope%2fname`).

Maven needs no protocol router: it is served by the item routes plus the `calculateMetadata`
and `generatePom` operations.

**Test tables.** The status table, the route × principal matrix and their coverage guard
split into one file per format, with fixture repositories of each type. A format's pull
request then touches only its own files.

**Remote and virtual repositories before P9 and P10.** npm and Go answer 501 in their own
envelope, naming the repository class (decision [0003](0003-repository-model.md)). Docker
keeps 405 `UNSUPPORTED` (decision 0031). Maven, served by the item routes, keeps the generic
behaviour: a remote refuses deploys and a virtual deploys to its default deployment
repository.

## Rationale

The hook model mirrors Artifactory's interceptors (research 24 section 1), which is how
Artifactory keeps format behaviour the same whichever API wrote the item.

**The download policy** follows Artifactory's source, with two departures:
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

## Constraints

- `Derive` writes without hooks. A format must not derive an item that another format would
  need to index.
- A rolled-back transaction can leave content that `Inspect` committed to the blob store;
  garbage collection already reaps unreferenced content (decision
  [0010](0010-retention-trash-and-gc.md)).
- What Artifactory does in research 24 section 5 is unconfirmed until the live comparison
  (P19, #216).

## Verification

- `internal/format/formattest` runs every format against the same contract on the in-memory,
  SQLite and PostgreSQL stores: the index is right after deploy, overwrite, delete, copy,
  move and restore, and a reindex reproduces it (#268).
- Unit and fault tests for each optional interface, `Derive` and the `Rejection` mapping
  (#267).
- A test-only format exercises every part of the router kit (#269).
- Every read path refuses a blacked-out repository and an excluded path, in unit tests and
  in `download_policy.feature` with `jf rt dl` (#328).

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
