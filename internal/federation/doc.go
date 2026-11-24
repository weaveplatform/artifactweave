// Package federation owns guarded single-target replication configuration,
// the bounded single-push transfer driver and pairing between instances.
// Outbox delivery, federation and topology retain their later owners.
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
// OpenPush receives Artifacts' public source API after module assembly and
// exact trusted outbound/TLS policy. It initializes its private policy key.
//
// # Outputs
//
// Configurations manages single push/pull targets, URL-filtered enablement and
// global pause controls. RepositoryLifecycle removes stale targets and runs
// inside Artifacts' guarded create/delete transaction. Configuration does not
// transfer content or publish a run result.
// Pusher supplies resumable transfer after OpenPush; dispatch and scheduling
// retain admission/lease ownership. Close releases the owned target HTTP pool.
// Caller restoration refreshes the original verified credential and current
// permissions under the project guard, and never returns a transaction context.
// Pairing returns the pairing service (core/pairing) over a given signed
// transport, admitting a caller who is an administrator now; RegisterWork
// also runs the periodic pruning of expired pairing nonces
// (PruneNoncesKind, every PruneNoncesInterval) and, once OpenLiveness has
// given it the signed peer client and an interval, the periodic read of
// every paired peer's status (PeerProbeKind), whose interval also sets the
// pairing service's stale window to two of them. OpenStreams builds the
// stream delivery engine (core/sync) over the signed peer client, with no
// stream kind registered until FED-02 adds repo:<key>; Streams returns it
// for the peer API's stream and blob operations, and RegisterWork then
// runs its dispatchers, pulls, blob retries, sweep and pruning.
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
// REP-01 configuration and REP-02/03 bounded direction drivers are implemented.
// REP-04 binds durable manual/Quartz admission and generation-fenced workers to
// actual transfer checkpoints and path evidence. Public stored-config execute
// answers 202 after durable admission; status uses current read authority and
// scoped evidence. Manual selection/transport overrides are sealed per run; explicit destinations need no saved row and retain independent history. Remaining
// qualification is open. Advanced
// federation remains P13/P14/P37. Both drivers open before the queue starts.
// Pairing is implemented P13 (PEER-02, #1926); stream delivery P13 (PEER-04,
// #1928); peer liveness P13 (#1949).
package federation
