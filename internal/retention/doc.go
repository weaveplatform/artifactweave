// Package retention owns cleanup policy administration and its execution fence.
//
// # Role
//
// retention module, module root. Only app imports this constructor; other
// modules reach public contracts through retention/api. New stores dependencies
// without I/O; Policies binds configuration over the shared unit of work.
//
// # Inputs
//
// Deps supplies retention/schedule persistence, current access APIs, secret-free
// repository ownership and the server schedule timezone (UTC by default).
//
// # Outputs
//
// Policy administration and durable generation checks for subsequent cleanup
// execution. Identity and authority refresh under the project guard before
// administrative disclosure or publication. Construction starts no scheduler,
// executor or network activity.
//
// # References
//
//   - docs/implementation/plan.md#p11-projects-webhooks-retention-replication.
//   - docs/implementation/plan.md#p16-cold-storage-archive-and-retention.
//   - docs/research/decisions/0010-retention-trash-and-gc.md.
//   - docs/research/decisions/0024-cold-storage-tier.md.
//   - docs/research/decisions/0044-modular-monolith-migration.md.
//
// # Status
//
// Implemented P11 policy administration (CLN-02). Evaluation, execution and
// reports retain CLN-03/04; cold archive/restore remain P16.
package retention
