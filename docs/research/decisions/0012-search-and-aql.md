# 0012: Search and AQL

Status: Proposed

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
- **`.transitive()`** adds items found on Smart Remote members, through the remote client
  ([0007](0007-remote-repositories.md)), for the first five remote members, as in
  Artifactory. A query against a repository with no such member gets the same rows as
  without it.

**Output.** The include, sort, offset and limit rules follow research 22 §1.2:
- naming an item field replaces the defaults, and property or statistic fields add to them;
- `sort`, `offset` and `limit` are refused unless the include names only primary-domain
  fields;
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
| 429 | the concurrent-query limit is reached |

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
  `.keys()`, `.newValue()` and `.dryRun()`, the first two required. `include`, `sort`,
  `offset` and `limit` answer 400, as JFrog documents for delete.
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
- **Exploding includes.** `.include("property")` multiplies rows, so sort, offset and limit
  are applied to the primary-domain query first, matching Artifactory's restriction.
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
- **Callers.** Quick, property, checksum, bad checksum, GAVC, versions, latest version,
  dependency and build artifacts search allow anonymous callers, as JFrog's page for each
  says. Creation, dates, usage, pattern and latest version by properties search need a
  named user and answer an anonymous caller 401. Results are what the caller may read,
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
- **Versions** needs `g` or `a`. It answers each module version once, with whether it is an
  integration version, newest first in Maven order. `v` filters them by pattern, so an
  integration pattern keeps only integration versions. `repos` takes virtual keys.
- **Latest version** needs `g` or `a` and answers `text/plain`.
  - Without `v` it is the highest release, in Maven order.
  - A `v` that names an integration version under a file's layout asks for the highest file
    version among that layout's integration files whose version matches `v`. A `v` names one
    when, after one of its hyphens, comes text the layout's folder integration revision
    expression accepts in full, or its file one in a layout without a folder one:
    `1.0-SNAPSHOT` under Maven.
  - Any other `v` filters the releases. Nothing found is a 404, `Unable to find artifact
    versions`.
- **`remote=1`** is accepted and answered from local repositories and caches, as `remote=0`
  is, until remote repositories proxy their upstream (P9). Any other value is a 400.

**Latest version by properties.** `GET /api/versions/{repo}/{path}` answers the highest value
of the lowercase `version` property, in Maven order, among the files the caller may read in
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
- **Build artifacts search** reads the default project's builds.
  - The run is the most recently started run of `buildNumber`, or of the build for `LATEST`,
    whose latest promotion has exactly the `buildStatus` given, among the runs the caller may
    read. No such run is a 404; when the caller may read none of the runs that qualify, it is
    the build-info API's refusal.
  - The files are found as a promotion finds them, as in Artifactory (research 22 §3.6):
    for each artifact, of the items in `repos` that hold its checksums and carry the run's
    `build.name` and `build.number`, the one with its name, else the first. In `repos` a
    remote key stands for its cache and a virtual key for its members; without `repos` every
    repository counts. A file the caller may not read is left out. `mappings` keep the files
    whose whole path matches any `input`, an RE2 expression; an empty `input` is ignored and
    an invalid one is a 400. `GET /api/builds/buildArtifacts/...` still matches by checksum
    alone, since the JFrog CLI ranks its matches itself.
  - A run whose filters leave nothing answers 200 with no results.

## Rationale

A real parser is the only way to serve the CLI's queries and users' queries from one
code path and to test them against both dialects. Compiling to SQL keeps the item table as
the single source of truth and avoids a second index.

**Remote keys matching their caches** is what makes `jf rt dl <remote>/<path>` work once a
file is cached, which JFrog documents as the way to download from a remote (#68).

**Actions answering in the `find` shape**, with per-item permissions, keeps AQL consistent
with the operations it batches. A dry run shows exactly what would be touched, and an AQL
delete or update can do nothing that `DELETE` or set-properties would refuse. JFrog
documents neither choice (research 22 §4).

**A dry run by default** follows from JFrog documenting no default: of the two, only a dry
run leaves a mistyped or forgotten suffix harmless. Read permission is required beside
delete or annotate because every row the response lists is one the caller then sees.

**Coordinates through the layout**, rather than parsing Maven paths, is what JFrog
describes: the search works for any layout with the tokens it needs. Narrowing by the
rendered start of a path keeps the dialects' only job to a `$match`, which the parity
fixtures already cover, and leaves the regular expressions of custom tokens to Go.

**The folder version for GAVC and versions** groups a unique snapshot's files under the
`1.0-SNAPSHOT` a Maven user asks for; latest version's file version is what JFrog's
integration example returns.

**Build artifacts answering 200 with no results** for an existing run matches the build
artifacts endpoint and the other searches. JFrog documents no 404 for it; the spec adds one
for a run that does not exist.

**The default result cap** protects the server's memory from an unbounded query, and is the
value JFrog SaaS uses. Self-managed Artifactory has no default cap, so this is a deviation
([DV-030](../../implementation/deviations.md#api-behaviour)). Operators who want
Artifactory's behaviour set `search.aql.max_results: 0`.

**Alternatives considered:**
- *Pattern-matching the CLI's queries only:* fragile and useless to users.
- *A separate search engine:* another process to run, against the single-binary goal.
- *Actions answering a count only:* a dry run could not show which items it would touch.
- *Actions for administrators only:* stricter than the per-item operations they batch, for
  no gain.
- *A real run when `.dryRun()` is left out:* one forgotten suffix would delete or rewrite
  everything the criteria match.
- *`properties.update` adding keys an item lacks:* the update would then reach every item
  the criteria match, which no JFrog example asks for.
- *No default result cap:* matches self-managed Artifactory, but leaves memory unbounded.
- *GAVC and versions only on Maven paths:* simpler, but wrong for Gradle, Ivy and custom
  layouts, which JFrog says the searches support.
- *Answering `remote=1` 501 until P9:* would refuse a request whose local answer is still
  correct for what is cached.

## Constraints

- The `builds` domain depends on build-info ([0014](0014-build-info.md)).
- Sorting on secondary-domain fields is not supported, as in Artifactory.
- Including fields of another domain in a `builds` or `build.promotions` result, which
  Artifactory nests (`modules[].artifacts[].items[]`), is not served yet: such an include
  answers 400 as unsupported. Criteria on those domains are served.
- No JFrog page shows how `$ne`, `$match` and `$nmatch` treat a remote or virtual key.
  They are defined above as the complements of `$eq` and `$match` over the expanded
  locations, which is to verify against a live Artifactory.
- `remote=1` does not consult the upstream until P9.
- JFrog's versions example lists `1.0-SNAPSHOT` above `1.0`; Maven order, used here, puts it
  below. Both, and latest version's 404 wording, are to verify against a live Artifactory.

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
- Dry-run and real-run tests for both actions, including skipped items, and shared
  fixtures for their criteria run in memory and on SQLite and PostgreSQL.
- GET search endpoint response-shape tests, against Maven, Ivy and broken layouts and
  published builds, with shared fixtures for the layout prefix and the version property
  query shapes run in memory and on SQLite and PostgreSQL.

## References

- [22-aql-searches-build-info](../22-aql-searches-build-info.md)
- [02-artifactory-core-api-inventory](../02-artifactory-core-api-inventory.md), Searches and AQL
- [09-jfrog-cli-compatibility](../09-jfrog-cli-compatibility.md), AQL row
- <https://docs.jfrog.com/artifactory/docs/artifactory-query-language>
