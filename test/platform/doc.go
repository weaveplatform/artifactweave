// Package platform checks a reference deployment end to end: artifactweave
// started against a scenario that platform-scenarios.yml has just applied
// (decision 0030).
//
// # Role
//
// Acceptance suite for the reference deployments, outside the modules.
// Scenario runs are evidence that a deployment works, not coverage.
//
// # Inputs
//
// The suite runs only when ARTIFACTWEAVE_SCENARIO names the scenario, so go
// test ./... passes over it. The workflow sets:
//
//	ARTIFACTWEAVE_SCENARIO               <cloud>/<scenario>, for example gcp/single-bucket
//	ARTIFACTWEAVE_SCENARIO_CONFIG        the scenario's artifactweave_config output (YAML)
//	ARTIFACTWEAVE_SCENARIO_STORAGE_HOST  the scenario's storage_host output
//	ARTIFACTWEAVE_SCENARIO_OUTPUTS       every output, as terraform output -json writes them
//	ARTIFACTWEAVE_BIN                    the artifactweave binary built in the job
//	ARTIFACTWEAVE_JF_BIN                 the pinned jf, for the CDN download check
//
// # Outputs
//
//   - TestAccScenario's result: the generic checks, then the scenario's own
//     (the mirror's copies, drain and repair, the cloud's replica, and the
//     CDN or Front Door edge), stopping at the first failure because later
//     checks build on earlier ones.
//
// # References
//
//   - Decision 0030: docs/research/decisions/0030-reference-deployments.md.
//   - Research 20: docs/research/20-reference-deployments.md.
//   - Plan:
//     docs/implementation/plan.md#p4b-reference-deployments-and-cdn-delivery.
//   - https://github.com/weaveplatform/artifactweave/issues/129: the workflow
//     and this suite.
//   - https://github.com/weaveplatform/artifactweave/issues/189: scenarios
//     written once, as this suite's checks and the consumer documentation.
//
// # Status
//
// Implemented P4b.
package platform
