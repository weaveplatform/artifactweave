// Package cleanup administers package cleanup policies and durable enablement.
//
// # Role
//
// retention module, core layer. Policy publication and schedule positions join
// one metadata transaction after current project administration is guarded.
//
// # Inputs
//
// Package configuration, Quartz schedules, current authority and secret-free
// repository ownership. Creation is disabled; enablement is a separate operation.
//
// # Outputs
//
// Validated policies, project-filtered reads, schedule positions and opaque
// generations. WithPolicy admits bounded local effects only while the exact
// queued generation remains enabled under the publication guard. Deletion
// retains existing run history.
//
// # References
//
//   - docs/implementation/plan.md#p11-projects-webhooks-retention-replication.
//   - docs/research/decisions/0010-retention-trash-and-gc.md.
//   - docs/research/decisions/0044-modular-monolith-migration.md.
//
// # Status
//
// Implemented P11 policy administration (CLN-02). Candidate evaluation,
// execution, stop operations and reports retain their CLN-03/04 owners.
package cleanup
