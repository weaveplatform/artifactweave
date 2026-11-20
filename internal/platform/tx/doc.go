// Package tx declares the unit-of-work contract, Runner: one transaction
// that every store call made with its context joins, and functions deferred
// until before or after that transaction commits.
//
// # Role
//
// platform layer: the context-carried unit of work that every module store
// and service uses. A store call joins whatever transaction its context
// carries, so a service runs calls to several stores, and later to several
// modules' api packages, in one Tx without passing a transaction value
// around. The package declares the interface and the before-commit callback
// queue, and imports only the standard library. Each family of stores implements a unit of work
// of its own, because the families differ on purpose in one case (see
// Invariants): platform/tx/memtx for the in-memory stores, and
// platform/sql/dialect for the SQL stores, over database/sql. Every
// module's port Store embeds Runner, so the composite in-memory, SQLite and
// PostgreSQL stores implement it.
//
// # Inputs
//
//   - Tx: a context and a function that makes its store calls with the
//     context it is given.
//   - AfterCommit: a context and a function to run once the transaction
//     that context is inside commits.
//   - BeforeCommit: an active transaction context and a fallible callback.
//   - Middleware surrounds ordinary work in a new root after backend binding
//     and rollback setup. It is configured once by the application composition.
//   - SetPublicationCheck binds repeatable ordinary-work settlement to the
//     owning transaction, before acknowledgement and final lease/stream locks.
//
// # Outputs
//
//   - Runner, with Tx and AfterCommit.
//   - BeforeCommit and Finalizers: ordered required work after business
//     mutations finish, inside the same transaction and before commit.
//   - CheckPublication: settle current local changes while commit facts can
//     still be registered. A nested join restores its owner's check; an inner
//     independent transaction cannot replace it. No configured check is a no-op.
//
// # Invariants
//
//   - An error from Tx's function rolls back every store call made with its
//     context, and Tx returns that error or one that wraps it.
//   - A panic escaping Tx's function rolls back those calls and continues
//     unchanged. A panic recovered inside the function leaves the outer
//     transaction to decide the outcome. AfterCommit runs after commit, so
//     a panic in one of its functions cannot roll back committed writes.
//   - A nested Tx joins the outer transaction, whose commit or rollback
//     decides for both. An inner error that the outer function drops still
//     fails the outer transaction in the SQL unit of work and does not in
//     the in-memory one, so callers return inner errors; the metadata
//     store's contract suite checks that case on every store.
//   - Deferred functions run in order after the commit, and never after a
//     rollback; outside a transaction AfterCommit runs its function at once.
//     So work that a change asks for is asked for exactly when the change
//     commits (decision 0033's enqueue within the change).
//   - BeforeCommit callbacks run only after successful business work, inside
//     the owning transaction. A callback error or panic rolls back its work
//     together with business state. Reentrant registration fails the unit,
//     including when the callback drops that registration error. Unsupported
//     runners and closed contexts refuse registration without executing it.
//   - Capture immutable event facts during business work. BeforeCommit
//     callbacks append them without taking later domain locks, preserving
//     domain-before-event-stream lock order across nested mutations.
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//     ("Metadata": store contracts composed with Tx, and an in-memory store
//     for unit tests).
//   - Decision 0021: docs/research/decisions/0021-peer-trust-and-sync-protocol.md
//     (the outbox entry is written in the transaction of the change it
//     records).
//   - Decision 0033: docs/research/decisions/0033-persistent-work-queue.md
//     ("Enqueue within the change").
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": one unit of work for every module store).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 138 and 139 (platform/tx, from the metadata store's Tx and
//     AfterCommit), lines 482 and 590 (tx.go and Runner), line 917 (a deploy
//     opens its unit of work through tx.go), line 1113 (descriptor apply
//     wraps several modules in one Tx) and lines 1091 to 1094 (the unit of
//     work is platform-level and context-carried). Its Inspect(ctx) has no
//     consumer yet and is not declared.
//   - Plan: docs/implementation/plan.md#p2-stores, task 1, and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/991
//
// # Status
//
// Implemented P2: Tx and AfterCommit as methods of the metadata store, with
// its in-memory, SQLite and PostgreSQL units of work (pull request 3).
// Moved here in P9d: the contract is declared here, and every module's port
// Store embeds it.
//
// # Testing
//
// beforecommit_test.go covers queue order, failure, panic cleanup, closed
// contexts and rejected reentrant registration. The shared platform store
// contract proves business and event rollback on memory/SQLite/PostgreSQL.
// The metadata store's contract suite (the TxCommit, TxRollback, TxNested
// and Cancelled subtests, and the rollback cases of the replay, outbox, work
// queue, request budget and Git LFS lock subtests) runs Tx and AfterCommit
// on its in-memory, SQLite and PostgreSQL stores. memtx's own tests cover the
// in-memory unit of work and platform/sql/dialect's the SQL one, each with
// the nested case that the suite leaves out, and test files of the in-memory
// and SQL stores and of the dialect store check at compile time that they
// implement Runner.
package tx
