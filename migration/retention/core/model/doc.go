// Package model holds the retention module's entity values: package and
// build cleanup policies and their runs, and the cold-storage tier's archive
// policies, executions and restore requests.
//
// # Role
//
// retention module, core layer, entity values. The module's store ports take
// and return them, and the cleanup, archive and restore services of P11 and
// P16 will build on them. A run's status is the retention api's RunStatus,
// which federation's full-sync runs report too. The package imports the
// standard library and retention/api. The shared types alias every name
// here (their bridge_retention.go) until PRO-02 deletes the bridge.
//
// # Inputs
//
//   - For RestoreRequest.Validate, a restore request as the cold-storage API
//     receives it.
//
// # Outputs
//
//   - CleanupPolicy with CleanupCriteria and CleanupItemType (package, build
//     and release bundle), and CleanupRun.
//   - ArchivePolicy with ArchiveFamily (retention v1 and smart archiving v2),
//     ArchiveExecution with ExecutionKind, ExecutionState and Progress, and
//     RestoreRequest with RestoreType and Validate.
//   - MaxRestoreItems and the sentinel ErrInvalidRestore.
//
// # Invariants
//
//   - A cleanup policy's criteria keep the legacy month fields, so a policy
//     written by an old client reads back as it was written.
//   - An archive policy's criteria are kept in its family's wire shape,
//     because the two families select differently (decision 0024).
//   - One restore takes at most MaxRestoreItems items, as JFrog's does.
//
// # References
//
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md
//     (cleanup policies).
//   - Decision 0024: docs/research/decisions/0024-cold-storage-tier.md
//     (archive policies, executions and restores).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence", "Type-alias bridges").
//   - Research 07: docs/research/07-webhooks-retention-projects.md. Research
//     16: docs/research/16-enterprise-products.md (cold storage). Research
//     15: docs/research/15-data-model.md, lines 687 and 688 (the cleanup
//     tables) and lines 715 and 716 (the archive tables).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 100 to 103 (a storage skeleton ahead of its phase), lines 325 to
//     329 (the retention module), lines 577 and 578 (each module owns its
//     store ports) and line 1058 (the shared types split into each module's
//     core/model).
//   - Plan: docs/implementation/plan.md#p2-stores, task 0,
//     #p11-projects-webhooks-retention-replication,
//     #p16-cold-storage-archive-and-retention and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1053
//
// # Status
//
// Implemented P2: the cleanup and archive values among the shared domain
// types (pull request 3). Staged in P9d. The behaviour that uses them
// arrives with P11 (cleanup policies) and P16 (the cold-storage tier).
//
// # Testing
//
//   - model_test.go: TestUnitRunStatusTerminal (which run statuses are
//     final) and TestUnitArchive (Progress, including an execution with no
//     total; restore requests that validate, and an unknown type, nothing
//     to restore, too many items and no destination failing with
//     ErrInvalidRestore).
//
// The metadata store's CleanupPolicies, CleanupRuns, ArchivePolicies and
// ArchiveExecutions contract subtests store and read these values on its
// in-memory, SQLite and PostgreSQL stores.
package model
