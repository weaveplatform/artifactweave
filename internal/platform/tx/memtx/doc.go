// Package memtx is the in-memory unit of work: one database of named tables
// and sequences behind one gate, with the transactions, row copies and
// keyset paging that every in-memory store built over it shares.
//
// # Role
//
// platform layer: the in-memory half of the unit of work that platform/tx
// declares, which the target layout leaves implicit. Store implements
// tx.Runner. An in-memory store embeds a *Store, keeps its rows in tables
// that TableOf returns, and makes each call through Do, so every store built
// over one *Store commits and rolls back together (decision 0044: one
// in-memory database for every module store). Today the metadata store's
// in-memory store embeds the one Store, and each module's in-memory adapter
// will embed it as the metadata store splits. It serves unit tests and
// embedders that need no durability. The package imports only the standard
// library and the kernel.
//
// # Inputs
//
//   - Contexts: Tx marks the context it passes on, and Do, a nested Tx,
//     AfterCommit and InTx recognise it and anything derived from it.
//   - Table names with their key and value types (TableOf), sequence names
//     (NextID), and the rows the stores put, read, list and delete.
//   - Rows sorted by a key of string and integer parts, with a
//     kernel.Page (Paginate).
//
// # Outputs
//
//   - Store, from New, with Tx, AfterCommit and Close (tx.Runner and the
//     metadata store's lifetime), Do, InTx and NextID.
//   - Table, from TableOf, with Rows, Get, Has, Put, Del, All and
//     DeleteWhere.
//   - Paginate, KeyStrings and CompareKey: keyset pages and their cursors.
//   - NotFound and Conflict: errors that wrap kernel.ErrNotFound and
//     kernel.ErrConflict with what was looked up or what collided.
//
// # Invariants
//
//   - One cancellable gate serialises access. Tx holds it for the whole
//     callback, and calls made with the transaction's context skip
//     re-locking, so Do inside Tx never waits for its own transaction. A
//     call that waits for the gate stops when its context ends, with an
//     error that wraps the context's and reads "inmem: ..." (decision 0043's
//     bounded execution).
//   - A failed Tx restores a snapshot of every table and sequence, giving the
//     all-or-nothing behaviour of the SQL stores; a table created inside it
//     goes too. Restoring replaces the tables, so a *Table is looked up with
//     TableOf on every use and never kept across a transaction.
//   - A nested Tx joins the outer one, and an inner error that the outer
//     function drops rolls nothing back. The SQL unit of work fails the
//     outer transaction instead, which is why each family of stores keeps a
//     unit of work of its own.
//   - Deferred functions run in order once the transaction has committed and
//     given up the gate, so they may use the store; none runs after a
//     rollback.
//   - Values are copied on the way in and on the way out, so callers never
//     share memory with the store, and on the way in every time is
//     normalised to UTC microseconds and every duration to milliseconds,
//     exactly what the SQL columns keep (kernel.Normalise). Stored values
//     are replaced, never changed in place, so a snapshot only copies each
//     table's map; code that writes Rows directly keeps to the same rule.
//   - Paginate pages by the same string and integer parts as the SQL stores,
//     so a cursor from one backend means the same position in the other.
//   - A transaction belongs to its *Store: a context inside one Store's Tx
//     is not inside another's, and every store built over one Store shares
//     its transactions.
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//     ("Metadata": an in-memory store exists for unit tests).
//   - Decision 0015: docs/research/decisions/0015-testing-and-acceptance.md
//     (the in-memory store runs the same contract suite as SQLite and
//     PostgreSQL).
//   - Decision 0043: docs/research/decisions/0043-api-reliability.md
//     ("Retained validation": cancellation and lock waits).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": one in-memory database with platform/tx/memtx; "Names
//     the migration introduces").
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 138 and 139 (platform/tx) and lines 1091 to 1094 (the unit of
//     work is platform-level and context-carried).
//   - Plan: docs/implementation/plan.md#p2-stores, task 1, and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/890
//   - https://github.com/weaveplatform/artifactweave/issues/991
//
// # Status
//
// Implemented P2: the in-memory store's tables, transactions, sequences, row
// copies and paging (pull request 3). In the P9c release, v0.2.0, a
// cancellable gate replaced its mutex, so that a call waiting for the store
// ends with its context (decision 0043, issue 890, pull request 905). Moved
// here in P9d: the unit of work leaves the in-memory store for this package.
//
// # Testing
//
//   - memtx_test.go: TestUnitTxCommit; TestUnitTxRollbackRestoresTablesAndSequences;
//     TestUnitTxNestedJoinsOuter (an inner error the outer function returns
//     rolls back both, one it drops rolls nothing back);
//     TestUnitAfterCommit (order, after the commit with the gate free,
//     never after a rollback, at once outside a transaction);
//     TestUnitCancelledContext (a context ended before the call, at the gate
//     and while waiting for it); TestUnitDoInsideTx; TestUnitInTx (per
//     store); TestUnitClose; TestUnitTable (copies, normalisation and every
//     method); TestUnitNextID; TestUnitNotFoundAndConflict (texts and
//     sentinels); TestUnitPaginateRoundTrip (cursors in both directions at
//     several limits, integer order, and each malformed cursor); and
//     TestUnitKeyStringsAndCompareKey.
//
// The metadata store's contract suite runs on its in-memory store, which is
// built on this package, as on SQLite and PostgreSQL: TxCommit, TxRollback,
// TxNested, Cancelled and BadCursor, and every entity's subtests through Do,
// the tables and Paginate.
package memtx
