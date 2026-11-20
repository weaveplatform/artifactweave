// Package project manages project lifecycle, environments, roles and membership.
//
// # Role
//
// access module, core layer.
//
// # Inputs
//
// Service takes the access port.Store and caller contexts, with
// artifacts/api.Repositories for lifecycle and environment-reference changes.
// Its caller supplies the project key and checks current administration
// privileges in the same transaction before publication. It has no HTTP
// dependency and does not turn a management scope into artifact access.
//
// # Outputs
//
// Role discovery combines current global definitions with project custom roles.
// CUSTOM mutations validate the distinct project action vocabulary and available
// environments. Direct user/group assignments require existing identities and
// roles; removing a role cleans assignments in the same project transaction.
// AssignedRoles and NamedRoles separately resolve current membership-based and
// explicitly selected definitions. These lookups do not themselves grant access.
//
// Creation allocates project settings and its server-owned build-info repository
// together. Deletion removes administration and unassigns repositories while
// retaining their contents. Environment rename updates role, assignment and
// repository references atomically; deletion refuses references still in use.
// access/adapters/http binds administration with explicit wire conversions.
// Every compound operation takes the project snapshot guard before its records,
// and no transaction spans network I/O or streaming.
//
// StorageAccounting surrounds the application composition's root transactions.
// Actual item, retained raw-cache, transformed-copy and ownership deltas settle
// together before event acknowledgment and lease fences. Lazy initialization
// uses canonical scalar totals before mutation. The durable warning handoff
// shares that transaction; mail delivery is a separate worker concern.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 285, and "Access and identity", lines 546 and 571-573.
//   - Plan:
//     docs/implementation/plan.md#p11-projects-webhooks-retention-replication.
//   - Context map: the access row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0013:
//     docs/research/decisions/0013-projects-and-environments.md.
//   - docs/research/project-roles-membership-contract-audit.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// P11 PRJ-01 supplies administration over the role and identity foundations.
// PRJ-02 activates current role-derived content and management grants, with
// guarded role-token mint/use and project lifetime boundaries. Repository
// attachment and standard sharing publish canonical configuration and Access
// assignments in one guarded transaction (PRJ-03). Quotas retain their separate
// P11 owner.
// The contract audit's unresolved vendor validation/scope edges remain qualified.
package project
