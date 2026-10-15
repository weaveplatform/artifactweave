# AQL, searches and build-info

Research baseline: **2026-10-15**.

Phase P5 completes Artifactory Query Language, serves the GET search endpoints and adds
build-info. Decision [0012](decisions/0012-search-and-aql.md) covers search and AQL, and
decision [0014](decisions/0014-build-info.md) covers build-info. This report collects what
Artifactory and its clients do where those decisions,
[research 02](02-artifactory-core-api-inventory.md) and
[research 09](09-jfrog-cli-compatibility.md) leave a point open. P3 shipped the `items`
domain in the shape the JFrog CLI generates (`pkg/aql`); everything below is what remains.

Each claim is labelled:

| Label | Means |
|---|---|
| **[D]** | documented by JFrog at the cited URL, read on 2026-10-15 |
| **[S]** | seen in source code at the cited commit |
| **[I]** | inferred from the documented facts; not stated by any source |

Sources:
- **AQL pages** under `https://docs.jfrog.com/artifactory/docs/`: `aql-syntax`,
  `aql-search-criteria`, `aql-query-output`, `aql-query-execution`, `aql-repository-queries`,
  `aql-entities-fields-reference`, `aql-performance` and `aql-examples`, each read with `.md`
  appended.
- **The Builds API pages** under `https://docs.jfrog.com/integrations/reference/`:
  `uploadbuild`, `appendbuild`, `promotebuild`, `renamebuild`, `getbuildruns`,
  `getallbuilds`, `getbuildinfo`, `controlbuildretention`, `deletebuilds` and
  `deletebuildsmultiple`. Each embeds an OpenAPI 3.1 block.
- **The cached search definitions** in `.specoracle/artifactory-searches-api/`.
- **Source at pinned commits:** jfrog-client-go at `0646d7b50f9a` (the commit `jf` 2.126.0
  pins), jfrog-cli-artifactory at `4a0f3827cfa0`, build-info-go at `f9d40441f262` and
  jfrog-cli-core at `b47892ded3a0`.
- **Artifactory's own source**, for promotion and retention (§3.6): the open source edition
  6.20.0 as mirrored at `github.com/knottnhupfer/artifactory-oss` `b99e5649bcad`, and the
  Java build-info model at `github.com/jfrog/build-info` `4f0d5407279e`.

## 1. AQL

### 1.1 Domains and fields

- **[D] Primary domains:** `items`, `builds`, `build.promotions`, `releases` and
  `release_artifacts` (`aql-syntax`), plus `item.infos` (`aql-entities-fields-reference`).
  The `archive` domain and the `entry` fields are deprecated, and since Q3 2024 they "will
  result in a query parsing error".
- **[D] Item fields:** `repo`, `path`, `name`, `created`, `modified`, `updated`,
  `created_by`, `modified_by`, `type` (`file`, `folder` or `any`; default `file`), `depth`,
  `original_md5`, `actual_md5`, `original_sha1`, `actual_sha1`, `sha256`, `size` and
  `virtual_repos`.
- **[D] Build fields:** `url`, `name`, `number`, `created`, `created_by`, `modified`,
  `modified_by`, `started` and `repo`. `started` is taken from the build-info document and
  stays fixed through promotion and replication.
- **[D] Fields of the other domains:**

  | Domain | Fields |
  |---|---|
  | Promotion | `created`, `created_by`, `status`, `repo`, `comment`, `user` (the CI user) |
  | Property | `key`, `value` |
  | Statistic | `downloaded`, `downloads`, `downloaded_by`, `remote_downloads`, `remote_downloaded`, `remote_downloaded_by`, `remote_origin`, `remote_path` |
  | Artifact | `name`, `type`, `sha1`, `md5` |
  | Module | `name` |
  | Dependency | `name`, `scope`, `type`, `sha1`, `md5` |
  | Item info | `item_id`, `props_modified`, `props_modified_by`, `props_md5` |
