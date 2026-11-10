// Package aql parses Artifactory Query Language and runs it (decision 0012).
// It covers the items domain: the shape the JFrog CLI generates for every
// file specification (research 09) and what users write by hand. That is
// $and and $or, field comparisons, $match and $nmatch globs, @property and
// property.key/property.value criteria, $msp, the stat domain, and the
// include, sort, offset, limit, transitive and distinct suffixes with
// Artifactory's output rules (research 22 §1). The builds and
// build.promotions domains are served too, and every domain reaches the
// others through relation paths such as artifact.module.build.name (see
// Joined), which is how the JFrog CLI finds a build's files; an include
// may name those domains' fields too, which Nest fills in (nest.go). It
// also parses the two actions, items.delete and properties.update
// (research 22 §1.5), whose criteria select items the same way; the server
// carries them out. Every repository's root is an items row (Roots).
//
// A parsed Query is run two ways that must agree row for row: Match and
// Compare are the reference semantics (the in-memory store uses them), and
// Compile turns the query into SQL for SQLite or PostgreSQL. The store
// contract suite runs the same queries on every backend and expects the
// same ordered rows. Before either, Resolve rewrites repo criteria against
// the configured repositories, so remote and virtual keys match what they
// stand for.
//
// # Role
//
// artifacts module, core layer: the AQL language and its SQL compiler per
// dialect, with no separate aqlsql adapter. Compile is a method of the
// parsed query and works on the package's unexported join plan, which the
// reference evaluator shares, so a separate package would have to export
// that plan for a single caller (decision 0044, "Artifacts and remote").
// The package imports only the standard library and the shared item and
// build values. It never reaches the module's adapters or an ecosystem http
// package (decision 0044, dependency rule 8), and never the store, which
// imports this package for the signatures of its searches: that is why
// AQLSource takes the two lookups it reads, BuildRuns and ItemSearch,
// rather than a store.
//
// # Inputs
//
//   - AQL text, at most MaxLength characters: the body of the AQL search
//     endpoint, which Parse reads into a Query. The GET searches and the
//     format handlers build a Query in code instead.
//   - The configured repositories (Repositories), which Resolve applies to a
//     query's repo criteria before it runs.
//   - Options: the clock that resolves $before and $last, and the most rows
//     any query returns, the search hard limit.
//   - For the reference evaluator, a Corpus: items with their properties and
//     statistics, build runs with their properties and modules, and
//     promotions.
//   - For an include of related domains, a Source of the rows it reaches:
//     AQLSource over a store's build-run lookups and item search, or a
//     Corpus. Nesting says what the caller may see.
//
// # Outputs
//
//   - Query, the parsed AST, validated against the output rules, or an error
//     that wraps ErrEmpty, ErrSyntax, ErrUnsupported, ErrInvalid or
//     ErrTooLong. CheckRestricted wraps ErrRestricted for a non-admin
//     caller's query whose rows leave out a field the permission check of
//     each row reads.
//   - SQL for SQLite or PostgreSQL (Compile and Compiled): the WHERE clause,
//     then the ORDER BY, LIMIT and OFFSET, with every value bound.
//   - The reference rows: Match, Compare, Select, SelectItems and
//     SelectBuilds, which every store's search must equal row for row.
//   - The response body: Response and BuildResponse render the rows in
//     Artifactory's shape, with the rows of related domains that Nest and
//     NestBuilds load (Related).
//   - The transitive search of a Smart Remote: TransitiveTarget checks a
//     query's shape, and PeerQuery writes the query it sends upstream.
//
// # Invariants
//
//   - The reference evaluator and the SQL Compile writes return the same
//     rows in the same order, ties broken by ID; aqltest's cases hold every
//     backend to it.
//   - Compiled SQL binds every value and never interpolates one.
//   - Error texts reach clients, so they stay byte-identical: ErrTooLong's
//     is Artifactory's message, and the parser's errors name the position
//     (offset) and what it found there.
//   - An action without .dryRun() is a dry run, so a client that omits it
//     destroys nothing.
//   - Nest refuses an include whose combinations of related rows outgrow its
//     budget, rather than working through them.
//
// # References
//
//   - https://docs.jfrog.com/artifactory/docs/artifactory-query-language.md,
//     with its aql-syntax, aql-search-criteria, aql-query-output,
//     aql-entities-fields-reference and aql-performance pages under the same
//     path.
//   - Research 22: docs/research/22-aql-searches-build-info.md, section 1
//     (domains and fields, output and limits, errors and permissions,
//     remote and virtual repositories, actions).
//   - Research 09: docs/research/09-jfrog-cli-compatibility.md, "The
//     generated AQL" (the query shapes the CLI sends).
//   - Decision 0012: docs/research/decisions/0012-search-and-aql.md, from
//     line 26 (the parser and the compiler per dialect) and "Verification"
//     (the shared cases on the reference evaluator and every store).
//   - Decision 0014: docs/research/decisions/0014-build-info.md, lines 13
//     and 102 (the builds domain, and runs joined to items by checksum).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Artifacts and remote" (no aqlsql), "Names the migration introduces"
//     (BuildRuns and ItemSearch) and "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 213 (core/aql) and the mapping row at line 1047, whose aqlsql
//     half decision 0044 overrides.
//   - Plan: docs/implementation/plan.md#p3-repositories-generic-artifacts-cli-aql-shape
//     (task 5), #p5-full-aql-searches-build-info, #p9-remote-repositories
//     (transitive AQL) and #p9d-modular-monolith-migration, epic ART
//     (https://github.com/weaveplatform/artifactweave/issues/941).
//   - https://github.com/weaveplatform/artifactweave/issues/975
//
// # Status
//
// Implemented P3, P5 and P9. P3 added the JFrog CLI's query shape and its
// SQL compiler (https://github.com/weaveplatform/artifactweave/issues/49).
// P5 added the items domain with properties, statistics, $msp, the output
// rules and limits (issue 163), remote and virtual keys (issue 68), the
// builds and build.promotions domains with their relation paths (issue
// 167) and the actions (issue 168). Its follow-ups brought the grammar and
// messages to 7.x's and added the includes of related domains (issue 410),
// of their properties and statistics (issue 494) and of every property
// (issue 506). P9 added the transitive search of a Smart Remote (issue
// 636). Staged in P9d. Roadmap: the local search logic that runs these
// queries moves to artifacts/core/search
// (https://github.com/weaveplatform/artifactweave/issues/1079).
//
// # Testing
//
//   - TestUnitCases and TestUnitBuildCases run aqltest's cases through the
//     reference evaluator; the store contract suite runs the same cases on
//     the in-memory store, SQLite and PostgreSQL, and nests their related
//     rows through AQLSource.
//   - The TestUnitParse* group, TestUnitCriteriaList, TestUnitItemInfos,
//     TestUnitSpanUnits and TestUnitTimesAndSpans: the grammar and its
//     errors.
//   - TestUnitMatchEdges, TestUnitMatchStatEdges, TestUnitJoinedEdges,
//     TestUnitPropertyAndStatCriteria and TestUnitWindow: the reference
//     evaluator.
//   - The TestUnitCompile* group: the SQL each dialect gets.
//     TestUnitGlobTranslations: a glob as the evaluator matches it and as
//     SQLite's GLOB and PostgreSQL's LIKE spell it.
//   - TestUnitResponse, TestUnitResponseSecondaryDomains,
//     TestUnitBuildResponse, TestUnitIncludes, TestUnitEveryKeyIncludes,
//     TestUnitFieldsAndRestrictions and TestUnitCheckRestrictedBuilds: the
//     output rules and the restricted queries.
//   - TestUnitResolve, TestUnitExpand and TestUnitRoots: repository keys and
//     roots.
//   - TestUnitNest, TestUnitNestBudget, TestUnitNestErrors,
//     TestUnitRelatedCases, TestUnitRelatedIncludes and
//     TestUnitRelatedPropertyIncludes: the includes of related domains.
//   - TestUnitTransitiveTargetRequiresEveryAlternative and
//     TestUnitPeerQueryPreservesCriteria: the transitive search.
package aql
