// Package cleanup administers package cleanup policies, selects candidates and
// durably admits manual and scheduled runs.
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
// Selection freezes a detached policy and clock, traverses the package index in
// bounded pages and explains comparator protection and format-owned lead facts.
// Each page refreshes authority and ownership; no candidate permits deletion.
// Selection changes no content, usage, work, or policy state. A changed lead
// during evaluation yields no candidates; execution must revalidate snapshots.
//
// ManualRun commits a run, its integer trigger identity, a private frozen policy
// and original credential reference, and its distinct work item together.
// ScheduledRun deduplicates the logical scheduler fire and fences its final
// queue lease in that same transaction. An enabled legacy policy whose actor
// is only a name needs verified re-enablement before scheduled execution.
// Stop/StopAll and emergency DisableAll request STOPPING; only the executor
// may mark a run terminal after fencing further destructive publication.
//
// # References
//
//   - docs/implementation/plan.md#p11-projects-webhooks-retention-replication.
//   - docs/research/decisions/0010-retention-trash-and-gc.md.
//   - docs/research/decisions/0044-modular-monolith-migration.md.
//
// # Status
//
// Implemented P11 policy administration and candidate evaluation (CLN-02/03).
// CLN-04 run admission, private snapshots and durable stop requests are present.
// Candidate execution/progress/terminal settlement remain in progress; report
// wire formatting retains CLN-05. These helpers do not activate public routes.
package cleanup
