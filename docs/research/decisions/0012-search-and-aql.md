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
- **`releases` and `release_artifacts`** depend on release bundles v1
  ([0023](0023-release-bundles-and-distribution.md)). Until that product serves them, they
  answer 400 naming the domain as unsupported.
- **`item.infos`** answers the same, because the item store keeps no record of property
  modifications.
- **The deprecated `archive` domain and `entry` fields** answer the documented parse error.

**Remote and virtual repositories.** A `repo` criterion follows research 22 §1.4:
- **A remote's key matches its cached items** without `.transitive()`.
- **`<remote>-cache` matches the same items.**
- **Result rows report `<remote>-cache`.** The CLI's follow-up `GET` of that path then
  reaches the cache.
- **A virtual repository's key expands** to its local members and its remote members'
  caches, and `virtual_repos` is added to the rows implicitly.
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
  - A token scoped `build:<pattern>:r` may query `builds`, and must include `name`,
    `number` and `repo`.
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
  would change.
- **Delete** needs delete permission on each item and goes through the trash can, as
  `DELETE` does ([0010](0010-retention-trash-and-gc.md)).
- **Update** needs annotate permission on each item and goes through the item service, so
  webhooks and indexes see the change.
- **Unpermitted items** are skipped and left out of the response.

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

**GET searches.** The GET search endpoints build AQL internally and render their documented
response shapes (research 22 §2), including `X-Result-Detail`. Quick search and property
search allow anonymous callers, and every other search needs an authenticated caller.

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
- *No default result cap:* matches self-managed Artifactory, but leaves memory unbounded.

## Constraints

- The `builds` domain depends on build-info ([0014](0014-build-info.md)).
- Sorting on secondary-domain fields is not supported, as in Artifactory.
- No JFrog page shows how `$ne`, `$match` and `$nmatch` treat a remote or virtual key, so
  they apply to the expanded set of repository keys. This is to verify against a live
  Artifactory.

## Verification

- Parser tests over the documented syntax, including error cases.
- Compiler fixtures run in memory and on SQLite and PostgreSQL, with identical ordered
  output.
- CLI-generated queries captured as fixtures and replayed, including `download_remote` for
  a remote's cache.
- Dry-run and real-run tests for both actions, including skipped items.
- GET search endpoint response-shape tests.

## References

- [22-aql-searches-build-info](../22-aql-searches-build-info.md)
- [02-artifactory-core-api-inventory](../02-artifactory-core-api-inventory.md), Searches and AQL
- [09-jfrog-cli-compatibility](../09-jfrog-cli-compatibility.md), AQL row
- <https://docs.jfrog.com/artifactory/docs/artifactory-query-language>
