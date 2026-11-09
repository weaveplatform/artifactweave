// Package inmem is the in-memory implementation of the notifications
// module's stores: webhook subscriptions, with their handlers' secrets, and
// the delivery records.
//
// # Role
//
// notifications module, adapter layer (in-memory): the reference
// implementation of the module's port.Store over a shared memtx.Store. The
// metadata store's in-memory store builds one Store over its own
// memtx.Store and delegates Webhooks to it; STO-19 moves that composition
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
//   - Store, from New, with Webhooks, and memtx's Tx, AfterCommit and Close.
//
// # Invariants
//
//   - The rows live in tables of the shared memtx.Store under the SQL
//     tables' names (webhooks, webhook_deliveries), so every call joins the
//     transaction that the caller's context carries, whichever module's
//     store began it, and the tables are the same whichever store reaches
//     them.
//   - Deliveries sort and bound by attempt time at the precision the SQL
//     columns store, with an unset time as zero, so a listing returns what
//     the SQL store would.
//
// # References
//
//   - Decision 0011: docs/research/decisions/0011-webhooks.md.
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (the in-memory store is the contract's reference backend).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": one in-memory database for every module store;
//     "Names the migration introduces", which lists Store and New).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 323 (the notifications adapters) and line 1056 (the metadata
//     store's in-memory store split per owner).
//   - Plan: docs/implementation/plan.md#p2-stores,
//     #p11-projects-webhooks-retention-replication and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1058
//
// # Status
//
// Implemented P2: the webhook store, as the metadata store's (pull request
// 3). Staged in P9d: Store and New. The webhook services that call it
// arrive with P11.
//
// # Testing
//
// inmem_test.go: TestUnitContract runs the notifications contract
// (adapters/storetest.Run) on stores from New over memtx.New(). The
// metadata store's in-memory store runs the same cases.
package inmem
