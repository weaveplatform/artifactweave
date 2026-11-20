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
// Configuration is implemented by REP-01. Transfer and execution are separate
// P11 owners; multi-push, event-triggered and checksum behavior remain P37.
package replication
