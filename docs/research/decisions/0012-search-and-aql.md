# 0012: Search and AQL

Status: Proposed

## Context

Artifactory Query Language is how the JFrog CLI resolves every file specification
(`jf rt u/dl/s/cp/mv/del/sp` all post AQL) and how users search. The grammar covers
domains (`items`, `properties`, `builds` and more), criteria with comparison and
pattern operators, property criteria, includes, sorting, offset, limit and transitivity.
The GET search endpoints (quick, property, checksum, pattern, GAVC, dates, versions) are
specialisations of the same queries. The metadata lives in SQLite and PostgreSQL.

## Decision

`pkg/aql` is a parser for the AQL grammar in
[02-artifactory-core-api-inventory](../02-artifactory-core-api-inventory.md) and a
compiler to SQL per dialect. The `items`, `properties` and `builds` domains are supported
with the dotted secondary domains (`property`, `stat`, `artifact.module.build`); the
deprecated `archive` domains answer the documented parse error.

Dialect traps are handled explicitly:

- `$match` and `$nmatch` are case-sensitive; SQLite uses `GLOB` (or `LIKE` with
  `PRAGMA case_sensitive_like`) with `*` and `?` escaped, PostgreSQL uses `LIKE` with an
  `ESCAPE` clause.
- Each `@key` criterion compiles to its own `EXISTS` over the property table; `{"@*":v}`
  is an any-key `EXISTS`.
- `.include("property")` explodes rows, so sort, offset and limit are applied to the
  primary-domain query first, matching Artifactory's restriction.
- `$before` and `$last` relative dates are resolved at compile time.
- `distinct` defaults to true.

The query shape the JFrog CLI generates (`$or` of `$and` groups with `repo`, `path`,
`name` matches, `type: any`, `path != .`, property objects, the fixed include list and the
suffix order include, sort, offset, transitive, limit) is captured as fixtures from the CLI
and is the first thing the parser must pass. `.transitive()` and the `scan-factor`
header are accepted and ignored.

The GET search endpoints build AQL internally and render their documented response shapes.
The body limit (6,000 characters) and the authenticated-only rule are enforced. Non-admin
callers are limited to the `items` domain and must include `repo`, `path` and `name`.

## Rationale

A real parser is the only way to serve the CLI's queries and users' queries from one
code path and to test them against both dialects. Compiling to SQL keeps the item table as
the single source of truth and avoids a second index.

**Alternatives considered:**
- *Pattern-matching the CLI's queries only*: fragile and useless to users.
- *A separate search engine*: another process to run, against the single-binary goal.

## Constraints

- The `builds` domain depends on build-info ([0014](0014-build-info.md)).
- Sorting on secondary-domain fields is not supported, as in Artifactory.
- Query cost is bounded by `limit`; a query without a limit is capped at the documented
  hard limit with `range.notification`.

## Verification

- Parser tests over the documented syntax, including error cases.
- Compiler fixtures run on SQLite and PostgreSQL with identical ordered output.
- CLI-generated queries captured as fixtures and replayed.
- GET search endpoint response-shape tests.

## References

- [02-artifactory-core-api-inventory](../02-artifactory-core-api-inventory.md), Searches and AQL
- [09-jfrog-cli-compatibility](../09-jfrog-cli-compatibility.md), AQL row
- <https://docs.jfrog.com/artifactory/docs/artifactory-query-language>
