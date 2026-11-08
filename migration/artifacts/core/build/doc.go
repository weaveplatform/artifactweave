// Package build implements the Builds API (decision 0014): publishing,
// reading, appending to, renaming, promoting and deleting build-info runs,
// discarding old ones, and finding the items a run's artifacts are.
//
// # Role
//
// artifacts module, core layer: the build-info service. A run is a store
// record and a document item in its project's build-info repository, and
// every change writes both through the item service (artifacts/core/item) in
// one transaction. As artifacts core, it never imports the module's adapters
// or an ecosystem http package (decision 0044, dependency rule 8).
//
// # Inputs
//
//   - Driving: the Builds API handlers, the build searches, the
//     idempotent replay of an append (AppendOnce), and the request limits,
//     which find a build's repository through ProjectKey.
//   - Driven: the metadata store (build runs, their promotions and the
//     replay receipts, through the whole store), the blob store
//     (platform/blob.Store) that holds each run's document, and the item
//     service with the format registry (Formats) and the work queue (Work),
//     so a document item runs its format's hooks as any write does.
//   - Build-info documents as the JFrog CLI and the build tools send them
//     (Parse), and the access check the caller passes for what a run may
//     reach (Access).
//
// # Outputs
//
//   - Published, appended, renamed, promoted and deleted runs (Published,
//     Append, Promotion and PromotionResult, Delete), and runs discarded
//     under a retention setting (RetentionSettings, Discard).
//   - Runs, names and numbers read back, and the items a run's artifacts
//     are (Artifacts, SearchArtifacts, Find).
//   - The started-time form JFrog writes (StartedLayout, FormatStarted,
//     ParseStarted), LATEST (Latest) and a run's project (ProjectKey).
//   - Errors: ErrInvalid, ErrNoTarget and the store's sentinels.
//
// # Invariants
//
//   - A run's record and its document item change together or not at all,
//     so the Builds API, the storage API and AQL agree (decision 0014).
//   - A document whose module lists one file twice is refused, as 7.x does
//     by default (build.block_duplicate_entries), unless the service allows
//     it.
//   - Error texts reach clients and stay byte-identical.
//
// # References
//
//   - Decision 0014: docs/research/decisions/0014-build-info.md.
//   - Research 22: docs/research/22-aql-searches-build-info.md, section 3.
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (every write reaches a format through the item service).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 208 (core/build), line 1026 (the mapping row) and rule 2 at lines
//     35 to 36.
//   - Plan: docs/implementation/plan.md#p5-full-aql-searches-build-info and
//     #p9d-modular-monolith-migration, epic ART
//     (https://github.com/weaveplatform/artifactweave/issues/941).
//   - https://github.com/weaveplatform/artifactweave/issues/1011
//
// # Status
//
// Implemented P5 to P9c: the Builds API, promotion and retention in P5, and
// the idempotent append replay after it. Staged in P9d.
//
// # Testing
//
//   - build_test.go, duplicates_test.go and promote_test.go: publishing,
//     reading, appending, renaming, deleting and promoting runs.
//   - retention_test.go: discarding runs under a retention setting.
//   - replay_test.go: an append replayed under one idempotency key.
//   - format_test.go and sidecar_test.go: the document item through the
//     format hooks, and its checksum sidecars.
//   - fault_test.go: every store failure surfaces and changes nothing.
package build
