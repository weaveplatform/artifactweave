// Package federation qualifies single-repository replication configuration
// through the assembled application router and current repository authority.
//
// # Role
//
// Server-assembly acceptance owned by shared CI in test_ownership.json.
//
// # Inputs
//
// Local, remote and virtual repositories, platform/project principals and
// actual JWT/reference credentials over memory, SQLite and PostgreSQL stores.
//
// # Outputs
//
// CRUD/default/validation and enablement-control assertions; safe password
// projection; atomic repository-lifetime invalidation and injected-failure
// rollback; current project flags, membership and token-ceiling enforcement.
//
// # References
//
//   - api/src/artifactory/paths-replication.yaml.
//   - docs/research/decisions/0013-projects-and-environments.md.
//   - https://github.com/weaveplatform/artifactweave/issues/1760.
//
// # Status
//
// P11 REP-01 configuration. Transfer, execution/status and scheduling retain
// their separate owners; advanced replication remains deferred.
//
// # Testing
//
// TestAccSingleReplicationSQLConfigurationAndRepositoryLifetime uses sealed
// keyrings and sqltest.RunModule with isolated PostgreSQL scratch schemas.
package federation
