// Package federation owns guarded single-target replication configuration.
// Peer trust, outbox delivery, federation and topology retain their later owners.
//
// # Role
//
// federation module, module root. Only app imports a module root; other
// modules reach federation through federation/api (decision 0044, dependency
// rules). New constructs configuration and repository-lifetime services without
// reading dependencies, starting workers or making remote requests.
//
// # Inputs
//
// Deps supplies the shared transaction store, current Access authority,
// secret-free repository ownership and canonical remote/proxy configuration.
//
// # Outputs
//
// Configurations manages single push/pull targets, URL-filtered enablement and
// global pause controls. RepositoryLifecycle removes stale targets and runs
// inside Artifacts' guarded create/delete transaction. Configuration does not
// transfer content or publish a run result.
//
// # References
//
//   - Context map, federation row, as
//     https://github.com/weaveplatform/artifactweave/issues/958 records it:
//     owns peer trust, outbox delivery, federated repositories, replication,
//     Access federation and topology; phases P11, P13, P14 and P37.
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", lines 85-104 and 331-346, and "Webhooks and events (P11, P13)",
//     lines 717-744.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders" and "Composition and HTTP".
//   - Decision 0021:
//     docs/research/decisions/0021-peer-trust-and-sync-protocol.md.
//   - Decision 0022: docs/research/decisions/0022-federated-repositories.md.
//   - Decision 0025:
//     docs/research/decisions/0025-platform-federation-and-topology.md.
//   - Plan:
//     docs/implementation/plan.md#p11-projects-webhooks-retention-replication,
//     #p13-peer-trust-and-federated-repositories,
//     #p14-platform-federation-and-topology,
//     #p37-advanced-replication-and-interoperability.
//
// # Status
//
// REP-01 configuration is implemented. Transfer, execution and scheduling are
// separate P11 tickets; peer and advanced federation remain P13, P14 and P37.
package federation
