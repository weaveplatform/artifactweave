// Package app wires configuration, the stores and the HTTP server together.
// cmd/artifactweave stays a thin cobra shim that calls Run and the other
// commands' functions; tests drive Run with a fake handler.
//
// # Role
//
// app: the composition root. It opens what the modules share (the metadata
// store, the blob tiers and their mirrors, the keyring, the work queue and
// the remote sources), builds the access module over them, and hands
// everything to the server root, which builds the router. It may import any
// package, and only cmd/artifactweave imports it.
//
// # Inputs
//
//   - The loaded config.Config (app/config), which cmd/artifactweave reads
//     with config.Load from the file, the environment and the flags.
//   - The command line, through cmd/artifactweave: serve calls Run, migrate
//     calls Migrate, keys rotate calls RotateKeys, and config apply calls
//     ApplyDescriptor with the descriptor file it names.
//
// # Outputs
//
//   - Run: serves until its context is cancelled, then drains the HTTP
//     server and closes what it opened.
//   - Migrate: the up, down and version actions over the schema migrations,
//     and ErrUnknownMigration for any other action.
//   - RotateKeys: re-seals every sealed value under the newest master key.
//   - ApplyDescriptor: applies a YAML descriptor to the configured store, or
//     with dryRun only checks it.
//   - Keyring, the keyring from master_key and master_key_previous, and
//     OpenStore, the configured metadata store.
//   - InitialPasswordFile: the file under the data directory where the first
//     start writes a generated admin password.
//
// # Invariants
//
//   - openRuntime opens the store, which applies pending migrations, seeds
//     the built-in principals and applies the bootstrap, then opens the
//     token service, the access module, both blob tiers, the garbage
//     collector, the work queue and the remote sources, in that order, and
//     starts the background loops last. A failure on the way closes what it
//     opened so far.
//   - The store opens, and its migrations finish, before the listener
//     accepts a request.
//   - Requests run on a context that the shutdown signal does not cancel, so
//     the drain lets them finish; they are cancelled only once it expires.
//
// # References
//
//   - Decision 0009: docs/research/decisions/0009-configuration-as-code.md
//     (the application configuration that Run and the commands take, and
//     config apply).
//   - Decision 0016: docs/research/decisions/0016-observability-and-health.md
//     (the structured log, which the HTTP server's error log joins).
//   - Decision 0033: docs/research/decisions/0033-persistent-work-queue.md
//     (the work queue that openRuntime starts).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     (app is the composition root and may import any package).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 746 to 805 (the composition root).
//   - https://github.com/weaveplatform/artifactweave/issues/1092
//
// # Status
//
// Implemented P0 to P9c. Staged in P9d (WEB-18) unchanged; the module
// settings, the module constructors, the file split and the router follow
// in WEB-19 to WEB-22.
//
// # Testing
//
// The tests start the runtime from a configuration as serve would:
// app_test.go, runtime_test.go and store_test.go cover Run, openRuntime,
// Migrate and RotateKeys; blob_test.go, mirrors_test.go and gc_test.go the
// blob tiers; seed_test.go and descriptor_test.go the first start and
// config apply; the *_remote_test.go suites remote repositories through the
// composed runtime. make conan-accept runs conan_client_test.go. hash_test.go
// lowers the password hashing cost and the token key size for all of them.
package app
