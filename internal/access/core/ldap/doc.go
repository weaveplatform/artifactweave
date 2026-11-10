// Package ldap will hold the access module's LDAP rules: bind and the group
// synchronisation policy.
//
// # Role
//
// access module, core layer. The directory client is access/adapters/ldap,
// which a core package never imports (decision 0044, dependency rules).
//
// # Inputs
//
// None yet. Planned: the LDAP settings of the Access LDAP API and the
// directory answers of the LDAP adapter (plan P12, task 3).
//
// # Outputs
//
// None yet. Planned (target layout, line 547): LDAP users bound and their
// group memberships synchronised (decision 0019).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Access and
//     identity", line 547.
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
