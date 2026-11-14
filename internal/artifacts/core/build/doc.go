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
//   - Driving: the Builds API handlers, the keyed append's, delete's and
//     promotion's handlers and replay bindings (AppendKeyed, DeleteKeyed,
//     PromoteKeyed and their Confirm functions, which the shared replay in
//     platform/idempotency runs in its transaction), the build
//     searches, and the request limits, which find a build's repository
//     through ProjectKey.
//   - Driven: the metadata store, through Store (the item service's store,
//     item.Store, with build runs, their promotions and the keyed actions'
//     reports), the blob store
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
//     Append, Promotion and PromotionResult, Delete), a keyed append's run
//     ID and its confirmation (AppendKeyed, ConfirmAppend), a keyed delete's
//     or promotion's report ID and the runs or messages it reported
//     (DeleteKeyed, ConfirmDelete, PromoteKeyed, ConfirmPromote), and runs
//     discarded under a retention setting (RetentionSettings, Discard).
//   - Runs, names and numbers read back, and the items a run's artifacts
//     are (Artifacts, SearchArtifacts, Find). NamePages and RunPages walk
//     the names and numbers a page at a time for a list written as it is
//     read, and NoRuns is the error of a build that showed the caller no
//     numbers.
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
//   - A keyed delete's or promotion's report commits with its effect and
//     outlives its receipt, so a replay always finds what the action
//     reported; a dry run or an aborted promotion stores none.
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
//   - https://github.com/weaveplatform/artifactweave/issues/1011,
//     https://github.com/weaveplatform/artifactweave/issues/1088 and
//     https://github.com/weaveplatform/artifactweave/issues/1410
//
// # Status
//
// Implemented P5 to P9c: the Builds API, promotion and retention in P5, and
// the idempotent append replay after it. Moved here in P9d. IDEM-01 (#1406)
// moved the receipt work to the replay keyed operations share
// (platform/idempotency), leaving the append's effect and reload here.
// IDEM-02 (#1410) added the keyed delete and promotion, with their reports.
//
// # Testing
//
//   - build_test.go, duplicates_test.go and promote_test.go: publishing,
//     reading, appending, renaming, deleting and promoting runs.
//   - retention_test.go: discarding runs under a retention setting.
//   - replay_test.go: the keyed append's, delete's and promotion's effects
//     and reloads under the shared replay: bound to the run or report they
//     first committed, refused once access is revoked or the run is renamed
//     or deleted, rolled back with their receipt, and pruning expired
//     reports.
//   - format_test.go and sidecar_test.go: the document item through the
//     format hooks, and its checksum sidecars.
//   - fault_test.go: every store failure surfaces and changes nothing.
package build
