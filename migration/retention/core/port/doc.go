// Package port declares the retention module's store ports: the cleanup
// store, the archive store and Store, the part of the metadata store the
// module uses.
//
// # Role
//
// retention module, core layer, store ports. The metadata store's SQL and
// in-memory stores implement CleanupStore and ArchiveStore, and the metadata
// store satisfies Store; STO-14 moves those implementations into the
// module's adapters. The ports take and return the module's core/model
// values, the retention api's RunStatus and kernel.Page, and join the unit
// of work of platform/tx. ArchiveStore keeps the archive policies and
// executions only: the archived items and the cold blob and replica indexes
// are the artifacts module's records, which the metadata store reaches
// through accessors of their own (decision 0044, "Ownership of shared
// models"). The metadata store aliases every name here (its
// bridge_retention.go) until PRO-02 deletes the bridge.
//
// # Inputs
//
//   - Cleanup and archive policies, cleanup runs and archive executions to
//     store, keys and IDs to look up, and a PolicyFilter, RunFilter or
//     ExecutionFilter with a kernel.Page to narrow a listing.
//   - The context of the caller's transaction, which every call joins.
//
// # Outputs
//
//   - CleanupStore: CreatePolicy, Policy, UpdatePolicy, DeletePolicy and
//     Policies; CreateRun, Run, UpdateRun, Runs and LastRun.
//   - ArchiveStore: CreatePolicy, Policy, UpdatePolicy, DeletePolicy and
//     Policies; CreateExecution, Execution, UpdateExecution and Executions.
//   - PolicyFilter, RunFilter and ExecutionFilter.
//   - Store: tx.Runner, Cleanup and Archive.
//
// # Invariants
//
//   - Errors are the kernel's sentinels, wrapped: kernel.ErrConflict for a
//     policy key that exists, kernel.ErrNotFound for a missing record.
//   - Deleting a policy keeps its runs or executions, which the run views
//     and the execution history still show.
//   - Runs and Executions list newest first, one kernel.Page at a time.
//
// # References
//
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md.
//   - Decision 0024: docs/research/decisions/0024-cold-storage-tier.md
//     (archive policies and executions are retention's records; the cold
//     indexes reuse the artifacts index code).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Ownership of shared models", "Type-alias bridges" and "Names the
//     migration introduces", which lists Store).
//   - Research 15: docs/research/15-data-model.md, lines 687 and 688 (the
//     cleanup tables) and lines 715 and 716 (the archive tables).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 100 to 103 (a storage skeleton holds core/port/store.go and its
//     store adapters), line 328 (the retention module's core/port), lines 577
//     and 578 (each module owns its store ports) and line 1055 (the metadata
//     store split into each module's core/port/store.go).
//   - Plan: docs/implementation/plan.md#p2-stores, task 1,
//     #p11-projects-webhooks-retention-replication,
//     #p16-cold-storage-archive-and-retention and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1053
//
// # Status
//
// Implemented P2: CleanupStore and ArchiveStore, with the metadata store's
// in-memory, SQLite and PostgreSQL stores (pull request 3). Staged in P9d,
// where ArchiveStore gave its archived-item methods and the cold indexes to
// accessors of their own and Store is declared. The services that call the
// ports arrive with P11 (cleanup) and P16 (the cold-storage tier).
//
// # Testing
//
// The package declares only ports, so it has no tests of its own. The
// metadata store's CleanupPolicies, CleanupRuns, ArchivePolicies and
// ArchiveExecutions contract subtests run the ports on its in-memory, SQLite
// and PostgreSQL stores, and compile-time assertions in the in-memory and
// SQL stores' tests show that both satisfy Store.
package port
