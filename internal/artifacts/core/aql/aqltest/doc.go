// Package aqltest holds the AQL fixtures every search backend must agree
// on (decision 0012): seed items chosen to trip the dialect traps (a GLOB
// character class, LIKE wildcards in names, a multi-byte character, a
// missing time, a remote cache, an item without statistics beside one whose
// statistics are zero), the repositories they live in, the query shapes the
// JFrog CLI generates, and the rows each query returns, in order.
//
// BuildSeeds add build runs and promotions that relate to the items by
// checksum, for the relation-path cases and the builds and
// build.promotions cases (BuildCases).
//
// The aql package's tests run the cases through aql.SelectItems and
// aql.SelectBuilds over Corpus; the store contract suite runs them through
// every ItemStore.Search and BuildStore.Search. Both resolve each query
// against Repositories first, as the server does.
//
// # Role
//
// artifacts module, test support: shared AQL cases every store runs. Only
// tests import it, and the test files of any module may (decision 0044,
// dependency rule 1). It imports the aql package, the shared item and build
// values and the standard library.
//
// # Inputs
//
//   - testdata/*.aql, embedded: the AQL the pinned JFrog CLI sends, one
//     file per command shape. make capture-aql copies them from the
//     acceptance scenario "the query shapes the fixtures capture"
//     (test/acceptance/features/round_trip.feature), which runs one jf
//     command per fixture over a tree laid out like Seeds.
//
// # Outputs
//
//   - Seeds and BuildSeeds: the items, with their properties and
//     statistics, and the build runs with their promotions, in the order a
//     store is to insert them, so that their IDs break ties in every
//     ordering. Base is their clock, and Now the clock the cases run at.
//   - Repositories: the seeds' repositories, with virtual ones over them
//     that nest a remote, another virtual repository and a member nothing
//     configures, and one that names itself.
//   - Hits and Corpus: the seeds as stored, for the reference evaluator.
//   - Cases (the CLI fixtures, then one case per operator and dialect trap,
//     and the queries the GET searches build in code), BuildCases and
//     RelatedCases: each query with the rows it returns, in order.
//     Case.Parse returns the query resolved against Repositories, and Paths
//     and BuildRows render hits the way a case lists its rows.
//
// # Invariants
//
//   - A case lists its rows in order, so a backend that finds the right
//     rows in another order fails it.
//   - The CLI fixtures hold what jf sent, never a query written by hand:
//     when the pinned jf changes, make capture-aql refreshes them and the
//     rows each case expects are checked again (docs/testing/ci.md).
//   - RelatedCases nest rows through an aql.Source, so a store's
//     aql.AQLSource must nest what Corpus does.
//
// # References
//
//   - Decision 0012: docs/research/decisions/0012-search-and-aql.md,
//     "Verification" (the shared cases on the reference evaluator and every
//     store, and the CLI-captured fixtures).
//   - Research 09: docs/research/09-jfrog-cli-compatibility.md, "The
//     generated AQL".
//   - Research 22: docs/research/22-aql-searches-build-info.md, section 1.
//   - docs/testing/ci.md, "AQL fixtures from the JFrog CLI" (make
//     capture-aql).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules" (rule 1).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 214 (core/aql/aqltest), the mapping row at line 1048, and line
//     848 (the port contract suites).
//   - Plan: docs/implementation/plan.md#p3-repositories-generic-artifacts-cli-aql-shape
//     (task 5), #p5-full-aql-searches-build-info (exit criteria) and
//     #p9d-modular-monolith-migration, epic ART
//     (https://github.com/weaveplatform/artifactweave/issues/941).
//   - https://github.com/weaveplatform/artifactweave/issues/975
//
// # Status
//
// Implemented P3 and P5. P3 added the seeds, the CLI fixtures and their
// cases (https://github.com/weaveplatform/artifactweave/issues/49). P5
// added the cases of every operator, statistic and dialect trap (issue
// 163), of remote and virtual keys (issue 68) and of the GET searches, the
// build seeds and cases (issue 167), and the --build shapes the CLI sends
// (issue 171). Its follow-ups added RelatedCases (issues 410 and 494) and
// the every-property includes (issue 506). Moved here in P9d.
//
// # Testing
//
// The package has no tests of its own. The aql package's TestUnitCases,
// TestUnitBuildCases and TestUnitRelatedCases run its cases through the
// reference evaluator, and the store contract suite runs them through the
// in-memory store, SQLite and PostgreSQL.
package aqltest
