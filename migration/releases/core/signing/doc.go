// Package signing will sign release bundle manifests with detached OpenPGP
// signatures.
//
// # Role
//
// releases module, core layer.
//
// # Inputs
//
// None yet. Planned: a bundle's canonical manifest, and a signing key from
// the access module's key pair store, with its passphrase (decision 0023).
//
// # Outputs
//
// None yet. Planned: detached signatures that edges verify against the
// propagated public key (decision 0023; plan P15, exit criteria).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 351.
//   - Plan:
//     docs/implementation/plan.md#p15-release-bundles-v1-and-distribution.
//   - Context map: the releases row, restated in the module root's doc.go;
//     https://github.com/weaveplatform/artifactweave/issues/958 holds the
//     map.
//   - Decision 0023:
//     docs/research/decisions/0023-release-bundles-and-distribution.md.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P15.
package signing
