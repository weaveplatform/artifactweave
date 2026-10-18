# 0012: Search and AQL

Status: Proposed

Checked against: **Artifactory 7.161.32** on 2026-10-17 ([research 22 section 6](../22-aql-searches-build-info.md#6-artifactory-7x-check), [#341](https://github.com/weaveplatform/artifactweave/issues/341)); audited against KB, Jira and client sources on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)).

## Context

Artifactory Query Language is how the JFrog CLI resolves every file specification
(`jf rt u/dl/s/cp/mv/del/sp` all post AQL) and how users search. The grammar covers
domains (`items`, `builds` and more), criteria with comparison and pattern operators,
property criteria, includes, sorting, offset, limit and transitivity, plus two actions that
change items. The GET search endpoints (quick, property, checksum, pattern, GAVC, dates,
versions) are specialisations of the same queries. The metadata lives in SQLite and
PostgreSQL. [Research 22](../22-aql-searches-build-info.md) holds the facts this record
relies on.

## Decision

`pkg/aql` is a parser for the AQL grammar in
[02-artifactory-core-api-inventory](../02-artifactory-core-api-inventory.md) and a
compiler to SQL per dialect.

**Domains.** The domains and fields are those in research 22 §1.1:
- **Primary domains:** `items`, `builds` and `build.promotions`.
- **Secondary domains,** named by their full relation path from the primary domain in
  either direction: `property` (the properties of whichever item, module or build the path
  reaches), `stat`, `module`, `artifact`, `dependency` and `item`. For example,
  `artifact.module.build.name` from items, and `module.artifact.item.name` from builds.
- **`$msp`** works on item, module and build properties.
- **How paths join**, as Artifactory's SQL joins its tables (research 22 §1.1):
  - an item relates to the build artifacts and dependencies whose SHA-1 is its actual
    SHA-1; files relate to their module, modules to their run, promotions to their run;
  - each relation path is joined once per query, so every criterion on one path meets the
    same row: `artifact.module.build.name` and `artifact.module.build.number` must hold for
    one run, which is what the JFrog CLI's `--build` searches rely on;
  - a path that reaches no row reaches a null row, as an outer join does. No criterion holds
    on it, `$ne` and a negative property criterion included, but an `$or` alternative on
    the found row can still match;
  - a dependency's `name` is its id and its `scope` its scopes joined with `,`, as
    Artifactory stores them.
- **Builds and promotions rows** render `build.<field>` and `build.promotion.<field>` in
  alphabetical order, leaving out empty values; every field is a default. A build's `repo`
  is its project's build-info repository.
- **`releases` and `release_artifacts`** depend on release bundles v1
  ([0023](0023-release-bundles-and-distribution.md)). Until that product serves them, they
  answer 400 naming the domain as unsupported.
- **`item.infos`** answers the same, because the item store keeps no record of property
  modifications.
- **The deprecated `archive` domain and `entry` fields** answer the documented parse error.

**Remote and virtual repositories.** A `repo` criterion follows research 22 §1.4. Its keys
resolve against the configured repositories before the query runs, on every engine:
- **A remote's key matches its cached items** without `.transitive()`.
- **`<remote>-cache` matches the same items.** A key ending in `-cache` names a cache only
  when no repository has that key, so a local repository called `x-cache` keeps its own.
- **Result rows report `<remote>-cache`.** The CLI's follow-up `GET` of that path then
  reaches the cache.
- **A virtual repository's key expands** to its local members and its remote members'
  caches, through nested virtual repositories. A member that leads back to a virtual
  repository already expanded is skipped. An `$eq` naming a virtual repository adds
  `virtual_repos` to the rows implicitly, beside the default fields.
- **A key no repository has** keeps the plain comparison.
- **Operators.** `$eq` holds for an item in the expansion of its key, and `$ne` for an item
  outside it. `$match` holds for an item whose reported key matches the pattern, or that
  lies in the expansion of a configured key the pattern matches; `$nmatch` holds exactly
  when `$match` does not. So `"libs-*"` reaches what `libs-virtual` stands for under
  `$match`, and excludes it under `$nmatch`. The ordering operators compare the reported
  key, like any text field.
- **`.transitive()`** is accepted and has no effect, so a query gets the same rows as
  without it (`pkg/aql/aql.go`, `Query.Transitive`). artifactweave has no Smart Remote
  search to make it transitive: Smart Remote configuration is accepted and ignored
  ([0007](0007-remote-repositories.md)). In Artifactory it adds items found on Smart Remote
  members, for the first five remote members of a virtual repository, and restricts the
  query: no `.sort()` or `.offset()`, an `.include()` of item and item property fields
  only, and a single repository named with `$eq`
  ([aql-repository-queries](https://docs.jfrog.com/artifactory/docs/aql-repository-queries.md),
  "Transitive Query Limitations"; research 22 §1.4). artifactweave enforces none of these
  restrictions, since the suffix changes nothing.

**Output.** The include, sort, offset and limit rules follow research 22 §1.2:
- naming an item field replaces the defaults, and property or statistic fields add to them;
- `sort`, `offset` and `limit` are accepted whatever the include, and apply to the items, as
  **Exploding includes** below sets out; a sort names only fields in the output;
- `distinct` defaults to true, and `.distinct(false)` turns it off;
- `?compact=true` minifies the response;
- a `stream: true` header is accepted, and the response shape stays the same.

**Limits and permission filtering.**
- **Limit before filtering.** `.limit(N)` reads N rows before permission filtering, as
  Artifactory does, so a non-admin may receive fewer than N.
- **Scan factor.** The `scan-factor` request header multiplies the rows read for that
  request. The configuration key `search.aql.non_admin_scan_factor` sets the default
  factor, 1.
- **Result cap.** `search.aql.max_results` caps every query, by default at 500,000 results;
  0 removes the cap. When the cap trims results, `range.notification` carries the
  documented text: "AQL query reached the search hard limit, results are trimmed. Please
  address configurable limit."
- **Restrictions on non-admins.**
  - Without a scoped token, a non-admin may query only `items`, and must include `repo`,
    `path` and `name`.
  - A token scoped `build:<pattern>:r` may query `builds` and `build.promotions`, and a
    `builds` query must include `name`, `number` and `repo`. The rows are the runs, or the
    promotions of runs, the caller may read under the build permission on the run's
    document path ([0014](0014-build-info.md)), narrowed by the token's pattern on that
    path. A build scope reaches no artifact.
  - Results honour each repository's include and exclude patterns.

**Errors.**

| Status | When |
|---|---|
| 400 | an empty query, a body over the size limit (`AQL query is too long`, 6,000 characters), or a syntax or validation error |
| 403 | an anonymous caller |
| 408 | the database timeout cancels the query |
| 429 | the concurrent-query limit is reached. Not sent, and unconfirmed for 7.x: artifactweave sets no such limit. [7.146.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71467-self-managed) added "concurrency limits at both global and per-user levels", but the documented switch, `artifactory.aql.queries.limit.enabled`, has defaulted to `false` since 7.84.16 ([aql-performance](https://docs.jfrog.com/artifactory/docs/aql-performance.md), "How Do I Configure AQL Rate Limiting?"), so a default 7.x instance may not throttle at all ([#216](https://github.com/weaveplatform/artifactweave/issues/216)) |

**Actions.**
- **`items.delete(<criteria>).dryRun(...)`** and
  **`properties.update(<criteria>).keys(...).newValue(...).dryRun(...)`** answer in the
  `find` shape: the affected rows and a `range`. A dry run therefore lists what a real run
  would change. Rows carry the default item fields; `properties.update` rows add the keys
  it changes, with their new values.
- **A dry run by default.** An action without `.dryRun()` is a dry run. `.dryRun()` takes
  `"true"` or `"false"` in any case, or a JSON boolean; anything else answers 400.
- **Criteria.** `items.delete` takes `find`'s criteria, with the same implicit
  `type: file`. `properties.update` takes `item.<field>` criteria, through `$and` and
  `$or`, and adds no implicit type, because folders carry properties too. The properties
  domain's own `key` and `value` fields answer 400 as unsupported.
- **Suffixes.** `items.delete` takes only `.dryRun()`, and `properties.update` takes
  `.keys()`, `.newValue()` and `.dryRun()`, the first two required. `sort`, `offset` and
  `limit` answer 400, as JFrog documents for delete
  ([aql-syntax](https://docs.jfrog.com/artifactory/docs/aql-syntax.md), "Limitations").
  `include` answers 400 too, which JFrog does not document: Artifactory 6.20's grammar
  accepts `.include()` after both actions (`DomainElement.getChainByDomain`), and whether
  7.x does is to verify against a live Artifactory
  ([#216](https://github.com/weaveplatform/artifactweave/issues/216)).
- **Limits.** The result cap and the scan factor apply as for `find`. A capped action
  changes only the rows it returns, with the cap's notification, and running it again
  reaches the rest.
- **Delete** needs read and delete permission on each item and goes through the trash can,
  as `DELETE` does ([0010](0010-retention-trash-and-gc.md)). A folder takes everything
  below it, as `DELETE` does, and an item it took is still listed.
- **Update** affects an item that has at least one of the keys. Each key it has gets the
  single new value, and keys it lacks are not added. It needs read and annotate permission
  on each item, and writes as set-properties does, in one transaction.
- **Unpermitted items** are skipped and left out of the response, as are items an action
  cannot reach: a virtual repository's own, and a remote repository's outside its cache.
  For a non-admin, the repository's include and exclude patterns apply as for `find`.

**Dialect traps** are handled explicitly:
- **Pattern operators.** `$match` and `$nmatch` are case-sensitive.
  - SQLite uses `GLOB`, whose `*` and `?` are AQL's wildcards, with `[` escaped so it
    cannot open a character class.
  - PostgreSQL uses `LIKE` with an `ESCAPE` clause. The escape character, `%` and `_` are
    escaped, then `*` becomes `%` and `?` becomes `_`.
- **Property criteria.** Each `@key` criterion compiles to its own `EXISTS` over the
  property table, and `{"@*":v}` is an any-key `EXISTS`.
- **Exploding includes.** Joining properties into the item query would give one row per
  item and property, so they are not joined. The item query is sorted, offset and limited on
  its own, and the store then loads the properties and statistics of the items it returned.
  - **Each result row is one item,** with all of its properties and statistics inside it,
    whatever the include.
  - **`.sort()`** orders the items by the item fields it names.
  - **`.offset(N)`** skips the first N items in that order, and `range.start_pos` is N.
  - **`.limit(N)`** returns at most N items, each with all of its properties.
  - **Not Artifactory's rows.** Beside a property or statistic include, Artifactory ignores
    the sort and counts its joined item-property rows, so its limit can return fewer items
    than N ([DV-074](../../implementation/deviations.md#api-behaviour)).
- **Relative dates.** `$before` and `$last` are resolved at compile time.

**Fixtures.** The query shape the JFrog CLI generates is captured as fixtures from the CLI
and stays the first thing the parser must pass:
- `$or` of `$and` groups with `repo`, `path` and `name` matches;
- `type: any` and `path != .`;
- property objects;
- the fixed include list;
- the suffix order: include, sort, offset, transitive, limit.

**GET searches.** The GET search endpoints build AQL internally, run it through the item
store's search, and render their documented response shapes (research 22 §2), including
`X-Result-Detail`.
- **Callers.** Quick, property, checksum, bad checksum, GAVC, versions, latest version and
  dependency search allow anonymous callers, as JFrog's page for each says. Creation, dates,
  usage, pattern, latest version by properties and build artifacts search need a named user
  and answer an anonymous caller 401; build artifacts search does so although its page says
  "can be anonymous", because Artifactory's handler refuses an unauthenticated caller. Results are what the caller may read,
  filtered as AQL's are, so a caller who may read nothing gets no results rather than a 403.
- **`repos`.** A remote's key and its `-cache` key both search the cache, and results
  report the cache key. Quick, creation, dates, usage and pattern search ignore a virtual
  key; property, checksum and bad checksum search its members. A key that names no
  repository finds nothing, except in creation search, which answers its documented 404
  for a key naming no repository the caller may see. Pattern search treats the key in its
  pattern the same way: one naming no local repository or remote cache finds no files.
- **What is searched.** Files only, except property search, which finds folders too. Quick
  search matches the name case-sensitively, as `$match` does. Bad checksum search compares
  the declared and computed checksums itself, because AQL cannot compare two fields; a
  checksum the client never declared counts as bad. Time windows include both ends.
  Pattern search's `*` and `?` stay within a segment, and `**` or a trailing `/` is a 400.
- **Limits.** `search.user_query_limit` (1,000) caps quick, property, checksum, usage,
  dates, creation and GAVC search for anonymous callers, and for every non-admin when
  `search.limit_anonymous_users_only` is false, as Artifactory's
  `artifactory.search.userQueryLimit` and `limitAnonymousUsersOnly` do. Bad checksum search
  stops at its documented 10,000, and `search.aql.max_results` caps every search, the files
  versions and latest version read included. Results past a limit are dropped silently,
  since these responses have no `range` to report it.

**Layout searches.** GAVC, versions and latest version read Maven coordinates from each
file's path through its repository's layout (`pkg/layout`), so Maven, Gradle, Ivy, sbt and
custom layouts follow one rule.
- **Which files.** Files only, descriptors such as POMs included, in repositories whose layout
  defines the organisation, module and base revision tokens. A layout that does not compile
  reads nothing.
- **Coordinates.** `g` matches the dotted organisation, `a` the module and `c` the
  classifier. A file's version is its base revision with its folder integration revision,
  or its file's when the folder has none: `1.0-SNAPSHOT` for every unique snapshot in that
  folder. Its file version prefers the file integration revision: `1.0-20260101.120000-1`.
  Parameters take `*` and `?` and match case-sensitively.
- **Narrowing.** The query takes the files under the start each layout renders for `g` and
  `a`, up to its first optional group or token without a value; the layout's match on each
  file decides. No layout pattern is translated to SQL.
- **GAVC** needs one of `g`, `a`, `v` and `c`.
  - Without `specific` it searches local repositories and caches and answers `uri` results,
    widened by `X-Result-Detail`.
  - With `specific=true`, `repos` may name virtual repositories, searched through their
    members. Each entry has `downloadUri` under the first key in `repos` that reaches the file,
    else under the file's own key, with `ext`, `version` and a `classifier` only when there is
    one. `X-Result-Detail` is ignored.
- **Versions and latest version** both answer from one list, as Artifactory's handlers
  (`ArtifactVersionsSearchResource`, `ArtifactLatestVersionSearchResource`) both read its
  search add-on's `getArtifactVersions`. Both need `g` or `a`, and `repos` takes virtual keys.
  - **The list** is the module's versions, each once, newest first in Mercury order
    (`types.CompareMercury`), the order of Artifactory's `MavenVersionComparator`. It ranks
    `1.0-SNAPSHOT` < `1.0-alpha-1` < `1.0-rc1` < `1.0` < `1.0-sp1`; Maven's own
    `ComparableVersion`, which `types.CompareMaven` follows, puts SNAPSHOT above `rc`.
  - **`v` without `*` or `?`** is a module version, and the list holds only its integration
    versions, by file version: `v=1.0-SNAPSHOT` lists `1.0-20260101.120000-1` and its
    siblings, and a release such as `v=1.0` lists nothing. The add-on's documentation says `v`
    "relates to integration versions only", and JFrog's pages and examples agree.
  - **Otherwise** every version counts. Versions lists an integration version by module
    version, so a folder of unique snapshots is one `1.0-SNAPSHOT`; latest version lists it by
    file version. A `v` with wildcards is applied to the list afterwards, as the handlers'
    `PathMatcher` filter is.
  - An empty list is a 404, `Unable to find artifact versions`, from both.
- **Versions** answers the list, filtered by a wildcard `v`, with whether each entry is an
  integration version. A wildcard matching nothing answers an empty list.
- **Latest version** answers `text/plain`, the first entry of the list that fits:
  - without `v`, the first release, else 404 `Latest release version not found`;
  - with a wildcard `v`, the first entry of either kind it matches, else 404 `Latest
    integration version not found`, the handler's message even when it wanted a release;
  - with any other `v`, the first integration entry, else the same 404.
- **`remote=1`** is accepted and answered from local repositories and caches, as `remote=0`
  is, until remote repositories proxy their upstream (P9). Any other value is a 400.

**Latest version by properties.** `GET /api/versions/{repo}/{path}` answers the highest value
of the lowercase `version` property, in the versions searches' Mercury order, among the files
the caller may read in
`repo` under the folder `path`.
- `_any` stands for every local repository and cache, or every path. A remote key stands for
  its cache and a virtual key for its members.
- Every query parameter but `listFiles` is a property the files must also carry, read as
  property search reads them. A file's every `version` value counts.
- `artifacts` is empty unless `listFiles=1`, when it lists the files that carry the winning
  version, by download URL, as JFrog's examples show. `listFiles` other than 0 or 1 is a 400.
- No file carrying the property is a 404.

**Build searches.**
- **Dependency search** takes a SHA-1 of 40 hex characters in either case; anything else is a
  400. It answers one `uri` under `/api/build` per build number with a run the caller may
  read whose dependencies include that SHA-1, in the order the runs were published, with
  `?project=` for another project's build.
- **Build artifacts search** reads the default project's builds, and follows Artifactory's
  `BuildArtifactsSearchResource` (OSS 6.20).
  - The body is checked before the caller, with Artifactory's messages: a blank `buildName`
    is a 400 `Cannot search without build name.`; neither `buildNumber` nor `buildStatus` is
    a 400 `Cannot search without build number or build status.`; both is a 400 naming both.
    Blank means empty or white space only. Then an anonymous caller is a 401.
  - The run is the most recently started run of `buildNumber`, or of the build for `LATEST`;
    or, given `buildStatus` instead, the most recently started run whose latest promotion
    has exactly that status; among the runs the caller may read. When the caller may read
    none of the runs that qualify, it is the build-info API's refusal.
  - The files are found as a promotion finds them, as in Artifactory (research 22 §3.6):
    for each artifact, of the items in `repos` that hold its checksums and carry the run's
    `build.name` and `build.number`, the one with its name, else the first. In `repos` a
    remote key stands for its cache and a virtual key for its members; without `repos` every
    repository counts. A file the caller may not read is left out. `mappings` keep the files
    whose whole path matches any `input`, an RE2 expression; an empty `input` is ignored and
    an invalid one is a 400. `GET /api/builds/buildArtifacts/...` still matches by checksum
    alone, since the JFrog CLI ranks its matches itself.
  - Finding no file, whether no run qualifies or its filters leave nothing, is a 404
    `Could not find any build artifacts for build '<name>'`, followed by ` number '<n>'` or,
    without a number, ` status '<s>'`.

## Rationale

A real parser is the only way to serve the CLI's queries and users' queries from one
code path and to test them against both dialects. Compiling to SQL keeps the item table as
the single source of truth and avoids a second index.

**Sort, offset and limit on the items, whatever the include,** gives the JFrog CLI what its
flags ask for: `--offset N` skips N files, and `--sort-by` orders files. Counting joined
item-property rows is a limitation JFrog documents, and its own client works around it by
leaving `property` out of the include for a sort or a limit and fetching properties in a
second query. Refusing the query, as artifactweave first did, broke `jf rt s --offset` and
`jf rt dl --offset` (#386), which keep `property` in the include. The rows differ from
Artifactory's, so this is a deviation
([DV-074](../../implementation/deviations.md#api-behaviour)).

**Remote keys matching their caches** is what makes `jf rt dl <remote>/<path>` work once a
file is cached, which JFrog documents as the way to download from a remote (#68).

**Actions answering in the `find` shape**, with per-item permissions, keeps AQL consistent
with the operations it batches. A dry run shows exactly what would be touched, and an AQL
delete or update can do nothing that `DELETE` or set-properties would refuse. JFrog
documents neither choice (research 22 §4). Artifactory 6.20 answers in the `find` shape
too, and leaves out the rows whose action failed (`AqlJsonStreamer.actOnRow`, research 22
§1.5).

**A dry run by default** is what Artifactory 6.20 does: `AqlDeleteItemAction` and
`AqlUpdateItemPropertyAction` both start with `dryRun = true` (research 22 §1.5). JFrog's
7.x pages document no default, and of the two only a dry run leaves a mistyped or
forgotten suffix harmless. Read permission is required beside delete or annotate because
every row the response lists is one the caller then sees.

**Coordinates through the layout**, rather than parsing Maven paths, is what JFrog
describes: the search works for any layout with the tokens it needs. Narrowing by the
rendered start of a path keeps the dialects' only job to a `$match`, which the parity
fixtures already cover, and leaves the regular expressions of custom tokens to Go.

**Following Artifactory's code for versions, latest version and build artifacts** (#214,
#215): where JFrog's pages and the handlers in Artifactory's open-source edition disagree,
clients were written against the server, so the handlers win. The handlers name the
comparator, the 404 messages, which `v` takes which branch, and the build artifacts checks;
what the closed add-on does inside `getArtifactVersions` is read from its documentation
comment, JFrog's examples and a JFrog knowledge-base capture. That capture shows
`latestVersion?v=*SNAPSHOT*` answering 404 `Latest integration version not found` for unique
snapshots and `1.0-SNAPSHOT` for a non-unique one, which only file versions explain, and
`v=1.0-SNAPSHOT` answering `1.0-20220406.094243-3`.

**The folder version for GAVC and versions** groups a unique snapshot's files under the
`1.0-SNAPSHOT` a Maven user asks for. JFrog's versions example lists `1.0-SNAPSHOT` that way;
nothing shows the add-on listing unique snapshots one by one without `v`.

**Mercury order** is what `MavenVersionComparator` wraps
(`VersionComparators.getVersionComparator("mercury")`), the only version comparator in
Artifactory's open-source code, and the one `ArtifactVersionsResult` sorts with. JFrog's
versions example, which lists `1.0-SNAPSHOT` above `1.0`, contradicts it, as it does Maven.

**The default result cap** protects the server's memory from an unbounded query, and is the
value JFrog SaaS uses. Self-managed Artifactory has no default cap, so this is a deviation
([DV-030](../../implementation/deviations.md#api-behaviour)). Operators who want
Artifactory's behaviour set `search.aql.max_results: 0`.

**Alternatives considered:**
- *Pattern-matching the CLI's queries only:* fragile and useless to users.
- *Counting joined item-property rows beside a property include, as Artifactory does:* a
  page could end inside an item's properties, `--offset 1` would skip one property of the
  first file rather than the file, and a limit would return fewer files than it names.
- *Refusing sort, offset and limit beside a property or statistic include:* Artifactory
  accepts the query, and `jf rt s --offset` and `jf rt dl --offset` send it.
- *A separate search engine:* another process to run, against the single-binary goal.
- *Actions answering a count only:* a dry run could not show which items it would touch.
- *Actions for administrators only:* stricter than the per-item operations they batch, for
  no gain.
- *A real run when `.dryRun()` is left out:* one forgotten suffix would delete or rewrite
  everything the criteria match.
- *`properties.update` adding keys an item lacks:* the update would then reach every item
  the criteria match, which no JFrog example asks for. Artifactory 6.20 updates only a key
  the item already has (`PropertiesServiceImpl.editProperty`, research 22 §1.5).
- *No default result cap:* matches self-managed Artifactory, but leaves memory unbounded.
- *GAVC and versions only on Maven paths:* simpler, but wrong for Gradle, Ivy and custom
  layouts, which JFrog says the searches support.
- *Answering `remote=1` 501 until P9:* would refuse a request whose local answer is still
  correct for what is cached.
- *Maven's `ComparableVersion` order for the versions searches:* what Maven itself resolves
  with, but not what Artifactory answers when a snapshot and a pre-release of one base
  version coexist.
- *Build artifacts search allowing anonymous callers, and answering 200 with no results:*
  what JFrog's page implies, but not what Artifactory's handler does.

## Artifactory 7.x check

The decision stands against 7.161.32
([research 22 section 6](../22-aql-searches-build-info.md#6-artifactory-7x-check)), except
where a line below says 7.x differs. Its limits, statuses and `scan-factor` come from the
7.x pages. The pattern rule for non-admins matches 7.146.7, which made AQL honour
repository include and exclude patterns "so AQL results align with ... other search
methods". The handlers this decision read from 6.20 (References) remain unconfirmed for
7.x.

`sort`, `offset` and `limit` beside a property or statistic include are accepted, as 7.x
accepts them. JFrog's page says they "will not work" there and shows an "Example where
sorting is ignored", not a refusal
([aql-query-output](https://docs.jfrog.com/artifactory/docs/aql-query-output.md)). The
JFrog CLI depends on it: jfrog-client-go keeps `property` in the include unless a spec has
a sort or a limit (`includePropertiesInAqlForSpec`), so `jf rt s --offset N` and
`jf rt dl --offset N` send `.offset(N)` beside it. What 7.x then returns differs, as
**Exploding includes** and [DV-074](../../implementation/deviations.md#api-behaviour)
record: the same page says a limit can return "fewer artifacts than N when one artifact has
several properties", and a 7.21.7 user report shows the sort ignored
([RTFACT-26346](https://jfrog.atlassian.net/browse/RTFACT-26346)). How 7.x counts an
offset there is inferred ([research 22 §1.2](../22-aql-searches-build-info.md#12-output-sorting-and-limits);
[#216](https://github.com/weaveplatform/artifactweave/issues/216)).

The audit against KB, Jira and client sources on 2026-10-18
([#378](https://github.com/weaveplatform/artifactweave/issues/378)) found these
differences. Each issue named fixes the code, and corrects this record's text when it
lands:
- 7.x differs: a query over the size limit answers 400 `AQL query is too long; please
  reduce the query length to less than 6000 chars`, the number being the configured limit
  ([aql-performance](https://docs.jfrog.com/artifactory/docs/aql-performance.md)); fixed by
  [#410](https://github.com/weaveplatform/artifactweave/issues/410).
- 7.x differs: the database cancels a REST query after
  `artifactory.aql.query.timeout.seconds` (900 s; 7,200 s with `stream: true`) and
  Artifactory answers 408, where artifactweave sets no timeout and never sends the 408 in
  **Errors** ([aql-performance](https://docs.jfrog.com/artifactory/docs/aql-performance.md),
  "What Are the Query Timeout Defaults?"); fixed by
  [#411](https://github.com/weaveplatform/artifactweave/issues/411).
- 7.x differs: `$before` and `$last` also take the long units `mills`, `seconds`, `days`,
  `weeks`, `months` and `years`, where `pkg/aql` takes only `ms`, `s`, `minutes`, `d`, `w`,
  `mo` and `y` ([aql-search-criteria](https://docs.jfrog.com/artifactory/docs/aql-search-criteria.md),
  "Relative Time Operators in AQL"); fixed by
  [#410](https://github.com/weaveplatform/artifactweave/issues/410).
- 7.x differs: `find(...)` takes several criteria objects separated by commas, as an
  implicit `$and`, where `pkg/aql` answers a syntax error
  ([aql-search-criteria](https://docs.jfrog.com/artifactory/docs/aql-search-criteria.md),
  "Compounding Criteria"); fixed by
  [#410](https://github.com/weaveplatform/artifactweave/issues/410).
- 7.x differs: an `items` query may include fields of any related domain, such as
  `artifact.module.build.*`, and results nest them, where `pkg/aql` answers 400 for such an
  include on every domain, not only the two **Constraints** names
  ([aql-query-output](https://docs.jfrog.com/artifactory/docs/aql-query-output.md);
  [aql-query-execution](https://docs.jfrog.com/artifactory/docs/aql-query-execution.md):
  "you may include any other accessible field from any domain"); fixed by
  [#410](https://github.com/weaveplatform/artifactweave/issues/410).
- 7.x differs: `item.infos.find(...)` is a primary domain
  ([aql-entities-fields-reference](https://docs.jfrog.com/artifactory/docs/aql-entities-fields-reference.md),
  "Item Info Domain Fields"), where `pkg/aql` answers the syntax error `unknown domain
  "item"`, not the unsupported-domain 400 **Domains** promises; fixed by
  [#410](https://github.com/weaveplatform/artifactweave/issues/410).

Two differences are kept on purpose, because **Build searches** follows Artifactory 6.20's
`BuildArtifactsSearchResource`. Each is a deviation registered by #378, to verify against a
live Artifactory ([#216](https://github.com/weaveplatform/artifactweave/issues/216)):
- 7.x differs: the build artifacts search's schema requires `buildNumber` and makes
  `buildStatus` an optional filter beside it, and its example sends `"buildNumber":
  "LATEST"` with `"buildStatus": "Released"`, where artifactweave answers 400 to both
  ([searchbuildartifacts](https://docs.jfrog.com/artifactory/reference/searchbuildartifacts.md)).
- 7.x differs: the same page says the search "Requires a privileged user (can be
  anonymous)", where artifactweave answers an anonymous caller 401
  ([searchbuildartifacts](https://docs.jfrog.com/artifactory/reference/searchbuildartifacts.md)).

## Constraints

- The `builds` domain depends on build-info ([0014](0014-build-info.md)).
- Sorting on secondary-domain fields is not supported, as in Artifactory.
- Including fields of another domain in a `builds` or `build.promotions` result, which
  Artifactory nests (`modules[].artifacts[].items[]`), is not served yet: such an include
  answers 400 as unsupported. Criteria on those domains are served.
- No JFrog page shows how `$ne`, `$match` and `$nmatch` treat a remote or virtual key.
  They are defined above as the complements of `$eq` and `$match` over the expanded
  locations, which is to verify against a live Artifactory.
- `remote=1` does not consult the upstream until P9 (#213).
- The handlers read are Artifactory 6.20's (`github.com/knottnhupfer/artifactory-oss`,
  `b99e5649bcad`), and how the closed add-on narrows by `v`, and whether it lists unique
  snapshots by module version without `v`, is inferred. Both are to verify against a live
  Artifactory (#216).

## Verification

- Parser tests over the documented syntax, including error cases.
- Compiler fixtures run in memory and on SQLite and PostgreSQL, with identical ordered
  output.
- CLI-generated queries captured as fixtures and replayed, including `download_remote` for
  a remote's cache, and shared cases for remote, cache, nested and cyclic virtual keys
  under every operator.
- Shared build fixtures (`pkg/aql/aqltest`) for every relation path in both directions,
  the CLI's `--build` query shapes, and the `builds` and `build.promotions` domains, with
  identical ordered rows from the reference evaluator, the in-memory store, SQLite and
  PostgreSQL.
- An acceptance round trip: `jf rt s --build` and `jf rt dl --build` find the files a
  published build produced, and nothing else.
- An acceptance round trip: `jf rt dl remote/*.jar` downloads a file seeded into the
  remote's cache.
- An acceptance round trip: `jf rt s --offset 1` and `jf rt dl --offset 1` skip the first
  file, which has two properties, and return the rest; shared cases run sort, offset and
  limit beside property, key and statistic includes on every backend.
- Dry-run and real-run tests for both actions, including skipped items, and shared
  fixtures for their criteria run in memory and on SQLite and PostgreSQL.
- GET search endpoint response-shape tests, against Maven, Ivy and broken layouts and
  published builds, with shared fixtures for the layout prefix and the version property
  query shapes run in memory and on SQLite and PostgreSQL.
- Table tests of Mercury order against Mercury's `ComparableVersion` rules, and search tests
  where a release candidate and a snapshot of one base version coexist; a test for each 404
  message and each build artifacts check.

## References

- [22-aql-searches-build-info](../22-aql-searches-build-info.md)
- [02-artifactory-core-api-inventory](../02-artifactory-core-api-inventory.md), Searches and AQL
- [09-jfrog-cli-compatibility](../09-jfrog-cli-compatibility.md), AQL row
- <https://docs.jfrog.com/artifactory/docs/artifactory-query-language>
