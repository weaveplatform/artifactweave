// Package huggingface owns the current Machine Learning layout used by
// Hugging Face model and dataset repositories: each model or dataset lives
// under its own folder <kind>--<owner>--<name> (models--acme--demo), with
// its files at snapshots/<40-hex-revision>/<file>, its named refs at
// refs/<name>, and its Xet records in a private .xet folder. Its hooks admit
// only that layout and rebuild the package index from the committed
// snapshots, and keep raw Xet records away from every generic route.
//
// # Role
//
// artifacts module, ecosystem layer: the storage layout and admission rules
// of the Hugging Face slice (package type huggingfaceml). Its Handler plugs
// into the item service through the format hooks (decision 0032); the
// slice's http package (artifacts/ecosystem/huggingface/http) serves the
// Hub, Xet and legacy-migration protocol over what it keeps, and the Xet
// wire formats are the xet package's.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for huggingfaceml repositories to Handler, and
//     the work queue, which runs every format's reindex through it; and the
//     protocol, which parses and builds identities and paths
//     (ParseIdentity, SnapshotPath, ParseSnapshotPath, RefPath,
//     ParseRefPath, Revision, RefName, FilePath) and marks its own writes
//     (XetWrite, DeferReindex).
//   - Driven: the format hooks' environment (format.Env), through which
//     Handler lists a repository's snapshot files and keeps the package
//     index (artifacts/core/pkgindex); the generic deploy policy
//     (artifacts/ecosystem/generic), which Handler embeds and whose
//     blackout and include and exclude checks it applies; and the xet
//     package, whose hash grammar a Xet record's path must follow.
//   - Settings: none.
//
// # Outputs
//
//   - Handler: the hooks that admit a write only in the current layout
//     (Admit), keep Xet records off copy, move and download
//     (GuardTransferSource, Resolve), rebuild the index after a snapshot
//     file is written or removed (Deployed, Deleting, Deleted, Reindex) and
//     class each path (Classify).
//   - The package index: a package per model or dataset, named
//     <kind>/<owner>/<name>, with a version per committed revision, whose
//     metadata lists the revision's files.
//   - The layout rules the protocol shares: Identity, ParseIdentity,
//     Revision, RefName, FilePath, SnapshotPath, ParseSnapshotPath, RefPath,
//     ParseRefPath, IsXetPath and ValidXetPath.
//
// # Invariants
//
//   - A model and a dataset with the same Hub ID stay disjoint, and the
//     superseded legacy layout is never read as the current one.
//   - Only a write the protocol marked with XetWrite reaches a Xet record,
//     and generic downloads, copies and moves never expose one.
//   - The index is rebuilt from the tree, so a delete, restore or move
//     never leaves a stale revision.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks; the protocol and storage decision); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/testing/huggingface.md (connecting the Hub client, the layout,
//     Xet and the evidence) and docs/operations/huggingface-migration.md
//     (the legacy layout migration).
//   - JFrog Hugging Face repositories:
//     https://docs.jfrog.com/artifactory/docs/hugging-face-repositories.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 233 (ecosystem/huggingface), line 383 (package type
//     huggingfaceml) and line 1006 (the mapping row).
//   - Plan: docs/implementation/plan.md#p32-data-and-model-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/779 and
//     https://github.com/weaveplatform/artifactweave/issues/1036
//
// # Status
//
// Implemented P9c, which delivered the plan's P32 group (issues #777 to
// #780 and #789). Staged in P9d.
//
// # Testing
//
//   - path_test.go: identities, snapshot and ref paths, and the legacy
//     layout and malformed names that stay outside the current layout.
//   - handler_test.go: admission with the repository's policy, the
//     lifecycle hooks, the reindex fence and storage failures.
//   - xet_test.go: the private .xet layout's boundaries.
package huggingface
