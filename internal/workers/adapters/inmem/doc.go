// Package inmem is the in-memory implementation of the workers module's
// stores: worker definitions, their state, their secrets and their
// executions.
//
// # Role
//
// workers module, adapter layer (in-memory): the reference implementation
// of the module's port.Store over a shared memtx.Store. The composite
// in-memory store in app/adapters/inmem builds one Store over its own
// memtx.Store and delegates Workers to it. The package imports the
// standard library, the kernel, the platform and the module's core only,
// and artifacts/api/repo, which the workers model's filter names.
//
// # Inputs
//
//   - New: the *memtx.Store whose tables hold the rows.
//   - The port's calls, with the context of the caller's transaction, if
//     there is one.
//
// # Outputs
//
//   - Store, from New, with Workers, and memtx's Tx, AfterCommit and Close.
//
// # Invariants
//
//   - The rows live in tables of the shared memtx.Store under the SQL
//     tables' names (workers, worker_state, worker_secrets,
//     worker_executions), so every call joins the transaction that the
//     caller's context carries, whichever module's store began it, and the
//     tables are the same whichever store reaches them.
//   - Deleting a worker deletes its state, secrets and executions in the
//     same call, as the SQL tables' cascades do.
//
// # References
//
//   - Decision 0026: docs/research/decisions/0026-workers-runtime.md.
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (the in-memory store is the contract's reference backend).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": one in-memory database for every module store;
//     "Names the migration introduces", which lists Store and New).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 694 (the workers adapters) and line 1056 (the metadata store's
//     in-memory store split per owner).
//   - Plan: docs/implementation/plan.md#p2-stores, #p17-workers and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1058
//
// # Status
//
// Implemented P2: the worker store, as the metadata store's (pull request
// 3). Staged in P9d: Store and New. The runtime that calls it arrives with
// P17.
//
// # Testing
//
// inmem_test.go: TestUnitContract runs the workers contract
// (adapters/storetest.Run) on stores from New over memtx.New(). The
// metadata store's in-memory store runs the same cases.
package inmem
