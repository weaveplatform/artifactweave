// Package system validates and reads the mail-server settings needed by quota
// delivery and administers bounded temporary logger sessions.
//
// # Role
//
// configuration module, core layer.
//
// # Inputs
//
// The mailServer descriptor mapping and the shared configuration store, runner
// and authority guard. Secret values live separately in ConfigDocument.Secrets.
// LoggerDeps supplies real logging decisions, guarded current administration and
// an optional timer scheduler for deterministic expiration checks.
//
// # Outputs
//
// Validated mail settings behind configuration/api.Mail. Every read captures
// public settings and the password under one guarded metadata transaction,
// completed before SMTP starts. No function here opens a network connection.
// Loggers validates temporary level requests, applies one in-memory session,
// restores current baseline decisions on expiry/delete and joins timers on Close.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", lines 305 and 309.
//   - Plan:
//     docs/implementation/plan.md#p12-descriptor-export-system-configuration-ldap.
//   - Context map: the configuration row, restated in the module root's
//     doc.go; https://github.com/weaveplatform/artifactweave/issues/958 holds
//     the map.
//   - Decision 0009: docs/research/decisions/0009-configuration-as-code.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// PRJ-04 supplies the narrow documented mailServer dependency for quota warnings.
// P12 CFG-03 supplies actual process-local logger controls. Remaining system and
// mail administration stay P12; log collection and streaming stay P38.
package system
