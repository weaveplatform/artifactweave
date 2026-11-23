// Package federation qualifies single-repository replication configuration and
// bounded push through assembled application processes and current authority.
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
//
// # References
//
//   - api/src/artifactory/paths-replication.yaml.
//   - docs/research/decisions/0013-projects-and-environments.md.
//   - https://github.com/weaveplatform/artifactweave/issues/1760.
//   - https://github.com/weaveplatform/artifactweave/issues/1761.
//
// # Status
//
// P11 REP-01 configuration and REP-02 bounded push. Execution/status and
// scheduling retain REP-04 ownership; advanced replication remains deferred.
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
package federation
