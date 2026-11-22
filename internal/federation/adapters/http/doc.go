// Package federationhttp serves single-target replication configuration,
// enablement/global controls, durable execution and scoped status.
//
// # Role
//
// federation module, adapter layer (HTTP). The package is named
// federationhttp so that it does not shadow net/http.
//
// # Inputs
//
// Generated request/status DTOs, guarded configuration, the durable dispatch
// coordinator and the current-authority status reader. Wire conversion remains
// here; core services have no generated API types.
//
// # Outputs
//
// Seven strict operations composed into the app router: single-target CRUD,
// global pause reads and block/unblock. PUT stores one target with 201, POST
// patches it or applies reserved bulk enable/disable filters, and reads mask
// passwords while keeping private worker revisions out of responses. Two
// handwritten greedy-path routes admit stored-config execution with 202 and
// return read-authorized durable status with 200. Admission makes no peer call;
// per-run options are immutable and credentials are separately sealed. Distinct
// destination overrides remain explicitly unsupported.
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
// REP-01 configuration, durable single-target status and stored-config execute
// are implemented. One-shot overrides and advanced, event-driven
// and checksum replication retain their separate generated/deferred owners.
package federationhttp
