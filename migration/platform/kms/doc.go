// Package kms will resolve external master-key references, so that the
// keyring's master key can come from an external key management service.
//
// # Role
//
// platform layer: key infrastructure with no business rules, beside
// platform/crypt, which seals columns with the master key.
//
// # Inputs
//
// None yet. Planned: a master-key reference in the application configuration
// that names a key held by an external service. Today the master key comes
// from a file or an environment variable only (decision 0017).
//
// # Outputs
//
// None yet. Planned (target layout, line 155): the master key for the keyring
// in platform/crypt, fetched through the reference, with provider-specific
// recovery tests (plan P38, task 4).
//
// # References
//
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", line 155.
//   - Plan:
//     docs/implementation/plan.md#p38-operations-and-external-integrations.
//   - Context map: platform has no row; the operations row owns external
//     integrations
//     (https://github.com/weaveplatform/artifactweave/issues/958).
//   - Decision 0017:
//     docs/research/decisions/0017-sealed-columns-and-master-key.md, which
//     leaves external master-key sources to P38.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders".
//
// # Status
//
// Placeholder: implemented by P38.
package kms
