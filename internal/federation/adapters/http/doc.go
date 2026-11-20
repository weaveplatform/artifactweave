// Package federationhttp serves single-target replication configuration and
// enablement/global controls through the generated Artifactory strict API.
//
// # Role
//
// federation module, adapter layer (HTTP). The package is named
// federationhttp so that it does not shadow net/http.
//
// # Inputs
//
// Generated replication request DTOs and the guarded configuration service.
// Wire conversion remains here; core configuration has no generated API types.
//
// # Outputs
//
// Seven strict operations composed into the app router: single-target CRUD,
// global pause reads and block/unblock. PUT stores one target with 201, POST
// patches it or applies reserved bulk enable/disable filters, and reads mask
// passwords while keeping private worker revisions out of responses.
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
// REP-01 configuration is implemented. Execute/status and advanced, event-driven
// and checksum replication retain their separate generated/deferred owners.
package federationhttp