- **[D] Relation paths.** A field of another domain is named by its full relation path from
  the primary domain, for example `items.find({"artifact.module.name":"mymodule"})` and
  `builds.find({"module.artifact.item.name":"artifactory.war"})` (`aql-syntax`).
- **[S] The CLI's build-scoped file searches** use exactly these paths:
  `{"$and":[{"artifact.module.build.name":"<n>","artifact.module.build.number":"<v>"}]}` for
  artifacts, and the matching `dependency.module.build.*` pair for dependencies
  (jfrog-client-go `artifactory/services/utils/aqlquerybuilder.go`, `buildClauseItems`).
- **[D] `$msp`** (match on a single property) works on every domain that has properties:
  item, module and build (`aql-search-criteria`).
- **[S] How Artifactory joins the domains.** Artifactory's open source edition, last
  published in 2022 (mirrored at `github.com/knottnhupfer/artifactory-oss`, `master`), shows
  the model behind the paths:
  - **By SHA-1.** Items join build artifacts and dependencies on the item's actual SHA-1
    (`AqlTableGraph`: `nodes.sha1_actual` to `build_artifacts.sha1` and
    `build_dependencies.sha1`). The `sha256` fields of both are commented out of
    `AqlPhysicalFieldEnum`.
  - **One table per domain.** The SQL generator joins each table once, with outer joins, so
    criteria on `artifact.module.build.name` and `artifact.module.build.number` meet on one
    build. Each property criterion, by contrast, is its own subquery, which is why `$msp`
    exists.
  - **A dependency's name is its id.** `BuildStoreServiceImpl` stores
    `dependency.getId()` as the dependency name and its scope set joined with `,` as the
    scope; build-info dependencies carry an id, not a name.

### 1.2 Output, sorting and limits

- **[D] Includes.** Naming any item field in `.include()` replaces the default fields;
  naming only property or statistic fields keeps the defaults and adds them
  (`aql-query-output`).
- **[D] Sort, limit and offset** work only when the query has no include, or when the include
  names fields of the primary domain only. Sorting is only on fields in the output.
- **[D] Limit and permissions.** `.limit(N)` reads N rows before permission filtering, so a
  non-admin may get fewer than N.
  - The `scan-factor` request header (7.133.0 and later) lets the search read beyond N, so a
    caller can still receive up to N results it may read.
  - The `artifactory.aql.query.non.admin.scan.factor` property sets the factor for every
    user, default 1. A header value above 1 overrides it for that request (`aql-performance`).
- **[D] Result cap.**
  - Self-managed Artifactory has "no default maximum result limit". Since 7.90.6,
    `aql.search.query.max.limit` can set one.
  - SaaS has used a fixed default of 500,000 results since 7.100.2, and from 7.107.0 sets
    `aql.search.query.max.limit` to 500,000 (`aql-performance`).
  - When the cap trims results, the `range` block gains `notification`: "AQL query reached
    the search hard limit, results are trimmed. Please address configurable limit."
    (`aql-query-output`).
- **[D] `?compact=true`** minifies the JSON. A `stream: true` request header switches the
  server to cursor reads in batches of 50,000 rows; the response shape is unchanged
  (`aql-query-execution`).
- **[D, S] Builds rows** carry `build.<field>` keys, and promotions rows
  `build.promotion.<field>`, as the `builds.find` example in `aql-query-output` shows. In the
  open source edition every build and promotion field is a default result field
  (`AqlPhysicalFieldEnum`), and result rows serialise their keys alphabetically and leave
  out nulls (`AqlRestResult.Row`: Jackson `alphabetic = true`, `NON_NULL`). Fields of other
  domains in an include nest under `modules`, `artifacts`, `items` and so on.
- **[D] `virtual_repos`** needs `repo`, `path` and `name` in the result set. It must usually
  be requested, and it is added implicitly when the query targets a virtual repository
  (`aql-repository-queries`).

### 1.3 Errors and permissions

- **[D] Status codes** (`aql-query-execution`):

  | Status | When |
  |---|---|
  | 400 | the query is empty, over the size limit (`AQL query is too long`), or fails syntax or validation |
  | 403 | an anonymous user runs a query |
  | 408 | the database timeout cancels the query |
  | 429 | the concurrent-query limit is reached |
