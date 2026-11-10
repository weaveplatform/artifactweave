// Package machinelearning owns FrogML's versioned model layout and publication
// invariants. Temporary uploads are never published model versions.
//
// A version's files are published under models/<name>/<version>, in the
// files, dependencies and code categories, beside its model-manifest.json;
// a transaction stages them first under models/<name>/tmp/<version>/<tx>,
// owned by the principal who started it.
//
// # Role
//
// artifacts module, ecosystem layer: the FrogML model layout and publication
// rules of the Machine Learning slice. Its Handler plugs into the item
// service through the format hooks (decision 0032), so every write, read,
// restore, delete and transfer in a Machine Learning repository reaches it
// whichever API carried it; the slice's http package
// (artifacts/ecosystem/machinelearning/http) serves the SDK's transaction
// and manifest API.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for machinelearning repositories to Handler; the
//     protocol, which checks identities, decodes and verifies manifests,
//     rechecks transactions and marks its own writes (Publication); and the
//     server's item routes, which answer 404 for a private record (Private),
//     its checksum sidecars and archive entries included.
//   - Driven: the generic deploy policy (artifacts/ecosystem/generic), which
//     Admit applies first; the store's items and the blob store, read
//     through format.Env; and the package index (artifacts/core/pkgindex),
//     which Reindex reconciles.
//   - Settings: none.
//
// # Outputs
//
//   - Handler: the hooks that admit writes only into the writer's own live
//     staging transaction (Admit), check restored files against the
//     manifest (Deployed), withdraw versions on delete (Deleted), keep
//     private records from being copied or moved (GuardTransferSource),
//     serve only complete versions' files (Resolve), class private records
//     as metadata (Classify) and publish exactly the complete versions
//     (Reindex).
//   - The layout and its checks: Identity with Prefix, Manifest and
//     Staging; Name, Segment, Version, File and Transaction; ParsePath and
//     Private.
//   - The manifest: Manifest and its parts, Decode, Read, Verify and
//     Missing; and CheckTransaction and Publication.
//
// # Invariants
//
//   - Names are one segment or a namespace and a name; segments are 1 to 60
//     ASCII letters, digits, underscores, hyphens and dots; tmp is reserved
//     in names, and tmp, files, dependencies and code in versions, so no
//     model's manifest or staging folder reads as another model's file.
//   - No write reaches a published path except through the native
//     transaction; raw writes land only in a live staging transaction of
//     their own writer (403 for another principal, 409 after 24 hours).
//   - A manifest is at most 4 MiB, declares at least one model artifact
//     and at most 10,000 files, each of at most 10 GiB with a lower-case
//     SHA-256, and has no duplicate or upper-case key and no nesting deeper
//     than 32 levels (400).
//   - A version is published, resolved and transferred only while its
//     manifest is stored and every declared file has its declared size and
//     SHA-256; deleting one withdraws the version and restoring the full
//     set publishes it again.
//   - Staging files and manifests are private: never resolved as raw
//     downloads, copied or moved.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks, and "P9c Machine Learning adapter (#780)", which
//     keeps remote and virtual repositories refused); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories, which this format does not serve: Machine Learning
//     repositories are local or federated only).
//   - docs/operations/machinelearning.md (the FrogML recipe, the layout,
//     the transaction lifetime and the limits, which the constants here
//     repeat) and docs/client-recipes/machinelearning.md (the client
//     recipe).
//   - JFrog Machine Learning repositories:
//     https://docs.jfrog.com/artifactory/docs/machine-learning-repositories.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 235 (ecosystem/machinelearning), line 385 (the slice row) and
//     line 997 (the mapping row).
//   - Plan: docs/implementation/plan.md#p32-data-and-model-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/780 and
//     https://github.com/weaveplatform/artifactweave/issues/1052
//
// # Status
//
// Implemented P9c, which delivered the plan's P32 group (issues #777 to
// #780 and #789). Staged in P9d.
//
// # Testing
//
//   - handler_test.go (external, machinelearning_test): staging admission
//     only into a live transaction of its owner, the index following
//     complete stored versions, and a reindex that fails closed on
//     corruption, storage failure and revocation.
//   - manifest_test.go (external): manifest bounds and serialization
//     metadata, stored manifest identity and content verification, and
//     ambiguous JSON and excessive depth refused.
//   - path_test.go (external): names, versions and file paths, and the
//     layout round trip without namespace or category ambiguity.
//
// The model routes' tests in artifacts/ecosystem/machinelearning/http
// exercise the hooks through the item service.
package machinelearning
