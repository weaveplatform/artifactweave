// Package ldap holds the access module's LDAP rules: the LDAP connections
// that the Access LDAP API and the descriptor's security.ldapSettings
// section share, their validation and protected manager password, and the
// revisioned snapshot through which directory clients read them. Binding
// against a directory (LDAP-02) and group synchronisation (LDAP-03) build on
// that snapshot.
//
// # Role
//
// access module, core layer. The access HTTP adapter serves
// /access/api/v1/ldap/settings through Service, and the module root binds
// access/api's LDAPSettings, which the configuration descriptor applies
// through, over the same Service, so both surfaces store one document per
// connection. The directory client is access/adapters/ldap, which a core
// package never imports (decision 0044, dependency rules): it will depend on
// Source instead. The package imports the standard library, the kernel,
// access/api, platform/configdoc and platform/tx.
//
// # Inputs
//
//   - Service: the configuration document port, and for Snapshot the
//     transaction runner and the administration guard; optionally the clock
//     and the source of random revisions.
//   - accessapi.LDAPChange: the fields a request or descriptor entry sets.
//     Create and Replace start from Defaults (the Access POST and PUT);
//     Plan and Apply start from the stored connection, field by field (the
//     descriptor). A nil ManagerPassword keeps the stored one, "" clears it.
//
// # Outputs
//
//   - accessapi.LDAPConnection: a connection's effective settings, never
//     its password.
//   - accessapi.LDAPResult: each descriptor change's outcome.
//   - Snapshot, Connection and Secret: every connection with its password,
//     which only Secret.Reveal returns, and revisions for each connection
//     and for the set. Source is the port the directory clients take.
//   - Refusals as *accessapi.LDAPInvalid (matching kernel.ErrInvalid),
//     ErrExists (also kernel.ErrInvalid) and ErrNotFound; store failures
//     wrapped, and kernel.ErrStore for a corrupt document.
//
// # Invariants
//
//   - The manager password is kept in the document's sealed secrets
//     (decision 0017), never in its JSON, never in an answer, an export, an
//     error or a log line; Secret prints as [redacted] through fmt, slog,
//     encoding/json and text encoders.
//   - A password needs a manager DN: one sent without it is refused, and a
//     change that empties the DN drops the stored one.
//   - Every write of a connection draws a new random revision, so a client
//     keyed by revision never reuses settings or a password after an
//     update, a disable, or a delete and create of the same key. Applying
//     the same descriptor again writes nothing and keeps the revision.
//   - Every method but Snapshot joins the transaction of its context; its
//     callers hold the administration guard across their authority check
//     and the write. Snapshot opens its own guarded transaction, so it never
//     pairs one write's settings with another's password. No directory I/O
//     runs inside any of them.
//   - Validation is complete before anything is stored, so a refused
//     connection, or a descriptor with one, writes nothing.
//
// # References
//
//   - Research 05: docs/research/05-platform-identity-and-security-apis.md,
//     "LDAP connections" (the contract, the defaults and which rules are
//     local).
//   - Research 06: docs/research/06-system-configuration-and-descriptor.md,
//     the security section.
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md.
//   - Decision 0019: docs/research/decisions/0019-human-authentication-tiers.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md.
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Access and
//     identity", line 547.
//   - Plan:
//     docs/implementation/plan.md#p12-descriptor-export-system-configuration-ldap.
//   - https://github.com/weaveplatform/artifactweave/issues/1832
//
// # Status
//
// Implemented P12 (LDAP-01): connections, their validation, the sealed
// manager password and the revisioned Source. Planned: bind and connection
// tests (LDAP-02) and group synchronisation (LDAP-03), which consume
// Source.
//
// # Testing
//
// ldap_test.go runs on the platform's in-memory configuration store:
// TestUnitDefaultsAndReadBack, TestUnitReplaceKeepsAndClearsThePassword,
// TestUnitDelete, TestUnitValidation (every refusal, and the Entra ID filter
// and escaped names that pass), TestUnitPlanAndApply (field-by-field
// changes and a second apply that writes nothing), TestUnitSnapshotRevisions,
// TestUnitSnapshotRefusals, TestUnitSecretNeverPrints, TestUnitStoreFailures
// and TestUnitCorruptDocuments. The SQL sealing and the HTTP and descriptor
// surfaces are proved in test/server/access, test/server/configuration and
// internal/app.
package ldap
