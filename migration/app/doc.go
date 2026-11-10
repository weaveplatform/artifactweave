// Package app wires configuration, the stores and the HTTP server together.
// cmd/artifactweave stays a thin cobra shim that calls Run and the other
// commands' functions; tests drive Run with a fake handler.
//
// # Role
//
// app: the composition root. It opens what the modules share (the metadata
// store, the blob tiers and their mirrors, the keyring and the work queue),
// builds the access, artifacts and configuration modules over them in
// buildModules, has the artifacts module open its remote sources, and hands
// everything to the server root, which builds the router. It may import any
// package, and only cmd/artifactweave and the server test harness
// (test/server/support/harness) import it.
//
// # Inputs
//
//   - The loaded config.Config (app/config), which cmd/artifactweave reads
//     with config.Load from the file, the environment and the flags. app
//     reads the keys a module owns only through its accessors, Access,
//     Artifacts and Configuration, and the platform's keys as fields.
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
//   - Runtime, RuntimeOptions and OpenRuntime: what serve holds open, with
//     the costs it is opened with, exported so that the server test harness
//     builds the router over the stores and modules serve uses.
//
// # Invariants
//
//   - OpenRuntime opens the store, which applies pending migrations, seeds
//     the built-in principals and applies the bootstrap, then opens the
//     token service, the authenticator, both blob tiers, the garbage
//     collector and the work queue, builds the modules, registers the
//     artifacts module's work and opens its remote sources, in that order,
//     and starts the background loops last. A failure on the way closes what
//     it opened so far.
//   - buildModules does no I/O, so building the modules once the work queue
//     is open moves none of the start-up I/O. The multipart upload service
//     and the garbage collector are built before them and take access's
//     store-only bindings, as the bootstrap does.
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
//     (the work queue that OpenRuntime starts).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     (app is the composition root and may import any package).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 746 to 805 (the composition root).
//   - https://github.com/weaveplatform/artifactweave/issues/1092,
//     https://github.com/weaveplatform/artifactweave/issues/1094 and
//     https://github.com/weaveplatform/artifactweave/issues/1096
//
// # Status
//
// Implemented P0 to P9c. Staged in P9d (WEB-18); WEB-19 added the module
// settings, WEB-20 the module constructors, which took over the remote
// wiring and the garbage collection loop, and WEB-21 gave each file one
// job: run.go serves, platform.go opens what the modules share, modules.go
// builds the modules, bootstrap.go and seed.go prepare the first start,
// runtime.go holds the runtime and serverlog.go the HTTP server's error log.
// TST-07 exported the runtime and moved the remote suites to
// test/server/artifacts. The router follows in WEB-22.
//
// # Testing
//
// The tests start the runtime from a configuration as serve would, each
// beside the file it tests: run_test.go covers Run; runtime_test.go
// OpenRuntime, its loops and Close; platform_test.go Keyring, OpenStore,
// Migrate, RotateKeys, the blob tiers and their mirrors; modules_test.go
// buildModules and the token service; seed_test.go and bootstrap_test.go
// the first start and config apply; serverlog_test.go the HTTP server's
// error log. hash_test.go lowers the password hashing cost and the token key
// size for all of them. The suites that read remote content through the
// composed runtime run from test/server/artifacts/<type>, over the harness's
// App and RemoteWorld.
package app
