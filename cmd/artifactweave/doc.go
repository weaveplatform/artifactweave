// Command artifactweave is the registry server and its operator commands:
// serve, migrate, config, keys and version.
//
// # Role
//
// Command, outside the modules. It stays a thin cobra shim: flags are parsed
// here and everything else lives in internal/ so it can be tested without a
// process.
//
// # Inputs
//
//   - Configuration from the file that --config names, the environment and
//     the flags, with flag over environment over file; internal/config
//     rejects unknown keys. Secrets have no flags, so they stay out of shell
//     history.
//   - The subcommand and its arguments: serve; migrate up, down and version,
//     which --module limits to one module's migration stream; config
//     validate and apply; keys rotate; version.
//   - The version, commit and build date stamped into
//     migration/platform/buildinfo at link time (the -X flags in the Makefile
//     and the release build).
//
// # Outputs
//
//   - The running server, schema migrations, a validated or applied
//     configuration descriptor and a rotated master key, each done by
//     internal/app.
//   - Logs through log/slog in the configured level and format. A failed
//     command prints "error: " and the error on standard error and exits 1.
//
// # References
//
//   - Decision 0009: docs/research/decisions/0009-configuration-as-code.md.
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md.
//   - docs/operations/configuration-reference.md.
//   - Plan: docs/implementation/plan.md#p0-foundations, #p2-stores and
//     #p3-repositories-generic-artifacts-cli-aql-shape.
//
// # Status
//
// Implemented P0 (serve, config validate and version), P2 (migrate and keys
// rotate) and P3 (config apply).
package main
