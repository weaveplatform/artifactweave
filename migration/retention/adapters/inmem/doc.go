// Package inmem is the in-memory implementation of the retention module's
// stores: cleanup policies and runs, and archive policies and executions.
//
// # Role
//
// retention module, adapter layer (in-memory): the reference
// implementation of the module's port.Store over a shared memtx.Store. The
// metadata store's in-memory store builds one Store over its own
// memtx.Store and delegates Cleanup and Archive to it; STO-19 moves that
// composition into app. The package imports the standard library, the
// kernel, the platform and the module's core and api only.
//
// # Inputs
//
//   - New: the *memtx.Store whose tables hold the rows.
//   - The ports' calls, with the context of the caller's transaction, if
//     there is one.
//
// # Outputs
//
//   - Store, from New, with Cleanup and Archive, and memtx's Tx, AfterCommit
//     and Close.
//
// # Invariants
//
//   - The rows live in tables of the shared memtx.Store under the SQL
//     tables' names (cleanup_policies, cleanup_runs, archive_policies,
//     archive_executions), so every call joins the transaction that the
//     caller's context carries, whichever module's store began it, and the
//     tables are the same whichever store reaches them.
//   - Runs and executions take their IDs from the memtx sequence of their
//     table, so IDs grow as the SQL store's do, and lists page newest first
//     by ID.
//
// # References
//
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md.
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (the in-memory store is the contract's reference backend).
//   - Decision 0024: docs/research/decisions/0024-cold-storage-tier.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": one in-memory database for every module store;
//     "Names the migration introduces", which lists Store and New).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 329 (the retention adapters) and line 1056 (the metadata store's
//     in-memory store split per owner).
//   - Plan: docs/implementation/plan.md#p2-stores,
//     #p11-projects-webhooks-retention-replication,
//     #p16-cold-storage-archive-and-retention and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1058
//
// # Status
//
// Implemented P2: the cleanup and archive stores, as the metadata store's
// (pull request 3). Staged in P9d: Store and New. The services that call
// them arrive with P11 (cleanup) and P16 (the cold-storage tier).
//
// # Testing
//
// inmem_test.go: TestUnitContract runs the retention contract
// (adapters/storetest.Run) on stores from New over memtx.New(). The
// metadata store's in-memory store runs the same cases.
package inmem
