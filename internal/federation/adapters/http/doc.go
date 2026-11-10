// Package federationhttp will serve the federation module's REST endpoints:
// pfed, grid, federation and replication.
//
// # Role
//
// federation module, adapter layer (HTTP). The package is named
// federationhttp so that it does not shadow net/http.
//
// # Inputs
//
// None yet. Planned: requests through the generated strict servers: the
// federation and replication operations of the Artifactory specification,
// paths-federation of the Access specification, and the grid and pfed
// specifications (target layout, lines 810, 811 and 814).
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
//     tree", line 345, and "Generated code and API specifications", lines
//     810-814.
//   - Plan:
//     docs/implementation/plan.md#p11-projects-webhooks-retention-replication,
//     #p13-peer-trust-and-federated-repositories,
//     #p14-platform-federation-and-topology,
//     #p37-advanced-replication-and-interoperability.
//   - Context map: the federation row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0022: docs/research/decisions/0022-federated-repositories.md.
//   - Decision 0025:
//     docs/research/decisions/0025-platform-federation-and-topology.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P11, P13, P14, P37.
package federationhttp
