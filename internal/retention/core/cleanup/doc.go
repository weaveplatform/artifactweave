// Package cleanup administers package cleanup policies, selects candidates and
// executes durable manual and scheduled runs.
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
// Project policies also retain the owning project incarnation. A reused key
// cannot admit old work or reuse a bound preview, even for a platform admin;
// verified re-enablement binds a new policy generation without rewriting history.
// Stop/StopAll and emergency DisableAll request STOPPING; only the executor
// may mark a run terminal after fencing further destructive publication.
// An explicit stop re-enqueues its real execution so parked work can settle.
//
// Executor reconstructs the frozen selection and restores the original current
// principal with its permission decider inside each metadata transaction.
// Bounded traversal cursors and immutable file intents survive restart. Normal
// ItemRemoval effects, actual item/byte acknowledgements and continuation CAS
// commit together, after current project/content checks and a final lease fence.
// Non-leads precede ranked leads; only acknowledged removals leave the expected
// version fingerprint. Stop, expiry and cancellation cannot publish a partial
// mutation as completed. Dry runs record selection without artifact effects.
// Exhausted work retries record the failing intent and retire other pending
// intents in bounded pages before FAILED. A refused outcome checkpoint retains
// queue retryability; it cannot strand an active run behind parked work. These
// private outcome writes never use the original caller as an enduring deletion
// capability and retain only the actually acknowledged item/byte counts.
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
// CLN-04 run admission, durable execution/progress and stop settlement are
// present, with public run/stop routes and application worker/scheduler wiring.
// Construction starts no worker; the runtime registers handlers before starting
// shared queue/discovery loops. Report wire formatting retains CLN-05.
package cleanup
