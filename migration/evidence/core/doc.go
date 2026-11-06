// Package core will hold the evidence module's logic: attestation,
// verification, trusted signers, subjects and the evidence policy.
//
// # Role
//
// evidence module, core layer.
//
// # Inputs
//
// None yet. Planned: attestations to attach and verify, and the artifacts and
// releases events, such as ItemCopied, ItemDeleted and Promoted, that the
// evidence policy acts on (target layout, line 965).
//
// # Outputs
//
// None yet. Planned (target layout, line 964): attest.go, verify.go (DSSE),
// signers.go, subject.go and policy.go, the retention, deletion, copy and
// promotion rules. The model and the store port are subpackages (lines 963
// and 966).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 356, and "Adding a new business domain: Evidence (P23)",
//     lines 963-966.
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
package core
