// Package api will be the evidence module's published interface: attaching,
// listing and verifying evidence, and the module's events.
//
// # Role
//
// evidence module, api layer. It may import only kernel, platform/outbox and
// its own api subpackages (decision 0044, dependency rules).
//
// # Inputs
//
// None yet. Planned callers: the governance module and the OneModel gateway
// (target layout, line 962).
//
// # Outputs
//
// None yet. Planned (target layout, line 962): evidence.go with Evidence
// (Attach, List, Verify), and events.go with EvidenceAttached.
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 356, and "Adding a new business domain: Evidence (P23)",
//     line 962.
//   - Plan: docs/implementation/plan.md#p23-evidence-and-provenance.
//   - Context map: the evidence row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P23.
package api
