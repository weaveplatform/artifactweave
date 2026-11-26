// Package federationhttp serves single-target replication configuration,
// enablement/global controls, durable execution and scoped status, and the
// service_trust pairing endpoints and federated repository operations.
//
// # Role
//
// federation module, adapter layer (HTTP). The package is named
// federationhttp so that it does not shadow net/http.
//
// # Inputs
//
// Generated request/status DTOs, guarded configuration, the durable dispatch
// coordinator and the current-authority status reader; for ServiceTrustAPI,
// the pairing service, its administrator check and the 501 stub of each
// service_trust operation. Wire conversion remains
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
// per-run options are immutable and credentials are separately sealed. Explicit
// push destinations are independent of saved configuration and never inherit credentials.
//
// ServiceTrustAPI serves /artifactory/api/v1/service_trust/ (pairing,
// pairings, revoke, exchange) and POST
// /access/api/v1/service_trust/pairing/{useCase} in 7.x's {"message"}
// bodies: 400 with the operation's message pattern for a refusal, 500 for
// anything else, 404 "Unknown use case" after the administrator check, and
// 501 before it for a use case a later phase serves (model.PairingFor).
// revoke changes nothing and exchange always answers 400 (DV-205).
//
// FederatedAPI also serves the FED-06 failed-binary list, queued replay,
// deletion and task counts. The three controls and binaries/tasks are
// administrator-only; status/repoState/{repoKey}/binaries/ requires current
// repository Read. Inferred count/path fields are marked x-unverified.
// FederatedAPI serves guarded membership/configuration operations, full sync,
// and connectionStates. The connection response uses the generated named
// FederationConnection DTO; receiver mode is independent of health. All
// connection reads require a current administrator and collect at most the
// nine remote members allowed by the ten-member federation limit.
// Unknown/non-federated keys return 200 with an empty connection array; a
// federation without a configured base URL returns 400 with that same envelope.
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
//   - Decision 0021: docs/research/decisions/0021-peer-trust-and-sync-protocol.md;
//     research 40, section 5: docs/research/40-federated-repositories-7x.md.
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
// The service_trust endpoints are implemented P13 (PEER-02, #1926).
// Receiving-only member enforcement and connectionStates are FED-03 (#1933).
package federationhttp
