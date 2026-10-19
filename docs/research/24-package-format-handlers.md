# Package format handlers

Research baseline: **2026-10-16**.

Checked against: **Artifactory 7.161.32** (self-managed; SaaS 7.174.0) on 2026-10-17, by [#341](https://github.com/weaveplatform/artifactweave/issues/341); audited against KB, Jira and client sources on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)); [the 7.x check](#6-artifactory-7x-check) records it.

Phase P7 adds npm, Go and Maven local repositories at once, and P8 adds PyPI, NuGet, Helm and
Terraform. Each format reacts to what is deployed: it indexes packages and versions, writes
derived files such as `maven-metadata.xml`, refuses what its layout forbids, and answers its
client's protocol under `/artifactory/api/<type>/<repo>/`. This report records what the
format framework on main offers today, how Artifactory structures the same job, and what the
formats of P7 to P9 need from the framework. Decision
[0032](decisions/0032-package-format-handlers.md) records the choices. The protocol facts of
each format are in their own reports: npm (#264), Go (#265), and Maven with Gradle, Ivy and
SBT (#266).

Each claim is labelled:

| Label | Means |
|---|---|
| **[D]** | documented at the cited place, read on 2026-10-16 |
| **[S]** | seen in source code at the cited commit |
| **[J]** | recorded in JFrog's public Jira (`https://jfrog.atlassian.net/rest/api/2/issue/<KEY>`), read on 2026-10-18; it counts as 7.x behaviour under the rules in the [research README](README.md) |
| **[I]** | inferred from the documented facts; not stated by any source |

Sources:
- **artifactweave** at `37379c5` on main: the paths cited below.
- **Artifactory OSS 6.20:** `knottnhupfer/artifactory-oss` at `b99e5649bcad`, under
  `base/capi/src/main/java/org/artifactory/sapi/interceptor/`,
  `backend/core/src/main/java/org/artifactory/repo/interceptor/` and
  `backend/core/src/main/java/org/artifactory/repo/virtual/interceptor/`.
- **jfrog-client-go** at `0646d7b50f9a`, `artifactory/services/go/publish.go`.
- **Decisions** [0018](decisions/0018-package-index.md),
  [0022](decisions/0022-federated-repositories.md),
  [0031](decisions/0031-docker-oci-registry.md) and
  [0033](decisions/0033-persistent-work-queue.md).

## 1. How Artifactory structures format work

| # | Fact | Label |
|---|---|---|
| 1.1 | Format behaviour hangs off one storage-interceptor contract. `StorageInterceptor` has `beforeCreate`, `afterCreate`, `assertDeletePathAllowed`, `beforeDelete`, `afterDelete`, `isCopyOrMoveAllowed`, `beforeMove`, `afterMove`, `beforeCopy`, `afterCopy`, `beforePropertyCreate`, `afterPropertyCreate`, `beforePropertyDelete` and `afterPropertyDelete` (`sapi/interceptor/StorageInterceptor.java`). A `before` callback refuses an operation by setting an error on the `MutableStatusHolder` it receives | [S] |
| 1.2 | `NpmMetadataInterceptor` implements `beforeCreate`, `afterCreate`, `beforeDelete`, `afterDelete`, `afterMove`, `beforePropertyCreate`, `beforePropertyDelete` and `afterPropertyCreate`: npm's metadata follows property changes as well as file changes | [S] |
| 1.3 | `MavenMetadataCalculationInterceptor` implements `afterCreate` and `afterDelete`; `MavenPomInterceptor` implements `afterCreate`, `isCopyOrMoveAllowed` and `afterImport`; `IntegrationCleanerInterceptor` (`maxUniqueSnapshots`) implements `afterCreate` | [S] |
| 1.4 | `NuGetCalculationInterceptor` implements `afterCreate`, `afterDelete`, `afterMove`, `afterCopy` and `afterImport` | [S] |
| 1.5 | Virtual repositories have their own `VirtualRepoInterceptor` contract (`shouldReturnCachedResource`, `interceptGetInfo`, `onBeforeReturn`), with Maven implementations for metadata and POMs (`repo/virtual/interceptor/`) | [S] |
| 1.6 | Artifact-level Maven metadata is recalculated after commit, on a queue with eight workers (decision 0033, from `MavenMetadataService`) | [S] |
| 1.7 | Whether the metadata files that Artifactory writes itself (such as `maven-metadata.xml`) pass back through the storage interceptors is not shown by the interceptors' code | [I] |

## 2. The framework on main

| # | Fact | Label |
|---|---|---|
| 2.1 | `internal/format/format.go` defines `Handler` with three hooks: `Deployed(ctx, st, c, it)` after a file is stored, `Deleting(...)` before each file is removed, and `Deleted(ctx, st, c, removed)` once after a delete or move removed items (the last from #254) | [S] |
| 2.2 | `format.For(t)` returns `docker.Handler` for `docker`, `oci` and `helmoci`, and the no-op `generic.Handler` for every other type | [S] |
| 2.3 | The hooks run inside the item service's transaction, from `internal/service/item/item.go` (deploy at line 328, delete at 543 to 579), `restore.go` line 72 and `transfer.go` (copy or move, 437 to 493) | [S] |
| 2.4 | `internal/service/item` imports `internal/format`, so a format package cannot import `item` to write an item without an import cycle | [S] |
| 2.5 | The hooks receive only `store.Store`. They have no blob store, so a format cannot read what was deployed: an npm `package.json`, a POM or a `go.mod` | [S] |
| 2.6 | `internal/layout/layout_test.go` forbids `internal/format/` from importing `internal/server`, so a format cannot build an `apierror`. `itemError` (`internal/server/items.go` line 186) maps the item service's own errors to statuses and turns anything else into a 500 | [S] |
| 2.7 | `format.For` is called only from the item service's deploy, delete, restore and transfer paths. No hook runs before a deploy, when a download path is resolved, or when an item's properties change | [S] |
| 2.8 | `internal/service/pkgindex` offers `Link`, `VersionOf` and `Prune`. Nothing on main calls `Link`: the Docker format writes its index through its own code (`internal/format/docker/index.go`), which also provides `docker.Reindex` | [S] |
| 2.9 | `types.ComparatorFor` (`pkg/types/version.go`) gives npm, Go, Helm and Terraform `CompareSemver`, PyPI `ComparePEP440`, NuGet `CompareNuGet`, Docker, OCI and Helm OCI the push order, and every other type, the Maven family included, `CompareMaven`. The P5 searches order Maven versions with `CompareMercury`, Artifactory's comparator (`internal/server/searches_layout.go`, `searches_versions.go`) | [S] |
| 2.10 | The store has `RecordVersionDownload` (`pkg/store/content.go`), but nothing calls it: `serveFile` records item downloads only | [S] |
| 2.11 | `pkg/layout/bundled.go` compiles the bundled layouts. `maven-2-default`, `ivy-default` and `gradle-default` are verified; `maven-1-default`, `sbt-default`, `sbt-ivy-default`, `npm-default`, `go-default`, `nuget-default` and `terraform-module-default` are marked unverified. #277 later checked every bundled layout ([research 03](03-repository-configuration-model.md#bundled-definitions)) | [S] |
| 2.12 | `item.Service.Layout` (`transfer.go` line 305) resolves a layout by name. The token reading the P5 searches use (`layoutScope.read`, `coordinates`) is private to `internal/server/searches_layout.go` | [S] |
| 2.13 | `repo.Defaults` sets an omitted `repoLayoutRef` to `sbt-default` for sbt, `ivy-default` for Ivy and `maven-2-default` for every other package type (`createLayout` in `pkg/repo/packagetypes.go`, [#396](https://github.com/weaveplatform/artifactweave/issues/396)). A protocol handler therefore cannot take its storage paths from `repoLayoutRef`. Artifactory's own defaults are in [research 03](03-repository-configuration-model.md#common-fields) | [S] for the code |
| 2.14 | The item routes match `/artifactory/{repoKey}/...` with `repoKey` any segment but `api` (`notAPI`, `internal/server/handwritten.go` line 104), so `/artifactory/api/npm/...` and `/artifactory/api/go/...` answer 404 today | [S] |
| 2.15 | The Docker registry is the one hand-written protocol package: `internal/server/docker`, mounted by `registry.Register(r)` in `internal/server/server.go` (line 178) before the generated servers, with the JFrog operations beside it reaching the generated server (decision 0031) | [S] |
| 2.16 | `surfaceFor` (`internal/server/stubs.go`) already picks the npm envelope `{"error":"..."}` for `/artifactory/api/npm/`, the plain-text one for `/artifactory/api/go/` and `/artifactory/api/pypi/`, and the Terraform registry's for `/artifactory/api/terraform/` | [S] |
| 2.17 | `includesPattern`, `excludesPattern` and `blackedOut` are stored with their defaults (`pkg/repo/defaults.go`) but not checked when a file is deployed; only AQL reads the include patterns (`internal/server/aql.go`) | [S] |
| 2.18 | A deploy answers 201 with the `ItemCreated` JSON (`internal/server/items.go` line 335) and no `X-Checksum-*` response headers; `serveFile` sets them on downloads only | [S] |
| 2.19 | `jf go-publish` reads `X-Checksum-Sha256` from the response to each PUT and logs when it is missing (jfrog-client-go `artifactory/services/go/publish.go` line 116) | [S] |
| 2.20 | Stored content carries SHA-256, SHA-512, SHA-1 and MD5, computed in one pass on every upload path (`pkg/blob/blob.go`, #296) | [S] |
| 2.21 | `internal/service/work` runs the persistent work queue of decision 0033: `Handle(kind, h)`, `Enqueue(ctx, kind, key, payload)` inside the caller's transaction, `EnsureFresh(ctx, kind, key)` and `WaitIdle(ctx)` (#297) | [S] |
| 2.22 | `item.Target` carries `Cached`, which marks a remote repository's cache (`internal/service/item/item.go` line 69) | [S] |
| 2.23 | The status table, the route × principal matrix and their coverage guard are single shared lists in `internal/server` (`statusTable`, `matrixTable`, `implementedHandwritten` in `status_test.go` line 475) | [S] |

## 3. Everything indexed is rebuildable from items

Decision 0022 already states that a federated repository recomputes "the package index and
the format metadata (Maven metadata, npm packuments, Helm index and the like) ... locally on
each member from the applied items rather than shipped". Decision 0018 states that "the item
tree remains the source of truth; a reindex rebuilds the index from it". Together they fix a
rule for every format **[S, from the decisions]**:
- What a format indexes, and every derived file it writes, must follow from the items, their
  content and their properties alone.
- A reindex, a federation member and a replication target all arrive at the same result.
- So a format must not keep state that only a protocol request carries. An npm dist-tag sent
  in a publish body, for example, becomes a property on the tarball (#264).

## 4. What P7 to P9 need from the framework

| # | Need | From | Label |
|---|---|---|---|
| 4.1 | Read the deployed content (`package.json` in a tarball, a POM, `go.mod`) before indexing. Reading it inside the write transaction would hold SQLite's single writer for the length of a decompression | npm, Maven, P8's Helm, NuGet and PyPI | [I] |
| 4.2 | Write derived items, such as `maven-metadata.xml` at three levels, and later Helm's `index.yaml`, without re-entering the hooks | Maven, P8 | [I] |
| 4.3 | Rewrite a deploy's path (a Maven `-SNAPSHOT` name to its unique form), add properties, or refuse it with a status (a snapshot in a releases-only repository: 409) | Maven; Artifactory's upload service and `beforeCreate` (1.1, 1.3) | [S] for Artifactory, [I] for us |
| 4.4 | React to property changes (npm `npm.disttag`) | npm; `NpmMetadataInterceptor` (1.2) | [S] |
| 4.5 | Resolve a download path before serving it (`[RELEASE]`, `[INTEGRATION]`, a `-SNAPSHOT` name to its newest unique build) | Maven | [I] |
| 4.6 | Answer a JFrog reindex operation (`calculateNpmRepositoryMetadata`, `calculateMavenMetadata` and the P8 ones, which answer 501 today) by rebuilding the index from the item tree | npm, Maven, P8 | [S] for the 501 stubs |
| 4.7 | Answer a protocol under `/artifactory/api/<type>/<repo>/` in the format's own error envelope, without shadowing the JFrog operations at fixed literals beside it | npm, Go, P8 | [S] for 2.14 to 2.16 |
| 4.8 | Tell unique content, metadata and derived items apart, so P9's cache expiry, P11's replication and P13's federation skip derived items and recompute them | P9, P11, P13 | [I] |
| 4.9 | Index items cached from a remote (`Target.Cached`), because P9 fills caches through the item service | P9 | [I] |
| 4.10 | Ask a per-format source for versions and documents: the local index in P7, an upstream in P9 (decision 0007), and a merge of members in P10 (decision 0008) | P9, P10 | [I] |
| 4.11 | Mount host-root routes (Terraform's `/.well-known/terraform.json`), accept multipart form uploads (twine) and authenticate with `X-NuGet-ApiKey` | P8 | [D] (plan P8) |
| 4.12 | Return `X-Checksum-Sha1`, `-Sha256` and `-Md5` on a deploy's 201, which `jf go-publish` reads (2.19) | Go | [S] |

## 5. Open points for the live comparison (P19, #216)

| # | Question | Label |
|---|---|---|
| 5.1 | Whether Artifactory's own derived writes (`maven-metadata.xml`, npm metadata) pass through the storage interceptors (1.7) | [I] |
| 5.2 | Whether Artifactory refuses a deploy outside `includesPattern`, inside `excludesPattern` or to a blacked-out repository for every package type, and with which status and message | [I] |
| 5.3 | Whether Artifactory returns `X-Checksum-*` headers on every deploy's 201, or only on some paths | [I] |
| 5.4 | Whether 7.x still refuses a download from a blacked-out repository, or of an excluded path, with 6.20's 404 and messages, ahead of the read permission check (decision 0032, #328) | [I] |
| 5.5 | What a Docker pull answers for a blacked-out repository and for a manifest or blob the patterns reject; the Docker addon is not open source, and artifactweave answers 404 `NAME_UNKNOWN`, `MANIFEST_UNKNOWN` and `BLOB_UNKNOWN` with the generic messages | [I] |
| 5.6 | Whether Docker tag lists, referrers and the catalog of a blacked-out repository answer 404 or list nothing; artifactweave still lists them | [I] |
| 5.7 | Whether the storage API's file list (`?list`) leaves out items the patterns reject, and how it treats a virtual repository's blacked-out member; it is built by the closed REST addon, and artifactweave refuses only a blacked-out repository named directly | [I] |
| 5.8 | Whether 7.x's storage API item info still describes items of a blacked-out repository and excluded paths, as 6.20's `ArtifactResource.prepareStorageInfoResponse` does | [I] |

## 6. Artifactory 7.x check

Checked against Artifactory 7.161.32 on 2026-10-17 ([#341](https://github.com/weaveplatform/artifactweave/issues/341)); audited against KB, Jira and client sources on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)).

**Sources.** JFrog's current documentation, read on 2026-10-17 (the pages named below), and the Artifactory release notes: self-managed 7.84.3 to 7.161.32 ([self-managed releases](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases)), SaaS 7.80.0 to 7.174.0 ([SaaS releases](https://docs.jfrog.com/releases/docs/artifactory-saas-releases)), the [known issues](https://docs.jfrog.com/releases/docs/artifactory-known-issues) and the [deprecations](https://docs.jfrog.com/releases/docs/artifactory-deprecations). JFrog no longer publishes the notes for 7.0 to 7.83 (the [end-of-life page](https://docs.jfrog.com/releases/docs/artifactory-end-of-life) keeps only their dates), so a change in that range shows only through the current documentation.

**Unconfirmed for 7.x:** the storage-interceptor contract (section 1) is 6.20's internal code;
the 7.x documentation and release notes describe no equivalent. Facts 1.1 to 1.5 are
unconfirmed for 7.x. The framework (decision 0032) depends on what formats do, not on how
Artifactory wires it.

**What 7.x adds to the open points of section 5:**
- **5.2 and the search APIs.** AQL filters items a repository's patterns reject for non-admins
  from [7.146.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed), "so AQL results align with repository access rules and other search
  methods". That points to the search APIs filtering them as well, as artifactweave does
  (DV-058), but no note says so outright.
- **5.4.** No 7.x page or note describes a download from a blacked-out repository, so it stays
  open.
- **npm.** [7.146.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed) refuses copy and move into `.npm/` (research 25 section 5).
  The JFrog operations `api/npm/auth` and `api/npm/<repo>/auth/<scope>` answer errors in the
  Artifactory envelope, `{"errors":[{"status":400,"message":"Not supported user type."}]}` on
  7.73.0 ([J] RTFACT-30181, research 25 section 4), so `surfaceFor` (fact 2.16) gives those
  two Artifactory's envelope and the rest of `/artifactory/api/npm/` npm's
  ([#292](https://github.com/weaveplatform/artifactweave/issues/292)).
  `api/npm/<repo>/reindex` answers in the Artifactory envelope too: `surfaceFor` picks it
  for that path ahead of npm's ([#293](https://github.com/weaveplatform/artifactweave/issues/293)).
- **Maven.** 7.x accepts more POMs, with a leading blank line, non-breaking spaces or
  ISO-8859-1 (research 27 section 6).

These are format rules, not framework ones.

The rest of section 5 stays on [#216](https://github.com/weaveplatform/artifactweave/issues/216).
