// Package releaseshttp will serve the releases endpoints: release bundles in
// Artifactory, the distribution service and the lifecycle API.
//
// # Role
//
// releases module, adapter layer (HTTP). The package is named releaseshttp so
// that it does not shadow net/http.
//
// # Inputs
//
// None yet. Planned: requests through the generated strict servers: the
// releasebundles operations of the Artifactory specification and the
// distribution specification in P15, and the lifecycle specification in P24
// (target layout, lines 810, 813 and 817).
//
// # Outputs
//
// None yet. Planned: the module's parts of those APIs, composed into the app
// router as partial strict servers (decision 0044, composition and HTTP).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 353, and "Generated code and API specifications", lines
//     810, 813 and 817.
//   - Plan:
//     docs/implementation/plan.md#p15-release-bundles-v1-and-distribution,
//     #p24-lifecycle-and-release-bundles-v2.
//   - Context map: the releases row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0023:
//     docs/research/decisions/0023-release-bundles-and-distribution.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P15, P24.
package releaseshttp
