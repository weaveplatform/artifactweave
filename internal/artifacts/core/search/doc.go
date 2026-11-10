// Package search is the logic of the GET searches (decision 0012, research
// 22 §2): it builds their items queries, scopes them to the repositories a
// repos parameter names, runs them a page at a time through the store's AQL
// path with the caller's permissions applied to each page, caps what they
// return, and reads Maven coordinates through each repository's layout for
// the GAVC, versions and latest version searches. Every query here is an
// aql.Query, so the searches compile to each store's dialect as AQL does.
// For those three layout searches it also discovers what the remote
// repositories' upstreams hold beyond their caches (UpstreamSearch), from
// authoritative Maven metadata or bounded folder listings.
//
// # Role
//
// artifacts module, core layer: the GET searches' query building, scoping,
// paging, permission filtering and layout and version search, and the
// upstream search of the layout searches. The HTTP adapter keeps parameter
// binding, response rendering, the caller's access, the error labels'
// logging and the upstream search's route and disclosure policy; it hands
// this package what it knows through Access, Upstream, Routes,
// UpstreamDeps, the layouts function and SearchRun.Internal. The package
// never imports the module's adapters, an ecosystem http package or a
// composite store: Store names the two accessors a search reads.
//
// # Inputs
//
//   - Store: the item search (AQL) and the repository configurations.
//   - Limits: the user query limit and the AQL hard cap.
//   - The caller (kernel.Principal) and Access, the adapter's view of the
//     repositories they may see and the rows they may read.
//   - The layouts function, which reads a repository layout by name, and,
//     for a module search that also asks the remote upstreams, Upstream.
//   - UpstreamDeps, for an upstream search: the remote sources, the item
//     service whose layouts the remotes' discovery maps between, the
//     adapter's Routes and its Fail hook.
//
// # Outputs
//
//   - SearchRun, from NewSearch, whose Collect returns the hits a search's
//     criteria find that the caller may read, in item order, up to a limit.
//   - The criteria the searches build: RepoScope, Files, All and Within.
//   - LayoutScope and its Find, which return LayoutHit values with their
//     coordinates; ModuleVersions, which returns VersionEntry values.
//   - UpstreamSearch, from NewUpstreamSearch: the upstreams' files (Gavc)
//     and versions (Versions), and Revalidate, the fence before the search
//     answers.
//   - RepoConfigs, the repositories by key, which the AQL handlers read too,
//     and InvalidSearchMetadata, the 502 that the adapter's routes answer
//     too.
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
//   - Only an availability failure of an upstream whose remote repository
//     does not hard-fail allows a partial result. Security, integrity,
//     capability and superseded-source failures go to UpstreamDeps.Fail and
//     never become an empty success.
//   - Every path an upstream search names passes the adapter's route and
//     read checks (Routes.Disclose), and Revalidate rechecks the reads,
//     listings, routes and disclosures it relied on before the search
//     answers. It downloads no binary, and its reads, listings and routes
//     are bounded.
//
// # References
//
//   - Decision 0012: docs/research/decisions/0012-search-and-aql.md, lines
//     314 to 357 (repos, what is searched, limits and the layout searches,
//     with GAVC's upstream discovery at 354 to 357) and 382 to 394
//     (remote=1).
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md
//     (upstream reads go through the source boundary as the caller).
//   - Decision 0008: docs/research/decisions/0008-virtual-repositories.md
//     (a virtual key searches its members).
//   - Research 22: docs/research/22-aql-searches-build-info.md, section 2
//     (the GET searches).
//   - docs/operations/upstream-search.md: the upstream search's bounds and
//     its failure, visibility and layout rules.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Dependency rules": core never imports adapters).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 215 and 216 (core/search).
//   - https://github.com/weaveplatform/artifactweave/issues/1079
//   - https://github.com/weaveplatform/artifactweave/issues/1080
//
// # Status
//
// Implemented P5 (the GET searches, their limits and the layout searches)
// and P9 (the upstream search, #213), as the server root's search handlers.
// Staged in P9d, which cut the logic out of the handlers into this package.
//
// # Testing
//
// TestAccUnder checks the path narrowing of the layout searches. The search
// suites under test/server/artifacts drive the searches over HTTP, through
// the adapter and this package: the store and layout failures whose labels
// SearchRun.Internal carries, and the upstream searches against fake
// upstreams (the searches_upstream*_test.go suites).
package search
