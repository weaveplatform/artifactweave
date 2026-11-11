// Package terraformbackend is the format of Terraform backend repositories
// (decision 0041): the workspaces and states of Terraform's remote backend,
// kept as items under workspaces/<ws>/ as 7.x keeps them. Its hooks keep the
// package index, in which a workspace is a package and each state a version
// named by its epoch milliseconds, whose metadata answers the API without
// reading the content. The content itself is written only through the
// Terraform backend API (artifacts/core/terraformstate), which marks its
// writes with Writing: through the item routes a deploy, a folder create, a
// copy or move into the repository, and a change to the lock's properties
// are refused, so nothing passes the lock or the seal. Deletes are allowed,
// into the trash can, as for any item.
//
// # Role
//
// artifacts module, ecosystem layer: the storage rules of the Terraform
// backend slice (package type terraformbackend). Its Handler plugs into the
// item service through the format hooks (decision 0032). The state service
// (artifacts/core/terraformstate) stays in core and is the only writer of
// workspaces, locks and states; the slice's http package
// (artifacts/ecosystem/terraformbackend/http) serves the TFE-shaped API
// over it.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for terraformbackend repositories to Handler, and
//     the work queue, which runs every format's reindex through it; and the
//     state service and the backend API, which build paths and IDs (Folder,
//     RecordPath, LatestPath, StatePath, WorkspaceID, StateID, Splits,
//     SplitStateID, CheckName), seal and read states (Seal, Open, MetaOf,
//     PlainMeta) and mark their writes (Writing).
//   - Driven: the format hooks' environment (format.Env), through which
//     Handler reads stored states (ReadStored) and keeps the package index
//     (artifacts/core/pkgindex), with AQL (artifacts/core/aql) for a
//     reindex; and the keyring (platform/crypt), which seals and opens
//     states under Purpose.
//   - Settings: Settings.Keys, the server's keyring. Without it a sealed
//     state is indexed as unreadable.
//
// # Outputs
//
//   - Handler: the hooks that refuse writes through the item routes (Admit,
//     AdmitFolder and AdmitAnnotation, answering Refusal's 400), hand what
//     the API knows of a state to the other hooks (Inspect), settle the
//     versions a write, a move, a delete or a restore changes (Deployed,
//     Deleting, Deleted), class each path (Classify) and rebuild the index
//     (Reindex).
//   - The package index: a package per workspace and a version per state,
//     named by its epoch milliseconds, with the state's Meta as the
//     version's metadata.
//   - The layout and identifier rules the state service and the API share:
//     Root, RecordName, LatestName, PropLockedBy, PropLockedAt and Purpose;
//     File, FileOf and ParseFile; Version, Created and Row; Meta, Encode and
//     DecodeMeta; Parse, MD5 and SHA256; ErrInvalidName, ErrNotState and
//     ErrNoKeyring.
//
// # Invariants
//
//   - Only a write marked with Writing passes Admit, AdmitFolder and
//     AdmitAnnotation; a client's request never carries the mark, whatever
//     route it takes.
//   - A version's index entry is what the tree says, read the same way by
//     the hooks and by a reindex, and in the same order on every store.
//   - Purpose ("terraform-state") is authenticated with every sealed state,
//     so its value never changes. TestAccSealedColumnsComplete exempts it by
//     value, since a state is a sealed blob rather than a sealed column.
//   - Content that does not open or is no state is indexed as Unreadable, so
//     a trash restore or a reindex never fails on it; the API refuses it
//     when it is read.
//
// # References
//
//   - Decision 0041: docs/research/decisions/0041-terraform-backend.md
//     ("Identifiers", "Storage", "encryptStates", "Locking", "The item
//     routes"), and research 32: docs/research/32-terraform-backend.md.
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks); decision 0018:
//     docs/research/decisions/0018-package-index.md; decision 0017:
//     docs/research/decisions/0017-sealed-columns-and-master-key.md (the
//     master key and the sealing format).
//   - JFrog Terraform backend repositories, as research 32 cites it:
//     https://docs.jfrog.com/artifactory/docs/terraform-opentofu-and-terraform-backend-repositories.md.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 231 (ecosystem/terraformbackend) and line 1004 (the mapping row).
//   - Plan: docs/implementation/plan.md#p8-pypi-nuget-helm-terraform-locals
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/557 (storage and
//     locking) and https://github.com/weaveplatform/artifactweave/issues/1035
//
// # Status
//
// Implemented P8 (Terraform backend local repositories). Staged in P9d.
//
// # Testing
//
//   - handler_test.go and fault_test.go: the refusals of the item routes,
//     the index a write, a move, a delete and a restore leave, sealed and
//     unreadable content, and store faults.
//   - paths_test.go and state_test.go: names, paths and identifiers, state
//     parsing, sealing and the index metadata.
//   - contract_test.go: the format contract suite (artifacts/core/formattest).
package terraformbackend
