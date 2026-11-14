// Package app wires configuration, the stores, the modules and the HTTP
// router together. cmd/artifactweave stays a thin cobra shim that calls Run
// and the other commands' functions; tests drive Run with a fake handler.
//
// # Role
//
// app: the composition root. It opens what the modules share (the metadata
// store, the blob tiers and their mirrors, the keyring and the work queue),
// builds the access, artifacts and configuration modules over them in
// buildModules, has the artifacts module open its remote sources, and
// builds over all of it the single chi router that serves every JFrog path
// prefix (NewRouter). It may import any package, and only
// cmd/artifactweave and the server test harness
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
//   - RouterConfig and NewRouter: the HTTP handler that serves /artifactory,
//     /access, /event, /router, /jfconnect, /v2 and the other specified
//     service prefixes over the stores, services and settings a
//     RouterConfig names. Run serves it; the server test harness builds it
//     for the suites under test/server.
//   - Runtime, RuntimeOptions and OpenRuntime: what serve holds open, with
//     the costs it is opened with, exported so that the server test harness
//     builds the router over the stores and modules serve uses.
//
// # Invariants
//
//   - OpenRuntime first sets the xz dictionary budget from
//     xz.dictionary_budget, which every decode in the process shares, those
//     of the work queue included (#1354).
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
//   - NewRouter installs its middleware in one order: the error profiles,
//     the request ID, panic recovery, the access log, the base URL, the
//     Smart Remote trace, the Docker API version header and registry
//     challenge, content type normalisation, collection slashes, plus as
//     space, the idempotent routes, the API recovery context and the xz
//     request holder (xzRequests, so a request's xz readers are one holder
//     of the dictionary budget); then, each
//     only when configured, the ingress request limits, the forced
//     authentication of Maven and P2 virtual repositories
//     (itemkit.ForceAuthentication), authentication, the API restrictions
//     and the principal request limits; then the Helm partial reindex. The
//     ingress limits count a request before anyone is authenticated and the
//     principal limits charge the caller after (decision 0043). The forced
//     authentication goes just before authentication, where 7.x's filter
//     sits, so it answers the same whether anonymous access is on or off.
//   - Both request limits admit a request under the x-cost class of the spec
//     operation it addresses, which limits_cost.go finds in the generated
//     operation table before routing, so the specs are the one place a
//     class is set. Only a request no spec operation addresses (an alias, a
//     native protocol route, a 404 or a 405) takes the class the path rules
//     give it: the probes, the recovery API and expensiveRequest.
//   - Hand-written routes are mounted before the generated strict servers,
//     and the 501 stubs of hand-written operations last, so each
//     hand-written route claims the pattern its spec derives instead of the
//     generated handler or the stub (decision 0002). Each spec's strict
//     server embeds the modules' partials at depth one and the generated
//     501 stubs at depth two, so an implemented operation shadows its stub
//     and two partials claiming one operation fail to compile.
//
// # References
//
//   - Decision 0001: docs/research/decisions/0001-mirror-the-jfrog-api-surface.md
//     (the routes, statuses and envelopes the router serves).
//   - Decision 0002: docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md
//     (one strict server per spec, and hand-written item paths mounted
//     first).
//   - Decision 0009: docs/research/decisions/0009-configuration-as-code.md
//     (the application configuration that Run and the commands take, and
//     config apply).
//   - Decision 0016: docs/research/decisions/0016-observability-and-health.md
//     (the structured log, which the HTTP server's error log joins).
//   - Decision 0033: docs/research/decisions/0033-persistent-work-queue.md
//     (the work queue that OpenRuntime starts).
//   - Decision 0043: docs/research/decisions/0043-api-reliability.md
//     (the request limits and their place in the middleware chain).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     (app is the composition root and may import any package).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 746 to 805 (the composition root, router.go at lines 748 to
//     762), 389 to 409 (where the server root's files went) and 819 to 834
//     (the router's strict servers composed by embedding).
//   - https://github.com/weaveplatform/artifactweave/issues/1092,
//     https://github.com/weaveplatform/artifactweave/issues/1094,
//     https://github.com/weaveplatform/artifactweave/issues/1096 and
//     https://github.com/weaveplatform/artifactweave/issues/1097
//
// # Status
//
// Implemented P0 to P9c. Moved here in P9d (WEB-18); WEB-19 added the module
// settings, WEB-20 the module constructors, which took over the remote wiring
// and the garbage collection loop, and WEB-21 gave each file one job: run.go
// serves, platform.go opens what the modules share, modules.go builds the
// modules, bootstrap.go and seed.go prepare the first start, runtime.go holds
// the runtime and serverlog.go the HTTP server's error log. TST-07 exported
// the runtime and moved the remote suites to test/server/artifacts. WEB-22
// brought the router here: router.go builds it and limits.go holds the
// request limits.
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
//
// The whole-router guards run beside NewRouter over fixtures_test.go: the
// status tables (status_test.go and status_<x>_test.go), the route and
// principal matrices (matrix_test.go), the spec checks (spec_test.go,
// spec_contract_test.go), the 501 stubs (stub_test.go), the error profiles
// (error_profiles_p8_test.go), the request limits (limits_test.go; the
// classes admission takes from the table, limits_cost_test.go; every spec
// operation's 429 and 503, limits_operations_test.go), the Docker list
// errors' registry envelope (docker_list_errors_test.go), the API
// reliability ledger (api_ledger_test.go, which make api-ledger runs to
// regenerate docs/research/api-reliability/operations.json) and decision
// 0043's operation rules (api_principles_test.go, whose rules are checked
// against the admission and error profiles here, with a fixture per rule in
// api_principles_fixtures_test.go). They must not import the harness, which
// imports app.
package app
