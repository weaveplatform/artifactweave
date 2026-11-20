// Package retentionhttp serves package cleanup policy administration.
//
// # Role
//
// retention module, HTTP adapter. PackagePoliciesAPI is a partial strict server
// composed by app with the generated Artifactory server and remaining stubs.
//
// # Inputs
//
// Generated policy requests and the retention service. Explicit mapping
// translates public camelCase searchCriteria into the stored model.
//
// # Outputs
//
// Disabled create 201, reads/replacement/delete 200, enablement 200 and scoped
// force-disable 200. Private revisions and audit fields stay out of the wire
// representation. Current service authorization precedes repository reads.
//
// # References
//
//   - api/src/artifactory/paths-cleanup.yaml.
//   - docs/implementation/plan.md#p11-projects-webhooks-retention-replication.
//   - docs/research/decisions/0010-retention-trash-and-gc.md.
//   - docs/research/decisions/0044-modular-monolith-migration.md.
//
// # Status
//
// Implemented P11 package policy administration (CLN-02). Execution and report
// operations retain CLN-03/04; archive and restore remain P16.
package retentionhttp
