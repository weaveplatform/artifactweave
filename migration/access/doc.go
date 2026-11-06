// Package access will hold the access module's constructor, New(Deps), which
// builds the Authenticator, the Authorizer and the module's services and
// publishes them through access/api.
//
// # Role
//
// access module, module root. Only app imports a module root; other modules
// reach access through access/api (decision 0044, dependency rules).
//
// # Inputs
//
// None yet. Planned: the Deps that app passes to New: the unit of work and
// the SQL handle, the keyring, the work queue, the outbox, the access
// settings and the logger (target layout, lines 772-775).
//
// # Outputs
//
// None yet. Planned: a Module whose API publishes the access/api contracts to
// app and the other modules, with the module's routes and background work
// (target layout, line 533). The module's layers are its subpackages.
//
// # References
//
//   - Context map, access row, as
//     https://github.com/weaveplatform/artifactweave/issues/958 records it:
//     owns users, groups, tokens, permissions, roles, projects, environments,
//     LDAP, SAML, OIDC, sessions and SCIM; phases P4, P11, P12, P21 and P22.
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", lines 258-300, "Access and identity", lines 529-573, and
//     "Composition root", lines 746-804.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders" and "Composition and HTTP".
//   - Decision 0005:
//     docs/research/decisions/0005-authentication-and-tokens.md.
//   - Decision 0006:
//     docs/research/decisions/0006-authorization-native-permission-model.md.
//   - Decision 0013:
//     docs/research/decisions/0013-projects-and-environments.md.
//   - Decision 0019:
//     docs/research/decisions/0019-human-authentication-tiers.md.
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration,
//     #p4-identity-and-authorization,
//     #p11-projects-webhooks-retention-replication,
//     #p12-descriptor-export-system-configuration-ldap,
//     #p21-browser-and-enterprise-authentication,
//     #p22-scim-and-identity-lifecycle.
//   - https://github.com/weaveplatform/artifactweave/issues/966: ACC-05 adds
//     module.go and rewrites this Status.
//
// # Status
//
// Placeholder: implemented by P9d.
package access
