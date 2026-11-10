// Package port declares the releases module's store ports: the bundle
// store, the distribution store and Store, the part of the metadata store
// the module uses.
//
// # Role
//
// releases module, core layer, store ports. The metadata store's SQL and
// in-memory stores implement BundleStore and DistributionStore, and the
// metadata store satisfies Store; STO-15 moves those implementations into
// the module's adapters. The ports take and return the module's core/model
// values and kernel.Page, and join the unit of work of platform/tx.
//
// # Inputs
//
//   - Release bundle versions with their artifacts, keep marks, retention
//     audits and exports, and distribution trackers with their jobs, to
//     store; names, versions, ids and peers to look up; a BundleFilter with
//     a kernel.Page, or job statuses, to narrow a listing.
//   - The context of the caller's transaction, which every call joins.
//
// # Outputs
//
//   - BundleStore: Create, Get, Update, ReplaceArtifacts, Delete, List and
//     Artifacts; SetKeep, Kept, AddAudit and Audits; PutExport, Export and
//     DeleteExport.
//   - DistributionStore: CreateTracker, Tracker, UpdateTracker and
//     Trackers; PutJob, Jobs and JobsForPeer.
//   - BundleFilter.
//   - Store: tx.Runner, Bundles and Distribution.
//
// # Invariants
//
//   - Errors are the kernel's sentinels, wrapped: kernel.ErrNotFound for a
//     missing bundle or tracker, kernel.ErrConflict for a bundle version
//     that exists, two artifacts with one target path, two jobs for one
//     peer, or an artifact list changed after signing.
//   - A bundle version's artifact list is frozen once it is signed
//     (model.BundleState.Editable).
//   - Retention audits outlive the bundle versions they record: Delete keeps
//     them, and AddAudit takes a bundle that no longer exists.
//
// # References
//
//   - Decision 0023: docs/research/decisions/0023-release-bundles-and-distribution.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence" and "Names the migration introduces", which lists
//     Store).
//   - Research 15: docs/research/15-data-model.md, lines 709 to 714 (the
//     bundle and distribution tables) and line 760 (bundle retention as
//     built).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 100 to 103 (a storage skeleton holds core/port/store.go and its
//     store adapters), line 351 (the releases module's core/port), lines 577
//     and 578 (each module owns its store ports) and line 1055 (the metadata
//     store split into each module's core/port/store.go).
//   - Plan: docs/implementation/plan.md#p2-stores, task 1,
//     #p15-release-bundles-v1-and-distribution and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1053
//
// # Status
//
// Implemented P2: BundleStore and DistributionStore, with the metadata
// store's in-memory, SQLite and PostgreSQL stores (pull request 3). Staged
// in P9d, where Store is declared. The services that call the ports arrive
// with P15.
//
// # Testing
//
// The package declares only ports, so it has no tests of its own. The
// metadata store's Bundles, BundleRetention, BundleExports and Distribution
// contract subtests run the ports on its in-memory, SQLite and PostgreSQL
// stores, and compile-time assertions in the in-memory and SQL stores'
// tests show that both satisfy Store.
package port
