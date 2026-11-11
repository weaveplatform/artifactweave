// Package gitlfs owns the content-addressed object layout of Git LFS
// repositories. An object is stored at
// objects/<first two OID characters>/<next two>/<64-character SHA-256 OID>,
// Artifactory's layout, and only content with that SHA-256 is admitted
// there; the object tree itself is the index the batch API answers from.
//
// # Role
//
// artifacts module, ecosystem layer: the Git LFS content-addressed object
// layout of the Git LFS slice. Its Handler plugs into the item service
// through the format hooks (decision 0032), so every write in a Git LFS
// repository reaches it whichever API carried it; the slice's http package
// (artifacts/ecosystem/gitlfs/http) serves the batch, transfer and locking
// API over the object tree.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for gitlfs repositories to Handler; and the
//     protocol, which checks every OID and builds its path (ObjectPath).
//   - Driven: the generic deploy policy (artifacts/ecosystem/generic),
//     which Handler embeds and Admit applies after its own checks.
//   - Settings: none.
//
// # Outputs
//
//   - Handler: the hooks that admit only an object at its own path with
//     its own SHA-256 (Admit), and a reindex that has nothing to rebuild
//     (Reindex). Deploys and deletes need nothing more, because the object
//     tree is the index.
//   - ObjectPath: the object layout, and the check of an OID.
//
// # Invariants
//
//   - An OID is 64 lower-case hexadecimal characters, a SHA-256.
//   - An object's content always has the SHA-256 its path names: a write
//     elsewhere or with other content is refused (409), and so is a client
//     checksum sidecar.
//   - Delete and restore change the batch view at once, because nothing is
//     derived from the object tree.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories, whose verified objects the cache keeps at the same
//     paths).
//   - docs/implementation/git-lfs-client.md (connecting Git LFS, the object
//     layout and the evidence), docs/research/36-git-lfs.md (the protocol
//     research) and docs/client-recipes/gitlfs.md (the client recipe).
//   - JFrog Git LFS repositories:
//     https://docs.jfrog.com/artifactory/docs/git-lfs-repositories.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 233 (ecosystem/gitlfs), line 379 (the slice row) and line 1005
//     (the mapping row).
//   - Plan: docs/implementation/plan.md#p33-automation-and-source-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/787 and
//     https://github.com/weaveplatform/artifactweave/issues/1052
//
// # Status
//
// Implemented P9c, which delivered the plan's P33 group (issues #782 to
// #788). Moved here in P9d.
//
// # Testing
//
//   - gitlfs_test.go: the canonical SHA-256 object path, and the refusal of
//     writes that are not objects.
//
// The object routes' tests in artifacts/ecosystem/gitlfs/http exercise the
// hooks through the item service.
package gitlfs