- **[D] Non-admins.** Without a scoped token, a non-admin may query only the `items` domain,
  and must include `repo`, `path` and `name`. A scoped token with `build:<pattern>:r` may run
  `builds.find()`, and must include `name`, `number` and `repo`. A missing field logs "AQL
  minimal field expectation error".
  - The pattern is a path in the build-info repository: `build:artifactory-build-info/**:r`
    reaches every build and `build:artifactory-build-info/my-build/**:r` one build name
    (`aql-query-execution`, quick reference).
  - **[S]** The 2022 open source edition predates build scopes: it drops every row of a
    domain other than items for a non-admin (`AqlRestResult.canRead`).
- **[D] Repository patterns.** AQL results honour each repository's include and exclude
  patterns.

### 1.4 Remote caches and virtual repositories (#68)

- **[D] Results report the cache key.** In the example `items.find({"repo" :
  "docker-remote-repo"}).transitive()`, a result row reports `"repo":
  "docker-remote-repo-cache"` (`aql-repository-queries`, example 1).
- **[D] What `.transitive()` does.** It enables "a search within a remote repository", which
  means a Smart Remote repository. In a virtual repository, the transitive search covers only
  the first five remote repositories (`aql-repository-queries`).
- **[D] Virtual repositories.** `items.find({"repo":"my-virtual"})` covers the "local
  repositories and remote repository caches included in the specified virtual repository"
  without `.transitive()`. With it, the query also lists items on Smart Remote members
  (`aql-repository-queries`).
