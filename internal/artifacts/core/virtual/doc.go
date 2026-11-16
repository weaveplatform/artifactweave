// Package virtual resolves virtual repositories: the order a virtual
// repository's members are searched in, and, as P10 goes on, the default
// deployment repository and the merge cache for metadata that members
// share (decision 0008).
//
// # Role
//
// artifacts module, core layer. Like every core package it never imports the
// module's adapters or an ecosystem http package (decision 0044, dependency
// rules). It imports no store either: core/aql walks members through it, and
// the store's port imports core/aql, so Repos is the one read it makes.
//
// # Inputs
//
//   - A virtual repository's configuration (its ordered members), read with
//     each member's through Repos: a port.RepoStore, or Configs over
//     configurations a request has already read.
//   - Options: the request's path and whether it names a folder, the Order
//     its caller walks in, and whether upstream steps and
//     priorityResolution apply.
//
// Planned: defaultDeploymentRepo,
// artifactoryRequestsCanRetrieveRemoteArtifacts, hideUnauthorizedResources
// and virtualRetrievalCachePeriodSecs, and the Merger that each format
// implements in its ecosystem/<type>/virtual.go (target layout, lines
// 659-665; decision 0008).
//
// # Outputs
//
//   - resolve.go: Members, a virtual repository's real members in 7.x's
//     effective order (decision 0008, R1 and R2): Resolve for downloads,
//     Package for package handlers and Stored for the storage API; and
//     Walk, every route from a virtual repository to each member, for the
//     searches that judge a member by the route that reached it.
//
// Planned (target layout, lines 659-663): hideUnauthorizedResources in
// resolve.go; merge.go, the Merger interface a format implements to merge
// metadata; cache.go, merged metadata cached for
// virtualRetrievalCachePeriodSecs.
//
// # Invariants
//
//   - Members never returns a virtual repository, and returns each real
//     member once per step: a local or federated member once, a remote
//     member as its cache and its upstream (Resolve), its upstream alone
//     (Package with upstream steps), or its cache alone (Stored, or with
//     NoUpstream).
//   - A cycle, a self-member, a diamond and a member key no repository has
//     never fail a walk; only a member read that fails does.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 222, and "Remote and virtual repositories", lines 659-665.
//   - Plan: docs/implementation/plan.md#p10-virtual-repositories.
//   - Context map: the artifacts row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0008: docs/research/decisions/0008-virtual-repositories.md,
//     rules R1 and R2, with their Artifactory 7.161.26 sources.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Implemented P10: member order and nesting (Members, Walk;
// https://github.com/weaveplatform/artifactweave/issues/1252). The rest of
// the package is planned for P10's other tickets
// (https://github.com/weaveplatform/artifactweave/issues/1521).
//
// # Testing
//
// resolve_test.go is an external test package (virtual_test) over Configs:
// a table of every Order over the six groups, priority, nesting, diamonds,
// cycles, self-members, missing keys and nested pattern drops, and Walk's
// routes, bounds and failures.
package virtual
