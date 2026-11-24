// Package federation qualifies single-repository replication configuration and
// bounded push through assembled application processes and current authority,
// and pairing between two instances through the service_trust endpoints.
//
// # Role
//
// Server-assembly acceptance owned by shared CI in test_ownership.json.
//
// # Inputs
//
// Local, remote and virtual repositories, platform/project principals and
// actual JWT/reference credentials over memory, SQLite and PostgreSQL stores.
// Single-push tests start a separate target OS process using OpenRuntime and
// the production router, plus the real source runtime and bounded driver.
//
// # Outputs
//
// CRUD/default/validation and enablement-control assertions; safe password
// projection; atomic repository-lifetime invalidation and injected-failure
// rollback; current project flags, membership and token-ceiling enforcement.
// Source bytes, computed digests, exact properties and scoped deletion through
// native target admission/metadata APIs, including an empty selected source.
// Pairing: both use cases paired, listed and unpaired on both instances, with
// 7.x's messages; refused tokens leaving no peer record; one of two concurrent
// redemptions pairing; a forced unpair of an unreachable peer.
//
// # References
//
//   - api/src/artifactory/paths-replication.yaml.
//   - docs/research/decisions/0013-projects-and-environments.md.
//   - https://github.com/weaveplatform/artifactweave/issues/1760.
//   - https://github.com/weaveplatform/artifactweave/issues/1761.
//   - https://github.com/weaveplatform/artifactweave/issues/1926 (pairing).
//
// # Status
//
// P11 REP-01 configuration and REP-02 bounded push. Execution/status and
// scheduling retain REP-04 ownership; advanced replication remains deferred.
// P13 PEER-02 pairing.
//
// # Testing
//
// TestAccSingleReplicationSQLConfigurationAndRepositoryLifetime uses sealed
// keyrings and sqltest.RunModule with isolated PostgreSQL scratch schemas.
// SinglePushProcesses uses independent source/target SQLite files or PostgreSQL
// scratch schemas. Target configuration and subprocess logs are private files;
// helper stdout is captured and only its owned loopback readiness address is
// consumed. Every target process is drained and joined by its owner.
// Recovery tests lose upload/property/delete acknowledgements after native
// commit, reopen the source runtime/database, and reject duplicate mutations.
// Current-fence tests block transfer while content, original credentials or
// target configuration change. Failure tests use native 401/403 responses and
// controlled checksum, 503 and socket-timeout faults. Maven tests check native
// metadata and checksum companions; native-client consumption belongs to REP-05.
// NTLM tests push through the fakeupstream NTLMv2 verifier configured by the
// public descriptor, then refuse a wrong proxy password and its stale run (#1848).
// Pairing tests open two runtimes on loopback listeners bound first, so each
// may call the other, over SQLite and PostgreSQL scratch schemas; closing one
// listener makes its instance unreachable.
//
// Every top-level test runs in parallel (#1882). Each builds its own runtimes,
// target processes, httptest servers, temporary directories and PostgreSQL
// scratch schemas, and none sets process environment or package state, so
// running them together shares nothing a test asserts on. The package is bound
// by database round trips, not CPU: run serially it was the slowest Go
// acceptance package and needed about 230s on hosted runners and up to 830s on
// a loaded workstation, against make accept-go's 15-minute package timeout. A
// new test that needs process-wide state must not call t.Parallel.
package federation
