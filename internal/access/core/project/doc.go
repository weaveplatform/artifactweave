// Package project implements the P11 project role and membership domain service.
// Project lifecycle, quotas, environments and repository attachment remain work
// for the complete project administration workflow.
//
// # Role
//
// access module, core layer.
//
// # Inputs
//
// Service takes the access port.Store and caller contexts. Its caller supplies
// the project key and checks administration privileges before mutation. It has
// no HTTP dependency and does not turn a management scope into artifact access.
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
// No project HTTP routes or effective project-role authorization are activated
// by these private services. The contract audit retains unresolved collision,
// default-array and scope semantics before that integration can be complete.
// Every compound operation takes the project snapshot guard before its records,
// and no transaction spans network I/O or streaming.
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
// P11 in progress, as prerequisites of issue 487. The full configuration,
// sharing, current-role authorization and native acceptance matrix remains open.
// Identity deletion now clears direct project assignments in its own
// transaction, guarded before identity rows; real-backend fault and concurrent
// assignment tests cover rollback and non-resurrection. Project HTTP views and
// token mint/use policy still need composition before role authorization.
package project
