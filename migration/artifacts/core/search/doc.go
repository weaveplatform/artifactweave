// Package search is the logic of the GET searches (decision 0012, research
// 22 §2): it builds their items queries, scopes them to the repositories a
// repos parameter names, runs them a page at a time through the store's AQL
// path with the caller's permissions applied to each page, caps what they
// return, and reads Maven coordinates through each repository's layout for
// the GAVC, versions and latest version searches. Every query here is an
// aql.Query, so the searches compile to each store's dialect as AQL does.
//
// # Role
//
// artifacts module, core layer: the GET searches' query building, scoping,
// paging, permission filtering and layout and version search. The HTTP
// adapter keeps parameter binding, response rendering, the caller's access
// and the error labels' logging; it hands this package what it knows
// through Access, Upstream, the layouts function and SearchRun.Internal.
// The package never imports the module's adapters, an ecosystem http
// package or the store aggregate: Store names the two accessors a search
// reads.
//
// # Inputs
//
//   - Store: the item search (AQL) and the repository configurations.
//   - Limits: the user query limit and the AQL hard cap.
//   - The caller (kernel.Principal) and Access, the adapter's view of the
//     repositories they may see and the rows they may read.
//   - The layouts function, which reads a repository layout by name, and,
//     for a module search that also asks the remote upstreams, Upstream.
//
// # Outputs
//
//   - SearchRun, from NewSearch, whose Collect returns the hits a search's
//     criteria find that the caller may read, in item order, up to a limit.
//   - The criteria the searches build: RepoScope, Files, All and Within.
//   - LayoutScope and its Find, which return LayoutHit values with their
//     coordinates; ModuleVersions, which returns VersionEntry values.
//   - RepoConfigs, the repositories by key, which the AQL handlers read too.
//
// # Invariants
//
//   - Results past a limit are dropped silently, since the searches'
//     responses have no range to report it; UserLimit and Capped give the
//     limits, and SearchPage the rows one store query reads.
//   - An administrator reads every row; anyone else gets the rows
//     Access.Readable keeps, page by page, so a page can yield fewer.
//   - Every failure the caller cannot act on goes through SearchRun.Internal
//     with its label ("search", "search permissions", "search layout"),
//     which operators read in the log, so the labels stay as they are.
//   - The 400s of ModuleSearch are apierror values whose text clients read.
//
// # References
//
//   - Decision 0012: docs/research/decisions/0012-search-and-aql.md, lines
//     314 to 357 (repos, what is searched, limits and the layout searches).
//   - Decision 0008: docs/research/decisions/0008-virtual-repositories.md
//     (a virtual key searches its members).
//   - Research 22: docs/research/22-aql-searches-build-info.md, section 2
//     (the GET searches).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Dependency rules": core never imports adapters).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 215 and 216 (core/search).
//   - https://github.com/weaveplatform/artifactweave/issues/1079
//
// # Status
//
// Implemented P5 (the GET searches, their limits and the layout searches)
// and P9 (the upstream part of a module search, through Upstream), as the
// server root's search handlers. Staged in P9d, which cut the logic out of
// the handlers into this package.
//
// # Testing
//
// TestAccUnder checks the path narrowing of the layout searches. The search
// suites under test/server/artifacts drive the searches over HTTP, through
// the adapter and this package, including the store and layout failures
// whose labels SearchRun.Internal carries.
package search
