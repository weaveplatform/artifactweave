// Package inmem is the composite in-memory store: the platform's in-memory
// store and every module's, over one in-memory unit of work, so a
// transaction begun through any of them spans them all. Tests use it where
// they need every store and no durability.
//
// # Role
//
// app, test support: the in-memory counterpart of app/adapters/sql. Store
// embeds a *memtx.Store, the in-memory unit of work, so the gate, the
// transactions with their snapshots, the row copies and the paging are
// memtx's, and so are Tx, AfterCommit and Close. Store keeps no rows of its
// own: the platform's stores are platform/store/inmem's and each module's
// are its adapters/inmem's, all over the same memtx.Store. Test files of any
// module may import it (dependency rule 1); production code never does. It
// imports memtx, the platform's in-memory store and every module's
// adapters/inmem, and never app.
//
// # Inputs
//
//   - The calls of each store, with the context of the caller's transaction,
//     if there is one.
//
// # Outputs
//
//   - Store, from New: the platform's Settings, Leases, WorkQueue, Configs,
//     Instance, Outbox, Uploads, Replays and RequestBudgets; the artifacts
//     module's Repos, RemoteCache, Items, Trash, Packages, Blobs, Replicas,
//     GitLFSLocks, BowerRegistrations, Builds, ArchivedItems, ColdBlobs and
//     ColdReplicas; the access module's Users, Groups, Permissions, Tokens,
//     Keys, Projects and APIRestrictions; Webhooks; Cleanup and Archive;
//     Replications, Peers, Federation and Platform; Bundles and
//     Distribution; Workers; and memtx's Tx, AfterCommit and Close.
//
// # Invariants
//
//   - New registers the artifacts module's layout watch with the platform's
//     config store, so a layout document that is created, changed or
//     deleted fences remote publication for the repositories that use it in
//     the same transaction, as the SQL composite does (decision 0007).
//   - The platform's and the modules' stores are fields rather than
//     embedded, so none of their methods is promoted onto Store, where it
//     could shadow an accessor without a compile error.
//   - The composite contract of app/storetest runs on Store and on the SQL
//     composite alike, so what a test observes here holds on SQLite and
//     PostgreSQL too (decision 0015).
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//     (an in-memory store for unit tests).
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (one contract suite for every backend).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": the in-memory adapters over memtx; "Dependency
//     rules", rule 1; "Names the migration introduces").
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     "Tests and enforcement" at lines 842 to 850 (module tests build their
//     module with in-memory stores).
//   - Plan: docs/implementation/plan.md#p2-stores and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1098
//
// # Status
//
// Implemented P2: the reference in-memory store (pull request 3); later
// phases added a store for each entity they introduced. Staged in P9d: each
// module's in-memory stores split into its own adapters/inmem over memtx
// (issues 1056 to 1062), leaving this package the composition over them,
// which issue 1098 moved under app.
//
// # Testing
//
//   - inmem_test.go: TestUnitContract runs the composite contract of
//     app/storetest on a new Store for each case.
//   - runner_test.go: a compile-time check that Store is a tx.Runner.
package inmem
