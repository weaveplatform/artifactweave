// Package port declares the notifications module's store ports: the
// webhook store and Store, the part of the metadata store the module uses.
//
// # Role
//
// notifications module, core layer, store ports. The metadata store's SQL
// and in-memory stores implement WebhookStore, and the metadata store
// satisfies Store; STO-14 moves those implementations into the module's
// adapters. The ports take and return the module's core/model values and
// kernel.Page, and join the unit of work of platform/tx.
//
// # Inputs
//
//   - Webhook subscriptions and delivery records to store, keys to look up,
//     and a WebhookFilter or DeliveryFilter to narrow a listing.
//   - The context of the caller's transaction, which every call joins.
//
// # Outputs
//
//   - WebhookStore: Create, Get, Update, Delete and List for subscriptions;
//     AddDelivery, Deliveries and PruneDeliveries for delivery records.
//   - WebhookFilter and DeliveryFilter.
//   - Store: tx.Runner and Webhooks.
//
// # Invariants
//
//   - Errors are the kernel's sentinels, wrapped: kernel.ErrConflict for a
//     key that exists, kernel.ErrNotFound for a missing subscription.
//   - Handler secrets are sealed at rest (decision 0017) and come back
//     unsealed from Get and List.
//   - Deleting a subscription deletes its delivery records.
//
// # References
//
//   - Decision 0011: docs/research/decisions/0011-webhooks.md.
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence" and "Names the migration introduces", which lists
//     Store).
//   - Research 15: docs/research/15-data-model.md, lines 684 to 686 (the
//     webhook tables).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 100 to 103 (a storage skeleton holds core/port/store.go and its
//     store adapters), line 322 (WebhookStore), lines 577 and 578 (each
//     module owns its store ports) and line 1055 (the metadata store split
//     into each module's core/port/store.go).
//   - Plan: docs/implementation/plan.md#p2-stores, task 1,
//     #p11-projects-webhooks-retention-replication and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1053
//
// # Status
//
// Implemented P2: WebhookStore, with the metadata store's in-memory, SQLite
// and PostgreSQL stores (pull request 3). Staged in P9d, where Store is
// declared. The webhook services that call it arrive with P11.
//
// # Testing
//
// The package declares only ports, so it has no tests of its own. The
// metadata store's Webhooks contract subtest runs WebhookStore on its
// in-memory, SQLite and PostgreSQL stores, and compile-time assertions in
// the in-memory and SQL stores' tests show that both satisfy Store.
package port