- **[D] The CLI's own guidance.** For a remote file that is not yet cached, it says to fetch
  it once, then "Run `jf rt dl` again against the same path", with the example
  `jf rt dl "generic-remote/path/file.zip" ./ --fail-no-op`
  (`https://docs.jfrog.com/artifactory/docs/generic-files.md`, "Download from remote
  repositories").
  - `--transitive` (7.17.0 and later) is offered as an alternative to the first fetch,
    through a virtual repository that includes the remote.
  - The CLI sends the remote's own key, with no `.transitive()` unless `--transitive` is
    given ([research 09](09-jfrog-cli-compatibility.md)).
- **[I] So a remote's key matches its cache.** A `repo` criterion naming a remote matches
  that remote's cached items without `.transitive()`, and the result rows report
  `<remote>-cache`. The CLI then downloads `GET <remote>-cache/<path>`, which Artifactory
  serves.
- **[I] The cache key matches too.** A criterion naming `<remote>-cache` matches the same
  items: the result rows carry that key, and the permission evaluator already resolves it
  to the remote (`internal/authz`).

Confidence:
- **High** that a remote's key matches its cache: two JFrog pages agree.
- **Medium** on the exact semantics of `$ne`, `$match` and `$nmatch` against a remote or
  virtual key, which no page shows.

### 1.5 Actions

- **[D] `items.delete(<criteria>).dryRun("true"|"false")`** works on the items domain only,
  and does not support `sort`, `limit` or `offset` (`aql-syntax`, `aql-examples`).
- **[D] `properties.update(<criteria>).keys(...).newValue("v").dryRun(...)`** updates item
  properties only, with `item.*` paths in the criteria (same pages).
- **Undocumented:** no page gives the response body, the permission each action needs, what
  an action without `.dryRun()` does, the version that introduced them, or a licence tier.
  Section 4 and decision 0012 cover what artifactweave does.

## 2. Searches

The cached definitions in `.specoracle/artifactory-searches-api/` match the specification in
`api/src/artifactory/paths-searches.yaml`, authored in P1.

- **[D] Quick (`artifact`), property (`prop`), checksum, bad checksum, usage, dates, creation
  and pattern** search local repositories and remote caches. Property, checksum and bad
  checksum search also take virtual repositories in `repos`.
  - Quick, property, checksum and bad checksum search allow anonymous callers ("Requires a
    privileged user (can be anonymous)"); creation, dates, usage and pattern search need "a
    privileged non-anonymous user".
  - `X-Result-Detail: info` and `properties` widen each result beyond the `uri`.
- **[D] GAVC, versions and latest version** read Maven coordinates through the repository's
  layout (`pkg/layout`, P0).
  - `latestVersion` answers `text/plain`.
  - `/api/versions/{repo}/{path}` compares the lowercase `version` property.
- **[D] Build-info searches.** `dependency` and `buildArtifacts` read build-info. `license`
  belongs to the licence feature and `archive` is deprecated; neither is in P5.
- **[D] Limits for internal and anonymous users.** "All searches return limited results for
  internal and anonymous users (same limits as in the user interface)", configurable by
  system property. This is the shared description on every cached search page.

## 3. Build-info

### 3.1 The API

The Builds API pages carry OpenAPI 3.1 blocks. The spec oracle fetches them from the
`jfrog-artifactory-builds-api` index (#162). docs.jfrog.com lists the same pages again under
`jfrog-artifactory-build-api` with `-1` slugs, so only one index is fetched.

| Operation | Request | Documented response | What the CLI checks [S] |
|---|---|---|---|
| Build upload | `PUT /api/build[?project=]`, JSON body | 200 | 200, 201 or 204, and reads `X-Checksum-Sha256` from the response for `--detailed-summary` (`artifactory/services/buildinfo.go`, `PublishBuildInfo`) |
| All builds | `GET /api/build[?project=]` | 200 `{uri, builds[{uri:"/<name>", lastStarted}]}`; no 404 listed | not called |
| Build runs | `GET /api/build/{name}[?project=]` | 200 `{uri, buildsNumbers[{uri:"/<number>", started}]}`; 404 | 200; 404 means not found |
| Build info | `GET /api/build/{name}/{number}[?started=&diff=&project=]` | 200 `{uri, buildInfo}`; 404 | 200; 404 means not found |
| Build append | `POST /api/build/append/{name}/{number}[?started=&buildRepo=&project=]` | 204 | not called: `jf rt build-append` appends locally |
| Promotion | `POST /api/build/promote/{name}/{number}[?project=]`, `application/vnd.org.jfrog.artifactory.build.PromotionRequest+json` | 200 `{messages[]}` | 200 (`artifactory/services/promote.go`) |
| Rename | `POST /api/build/rename/{name}?to=` | 200 `text/plain` | not called |
| Retention | `POST /api/build/retention/{name}[?async=]`, `count` typed `integer` | 200 | **204** (`artifactory/services/discardBuilds.go`); `count` is sent as a **string** (`MaxBuilds string json:"count,omitempty"`) |
| Delete | `DELETE /api/build/{name}?buildNumbers=&artifacts=0\|1&deleteAll=0\|1` | 200 `text/plain` | not called |
| Delete (multiple) | `POST /api/build/delete`, `{project, buildName, buildNumbers[], deleteArtifacts, deleteAll}` | 200 `text/plain` | 200 or 204 |
| Build artifacts | `GET /api/builds/buildArtifacts/{name}/{number}/{build-info repo}` | undocumented | 200 `[{repo, path, name}]`; any error falls back to AQL (`artifactory/services/utils/artifactoryutils.go`) |

- **[D] Upload preconditions.** Every artifact in `modules[].artifacts` must already exist with
  matching SHA-1 and MD5, and carry `build.name` and `build.number` properties equal to the
  document's `name` and `number`; otherwise the UI reports "No path found".
- **[D] Permissions.**
  - Upload needs deploy permission on the build, plus delete to overwrite an existing
    build-info artifact.
  - Reads need read permission on the build.
  - Promotion needs deploy; rename needs deploy and delete; retention and deletion need
    delete.
- **[D] Promotion options.** By default it covers all artifacts of all scopes, with
  dependencies off. `scopes` are additive, and the target may be a virtual repository
  (5.7 and later). `properties` are attached to the build's artifacts whether or not a target
  is given.

### 3.2 The document

- **[S] What the CLI sends.** build-info-go's `BuildInfo` sends `name`, `number`, `agent`,
  `buildAgent`, `modules`, `started`, `durationMillis`, `properties`, `artifactoryPrincipal`,
  `url`, `issues`, `artifactoryPluginVersion` and `vcs`. It sends no `version` field.
  `started` uses the layout `2006-01-02T15:04:05.000-0700` (`entities/buildinfo.go`).
- **[D] The OpenAPI schema** additionally lists `version`, `type`, `licenseControl` and
  `buildRetention`.
- **[S] Module types** include `build`, `generic`, `maven`, `gradle`, `docker`, `npm`, `nuget`,
  `go`, `python`, `terraform`, `helm`, `conan`, `nix`, `uv`, `gem`, `apk`, `cargo` and
  `debian`. A module of type `build` names another build as `<name>/<number>`, and the CLI's
  build searches follow such modules recursively (`getAggregatedBuilds`).

### 3.3 Identity and storage

- **[S] A run, not a number, is the identity.** One build name and number may have several
  runs. `jf rt bp --overwrite` counts how often a number appears in the build runs, and sends
  that number that many times in `POST /api/build/delete` (jfrog-cli-artifactory
  `artifactory/commands/buildinfo/publish.go`, `CalculateBuildNumberFrequency`, whose comment
  reads "the build number is not unique"; jfrog-client-go `createDeleteBuildInfoBody`). A run
  is identified by name, number and `started`, not by name and number alone.
- **[S] The document is stored as an item.**
  - It lives in `<project>-build-info`, at `<name>/<number>-<started epoch ms>.json`.
  - The repository is `artifactory-build-info` when the project is empty or `default`
    (`GetBuildInfoRepositoryByProject`).
  - The CLI's query for it matches `path` against the encoded name and `name` against
    `<encoded number>-<timestamp>.json`.
- **[S] How names and numbers are encoded** (`aqlquerybuilder.go`,
  `encodeForBuildInfoRepository`):

  | Character | Encoding |
  |---|---|
  | `/` | `%2F` |
  | `\` | `%5C` |
  | `\|` | `%7C` |
  | `*` | `%2A` |
  | `?` | `%3F` |
  | `'` | `%22` (as written in the source) |
  | `:` | `%3A` |
  | `;` | `%3B` |
  | `%` | `%25` |

  Any whitespace becomes a space, and a space next to an encoded `/` becomes `%20`.
- **[S] `LATEST`.** The CLI finds the latest number with
  `items.find({"type":"file","repo":"<project>-build-info","path":{"$match":"<encoded name>"}}).sort({"$desc":["created"]}).limit(1)`,
  then strips the name after its last `-` (`GetLatestBuildNumberFromArtifactory`,
  `CreateAqlQueryForLatestCreated`).
- **[S] Build-append** finds the document's SHA-1 and MD5 by querying that item
  (`CreateAqlQueryForBuildInfoJson`).
- **[S] Raw paths.** The promotion URL path-escapes name and number with `url.PathEscape`, so
  a `/` arrives as `%2F` in the path (`buildUrlWithEscapingSlash.go`). A router that decodes
  the path before matching splits such a name.

### 3.4 Properties on upload

- **[S] The CLI sets the build properties itself.** `jf rt u --build-name --build-number`
  adds the matrix parameters `build.name=<n>;build.number=<v>;build.timestamp=<ms>`
  (jfrog-cli-core `common/build/buildutils.go`, `CreateBuildProperties`). The server already
  stores matrix parameters as properties (P3).
- **[S] VCS properties after publishing.** `jf rt bp` may set CI VCS properties on the build's
  artifacts through `PUT api/storage` and the build-artifacts API
  (`commands/buildinfo/civcs_props.go`).

### 3.5 Terraform

- **[S] No provider manages build-info.** Neither jfrog/artifactory nor jfrog/platform does.
  The only use of `api/build` in either is a release-bundle test fixture
  (`resource_artifactory_release_bundle_v2_test.go`). Build permissions are already covered
  by `platform_permission` (#85).

### 3.6 Promotion and retention in Artifactory's source

The open source edition carries the REST resource and the promotion logic; the discard
itself is in the closed Pro build add-on.

- **[S] Promotion** (`BuildResource.promote`, `BuildPromotionHelper`, `BaseBuildPromoter`):
  - It answers **400 when `failFast` is set and any message is an error or a warning**,
    and 200 otherwise, with `{messages[]}` either way. A missing build or target repository
    is 404; a fail-fast promotion with artifacts it cannot find is 400.
  - A virtual target becomes its default deployment repository.
  - Artifacts are the items carrying the run's `build.name` and `build.number` properties
    whose SHA-1 is an artifact's, in `sourceRepo` when one is given. Dependencies are found
    by checksum alone, in any repository, filtered by `scopes`. An unmatched artifact or
    dependency is an error: `Unable to find artifact '<name>' of build '<name>' #<number>`.
  - Files are moved or copied to the target at the paths they had, with the dry-run and
    fail-fast flags passed on. What a fail-fast move had done before the error stays done.
  - `properties` are added to the files at the target, or where they are without one; a
    file the caller may not annotate is a warning.
  - The promotion is recorded only with a `status`, not in a dry run, and not when a
    fail-fast promotion met an error or warning. Each case adds an info line such as
    `Skipping promotion status update: no status received.`
  - Message levels are the status entries' level names: `ERROR`, `WARNING`, `INFO`
    (`PromotionResult`). The docs show them in lower case. The Jenkins plugin fails a
    promotion on a `WARNING` or `ERROR` line (`jfrog/hudson-artifactory-plugin`,
    `ArtifactoryPromoter`).
- **[S] Retention** (`BuildResource.retention`, `BuildServiceImpl.addBuild`):
  - A `count` of 0 is refused with 400 "Max count retention needs to be a positive number".
    build-info's `BuildRetention` defaults `count` to -1, which keeps any number of runs.
  - `async` is a plain boolean parameter, so an absent one is false and the request discards
    before answering, unless the `build.retention.always.async` property is set. The docs give
    true as the default.
  - The request is one discard; it records no lasting policy. A published document's
    `buildRetention` is applied once, after the run is stored.
  - `minimumBuildDate` is a `java.util.Date`, which Jackson reads from epoch milliseconds or
    a timestamp.
- **[I] Protected numbers and `count`.** The closed add-on decides whether runs of a
  protected number count towards `count`. Build retention mirrors Jenkins's "discard old
  builds", whose log rotation counts builds kept forever towards the number kept.

## 4. Inferred and undocumented

These points are not stated by any source. Decision 0012 or 0014 records what artifactweave
does for each.

- **[I] Reading a number without `started`.** `GET /api/build/{name}/{number}` without
  `started` answers the most recent run with that number.
- **[I] What actions return.** A dry run must show which items it would touch, which suggests
  `.delete()` and `.update()` answer in the `find` shape: the affected rows and a `range`.
- **[I] What actions need.** `.delete()` would follow `DELETE` and need delete permission on
  each item; `.update()` would follow set-properties and need annotate permission.
- **[I] An action without `.dryRun()`.** Every documented example names it, so no source
  shows the default. Only a dry run makes leaving it out harmless.
- **[I] Out of P5.** The `releases` and `release_artifacts` domains depend on release
  bundles v1 (P15). `item.infos` needs a record of property modifications that the item store
  does not keep.

## 5. Versions at the research baseline

| Component | Version |
|---|---|
| JFrog Artifactory docs (AQL, searches) | read 2026-10-15 |
| JFrog Builds API reference | read 2026-10-15 |
| JFrog CLI | 2.126.0 (acceptance) |
| jfrog-client-go | `0646d7b50f9a` |
| jfrog-cli-artifactory | `4a0f3827cfa0` |
| build-info-go | `f9d40441f262` |
| jfrog-cli-core | `b47892ded3a0` |
| Artifactory open source edition (mirror) | 6.20.0, `b99e5649bcad` |
| jfrog/build-info (Java) | `4f0d5407279e` |
