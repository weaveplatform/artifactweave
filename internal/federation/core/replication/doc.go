// Package replication manages single push/pull configuration under current
// repository authority and fences worker bookkeeping with private revisions.
//
// # Role
//
// federation module, core layer.
//
// # Inputs
//
// Presence-aware Input, a shared metadata unit of work, current repository
// authority, secret-free ownership and canonical remote/proxy configuration.
//
// # Outputs
//
// Secret-safe View, stable public target identity, private change revisions,
// atomic bulk enablement and global pause controls. An omitted credential is
// merged before the store's whole-row replacement. WithTarget guards bounded
// metadata bookkeeping; it never holds a transaction over network I/O.
// The push path scope combines an exact requested root with repository-relative
// Ant include/exclude criteria; exclusions win for both enumeration and exact
// destination reconciliation. A path match grants no content permission.
// Target routes append escaped file-name segments to the configured repository
// and derive storage metadata routes under its deployment prefix. File names
// cannot become URL references or matrix/query parameters. These helpers perform
// no HTTP requests; the executing transfer must still enforce current authority,
// credentials, destination policy and durable acknowledgement checkpoints.
// Push supplies the bounded single-target driver over TransferSource and
// PushConnections. It records intents/cursors together, verifies destination
// content and properties, recovers unknown acknowledgements by observing remote
// state, and rechecks source/configuration/transport and work ownership before
// local success publication. All remote operations stay outside metadata
// transactions. Explicit missing statistics capability is refused at admission.
// Pull supplies bounded owning-cache transfer over artifacts/api.PullSource.
// It restores the original credential per step, recovers pending intents before
// advancing cursors, and records verified cache/removal results in the artifact
// owner's transaction. Current source/options/configuration and the final work
// fence guard publication; no-store/offline admission retains its refusal.
// ScheduleKind publishes private target-revision-bound Quartz positions in the
// same configuration transaction. Each enabled schedule retains its verified
// author reference; an identical retry preserves its logical fire. Credential,
// source, enablement and lifetime changes fence queued generations. Legacy
// configurations with no retained author stay unscheduled until an authenticated
// configuration/enablement write establishes that authority.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 338.
//   - Plan:
//     docs/implementation/plan.md#p11-projects-webhooks-retention-replication,
//     #p37-advanced-replication-and-interoperability.
//   - Context map: the federation row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Configuration is implemented by REP-01. REP-02 supplies source/progress
// contracts, the bounded driver, guarded target HTTP and application bindings.
// REP-03 supplies durable verified owning-cache pull, including native registry
// admission.
// REP-04 supplies guarded Quartz configuration/lifecycle positions and private
// author persistence, exclusive generation-fenced target ownership, and active
// transfer renewal/cancellation. Coordinator/Worker now restore original authors,
// admit distinct manual/stable Quartz lifetimes, and join actual push/pull
// checkpoints with idempotent selected-path evidence. Public execute/status
// adapters and remaining runtime qualification are still integration work. Multi-push,
// event-triggered and binary-existence optimization remain P37.
//
// # Testing
//
// Core race tests cover intent/checkpoint rollback, checksum/property settlement,
// scoped reconciliation and current source/configuration/ownership fences.
// test/server/federation exercises real source runtimes and separate target
// processes on SQLite/PostgreSQL, including native Maven effects, response loss,
// restart, current authority and target failures.
package replication
