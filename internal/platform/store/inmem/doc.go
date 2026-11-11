// Package inmem is the in-memory implementation of the stores the platform
// owns: the persistent work queue, leases, settings, configuration
// documents, this instance's identity, the federation outbox, upload
// sessions, API replays and request budgets.
//
// # Role
//
// platform layer, adapter: the in-memory reference implementation of the
// platform's ports (work.WorkQueue, work.LeaseStore,
// settings.SettingsStore, configdoc.ConfigStore, instance.InstanceStore,
// outbox.OutboxStore, session.UploadStore, idempotency.ReplayStore and
// apilimit.RequestBudgetStore) over a shared memtx.Store. The composite
// in-memory store in app/adapters/inmem builds one Store over its own
// memtx.Store, delegates its nine platform accessors to it and registers
// the layout fence as a watch. Tests that need only a platform store,
// such as the work queue's, the upload sessions', the cloud blob
// backends' and the request limiter's, build it alone over memtx.New().
// The package imports the standard library, the kernel and platform
// packages only.
//
// # Inputs
//
//   - New: the *memtx.Store whose tables hold the rows, and the
//     configdoc.Watch values its config store calls.
//   - The ports' calls, with the context of the caller's transaction, if
//     there is one.
//
// # Outputs
//
//   - Store, from New, with Settings, Leases, WorkQueue, Configs, Instance,
//     Outbox, Uploads, Replays and RequestBudgets, and memtx's Tx,
//     AfterCommit and Close.
//
// # Invariants
//
//   - The rows live in tables of the shared memtx.Store under the SQL
//     tables' names, so every call joins the transaction that the caller's
//     context carries, whichever module's store began it, and the tables
//     are the same whichever store reaches them.
//   - The config store runs the watches of a document's kind inside the
//     write's transaction, in the order given, at the points the SQL store
//     does: after a Put that creates the document or changes its Document
//     bytes, and after every successful Delete. A watch's error rolls the
//     write back.
//   - Settings keep each value as the JSON it encodes to, and times compare
//     at the precision the SQL columns store, so a read returns what the
//     SQL store would.
//   - The replay store writes its records straight into its table's rows,
//     without kernel.Normalise, because a record's fields are unexported
//     and Normalise does not reach them; Do normalises the receipt's expiry
//     before it stores it.
//
// # References
//
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (the in-memory store is the contract's reference backend).
//   - Decision 0021: docs/research/decisions/0021-peer-trust-and-sync-protocol.md
//     (the instance identity and the ordered outbox).
//   - Decision 0027: docs/research/decisions/0027-cloud-blob-backends.md
//     (upload sessions in the uploads table).
//   - Decision 0033: docs/research/decisions/0033-persistent-work-queue.md.
//   - Decision 0043: docs/research/decisions/0043-api-reliability.md
//     (API replays and request budgets).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": one in-memory database for every module store, and
//     the platform's adapters in platform/store).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 1055 to 1057 (the metadata store, its in-memory store and its
//     contract split per owner).
//   - Plan: docs/implementation/plan.md#p2-stores, #p7-npm-go-maven-locals
//     and #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1056
//   - https://github.com/weaveplatform/artifactweave/issues/1057
//
// # Status
//
// Implemented P2: the settings, lease, configuration document, instance,
// outbox and upload stores, as the metadata store's (pull request 3). P7:
// the persistent work queue (issue 297, pull request 299). P9: layout writes
// fence the remote repositories that use the layout (pull request 680).
// P9c: the replay and request budget stores (issue 890, pull request 905).
// Moved here in P9d, which added Store and New, with the watches in place of
// the layout fence.
//
// # Testing
//
// inmem_test.go: TestUnitContract runs the platform contract
// (platform/store/storetest.Run, the watch cases included) on stores from
// New over memtx.New(). The metadata store's in-memory store runs the same
// cases but the watch cases.
package inmem
