// Package sql is the SQL implementation of the notifications module's
// stores: webhook subscriptions, their handlers' sealed secrets and the
// delivery records, with the module's registration with the SQL layer.
//
// # Role
//
// notifications module, adapter layer (SQL). Store implements the module's
// port.Store (port.WebhookStore over the shared unit of work) over a shared
// dialect.Store, on SQLite and PostgreSQL alike. Module registers the
// notifications stream (adapters/sql/migrations) and the sealed column
// webhook_secrets.value, fourth in the canonical module order. The
// composite SQL store in app/adapters/sql builds one Store over its own
// dialect store, delegates Webhooks to it and names Module in its module
// list. The package imports the standard library, the kernel, the platform,
// the module's core and its stream only.
//
// # Inputs
//
//   - New: a *dialect.Store over a database that holds the notifications
//     stream's tables.
//   - The port's calls, with the context of the caller's transaction, if
//     there is one.
//
// # Outputs
//
//   - Store, from New, with Webhooks, and the dialect store's Tx,
//     AfterCommit and Close.
//   - Module: the name notifications, the stream migrations.FS and the
//     sealed column webhook_secrets.value.
//
// # Invariants
//
//   - Store is built over the shared *dialect.Store, so every call joins
//     the unit of work that the caller's context carries, whichever
//     module's store began it (decision 0044, "Persistence").
//   - Every column the store seals is in Module: webhook_secrets.value,
//     with the purpose "webhook_secrets.value" and the row id
//     "<webhook>/<handler index>/<name>", so key rotation re-seals it
//     (decision 0017). Neither ever changes: both are authenticated with
//     every value.
//   - A handler's secrets are rows of webhook_secrets; the subscription's
//     JSON document never holds one.
//   - The SQL text, the table names and the error texts are those the
//     metadata store's SQL store used before the module owned them.
//
// # References
//
//   - Decision 0011: docs/research/decisions/0011-webhooks.md.
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": one dialect.Module per module in the canonical order;
//     "Names the migration introduces", which lists Store, New and Module).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 323 (the notifications adapters), line 617 (the notifications
//     SQL adapter) and lines 631 to 633 (RunModule).
//   - Plan: docs/implementation/plan.md#p2-stores,
//     #p11-projects-webhooks-retention-replication and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1058
//
// # Status
//
// Implemented P2: the webhook store with its sealed secrets, as the
// metadata store's (pull request 3). Staged in P9d: Store, New and Module.
// The webhook services that call it arrive with P11.
//
// # Testing
//
//   - sql_test.go: TestAccContract runs the notifications contract
//     (adapters/storetest.Run) on SQLite in memory and in a file through
//     sqltest.RunModule. The metadata store's contract runs the same cases
//     on PostgreSQL.
//   - webhooks_test.go: TestAccWebhooksFaults (a fault injected after a
//     successful statement fails the write or read), TestAccWebhooksSealed
//     (the secrets refuse a read without the keyring) and
//     TestAccWebhooksUnmigrated (every call reports the driver's error on a
//     database without the schema).
package sql
