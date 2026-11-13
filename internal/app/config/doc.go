// Package config loads the application configuration described in
// docs/operations/configuration-reference.md and decision record 0009.
//
// # Role
//
// app: application configuration. The composition root and the commands in
// cmd/artifactweave take the Config this package loads; no module imports
// it. The YAML types stay here, beside the composition root, because
// ByteSize decodes itself from the file (it implements yaml.Unmarshaler).
// It turns the sections a module owns into that module's settings type
// (access/api, configuration/api, and the artifacts module root, which only
// app may import).
//
// # Inputs
//
//   - The YAML file that LoadOptions.File names, if any.
//   - The environment, through LoadOptions.Env (OSEnv in the command): each
//     dotted key upper-cased behind EnvPrefix, so database.dsn is
//     ARTIFACTWEAVE_DATABASE_DSN.
//   - The flags the command line set, through LoadOptions.Flags, by dotted
//     key.
//
// # Outputs
//
//   - Config, with one section type per area (Database, Blob, Work, the
//     package formats' sections and the others), and its methods: Validate,
//     String (the redacted summary that config validate prints),
//     RequireMasterKey and the derived paths and DSN.
//   - Load, which applies file, environment and flags over the defaults and
//     validates the result, and LoadOptions and OSEnv for it.
//   - Default, the configuration with no file, environment or flag, built
//     from the Default constants.
//   - ByteSize and ParseByteSize, EnvPrefix, and the errors Validate and
//     Load wrap (ErrUnknownKey and the others).
//   - The module settings, which app reads module-owned keys through:
//     Access (token.allow_basic_auth, environments.global_limit),
//     Artifacts (the download, search, aql_timeout, build, deploy, npm,
//     cran, huggingface, maven, nuget, remote, gc, conda, helm and pypi
//     sections) and Configuration (docker.subdomain_match). The platform's
//     keys (listen, base_url, data_dir, compat, database, blob, cold_blob,
//     the master keys, bootstrap, log, work, api and xz) stay fields that
//     app reads directly.
//
// # Invariants
//
//   - The precedence is flag, then environment, then file, then default.
//   - The file is strict YAML: an unknown key is an error rather than a
//     silently ignored typo, because a misspelt storage key would otherwise
//     start the server on the wrong backend without any warning.
//   - String replaces every secret, so a pasted summary can never leak a
//     key or a password.
//   - Module settings are derived by the accessors, never stored as fields
//     of Config: a field would be a key of the file and a line of the
//     config validate output, which String marshals in field order.
//     TestUnitConfigStringGolden pins that output.
//
// # References
//
//   - Decision 0009: docs/research/decisions/0009-configuration-as-code.md
//     ("Decision", layer 1: the application configuration).
//   - docs/operations/configuration-reference.md: every key, its
//     environment variable and its default.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Composition and HTTP" (per-module settings are accessor methods on
//     Config).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 746 to 805 (config/ under the composition root).
//   - https://github.com/weaveplatform/artifactweave/issues/1092 and
//     https://github.com/weaveplatform/artifactweave/issues/1093
//
// # Status
//
// Implemented P0 to P9c: each phase added the keys of what it built. Moved
// here in P9d (WEB-18) unchanged; WEB-19 added the module settings accessors.
package config
