// Package inmem is the in-memory implementation of the releases module's
// stores: release bundles v1 with their artifacts, keeps, retention audits
// and exports, and the distribution trackers with their per-peer jobs.
//
// # Role
//
// releases module, adapter layer (in-memory): the reference implementation
// of the module's port.Store over a shared memtx.Store. The metadata
// store's in-memory store builds one Store over its own memtx.Store and
// delegates Bundles and Distribution to it; STO-19 moves that composition
// into app. The package imports the standard library, the kernel, the
// platform and the module's core only.
//
// # Inputs
//
//   - New: the *memtx.Store whose tables hold the rows.
//   - The port's calls, with the context of the caller's transaction, if
//     there is one.
//
// # Outputs
//
//   - Store, from New, with Bundles and Distribution, and memtx's Tx,
//     AfterCommit and Close.
//
// # Invariants
//
//   - The rows live in tables of the shared memtx.Store under the SQL
//     tables' names (release_bundles, bundle_artifacts, bundle_keeps,
//     bundle_audits, bundle_exports, distribution_trackers and
//     distribution_jobs), so every call joins the transaction that the
//     caller's context carries, whichever module's store began it.
//   - Deleting a bundle deletes its artifacts, keep and export in the same
//     call, as the SQL tables' cascades do; its retention audits stay.
//
// # References
//
//   - Decision 0023: docs/research/decisions/0023-release-bundles-and-distribution.md.
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (the in-memory store is the contract's reference backend).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": one in-memory database for every module store;
//     "Names the migration introduces", which lists Store and New).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 353 (the releases adapters) and line 1056 (the metadata store's
//     in-memory store split per owner).
//   - Plan: docs/implementation/plan.md#p2-stores,
//     #p15-release-bundles-v1-and-distribution and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1059
//
// # Status
//
// Implemented P2: the bundle and distribution stores, as the metadata
// store's (pull request 3). Staged in P9d: Store and New. The services that
// call them arrive with P15.
//
// # Testing
//
// inmem_test.go: TestUnitContract runs the releases contract
// (adapters/storetest.Run) on stores from New over memtx.New(). The
// metadata store's in-memory store runs the same cases.
package inmem
