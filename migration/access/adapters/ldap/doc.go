// Package ldap will be the access module's LDAP client: binding against a
// directory and reading its users and groups.
//
// # Role
//
// access module, adapter layer (LDAP).
//
// # Inputs
//
// None yet. Planned: the directory servers that the LDAP settings name (plan
// P12, task 3).
//
// # Outputs
//
// None yet. Planned: bind results and directory entries for access/core/ldap,
// tested against an LDAP test container (plan P12, exit criteria).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 298, and "Access and identity", line 557.
//   - Plan:
//     docs/implementation/plan.md#p12-descriptor-export-system-configuration-ldap.
//   - Context map: the access row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0019:
//     docs/research/decisions/0019-human-authentication-tiers.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P12.
package ldap
