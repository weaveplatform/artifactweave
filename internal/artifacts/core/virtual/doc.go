// Package virtual will resolve virtual repositories: ordered member
// resolution, the default deployment repository and the merge cache for
// metadata that members share.
//
// # Role
//
// artifacts module, core layer. Like every core package it never imports the
// module's adapters or an ecosystem http package (decision 0044, dependency
// rules).
//
// # Inputs
//
// None yet. Planned: a virtual repository's configuration (its ordered
// members, defaultDeploymentRepo,
// artifactoryRequestsCanRetrieveRemoteArtifacts, hideUnauthorizedResources
// and virtualRetrievalCachePeriodSecs) and the Merger that each format
// implements in its ecosystem/<type>/virtual.go (target layout, lines
// 659-665; decision 0008).
//
// # Outputs
//
// None yet. Planned (target layout, lines 659-663):
//
//   - resolve.go: ordered member resolution, defaultDeploymentRepo and
//     hideUnauthorizedResources.
//   - merge.go: the Merger interface a format implements to merge metadata.
//   - cache.go: merged metadata cached for virtualRetrievalCachePeriodSecs.
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
//   - Decision 0008: docs/research/decisions/0008-virtual-repositories.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P10.
package virtual
