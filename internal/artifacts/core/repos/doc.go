// Package repos holds the repository operations that more than one entry
// point performs: the repositories API and the configuration descriptor
// (PATCH /api/system/configuration, config apply and the bootstrap
// descriptor). Its errors are apierror values, so every entry point answers
// a bad configuration with the same status and message.
//
// # Role
//
// artifacts module, core layer: repository operations behind
// artifacts/api.Repositories, which NewRepositories binds over the store and
// access/api's Keys and Projects, the only way it reaches the access module
// (decision 0044, dependency rule 6). As artifacts core, it never imports
// the module's adapters or an ecosystem http package (dependency rule 8).
//
// # Inputs
//
//   - Driving: the repositories API handlers and the configuration
//     descriptor, through the package's functions or the Repositories that
//     NewRepositories returns.
//   - Driven: the metadata store (repository configurations, items and
//     their properties, blob references, the package index, trash entries
//     and the work queue, through Store, which embeds the trash can's
//     store), the trash service
//     (artifacts/core/trash), which keeps a deleted repository's content,
//     and accessapi.Keys and accessapi.Projects, for the key pair and
//     environment checks.
//
// # Outputs
//
//   - NewRepositories: the artifacts module's Repositories.
//   - A configuration prepared and checked for a write (PrepareCreate,
//     CheckMembers, CheckKeypairs, CheckEnvironments), a patch merged into
//     a stored configuration (Merge), whether an update owes a reindex
//     (OwesReindex) and whether a key may be deleted (Protected).
//   - Remove: the whole-repository delete.
//   - Errors: apierror values that answer 400 with the message every entry
//     point shares, the store's sentinels, and artifactsapi.ErrChanged.
//
// # Invariants
//
//   - A delete is one transaction, however large the repository, so a
//     failure part of the way leaves it whole, its content where it was and
//     the trash can as it was.
//   - While the trash can is on, a delete keeps the blob references of the
//     repository's own content for a restore, until a restore takes them
//     over or a purge releases them.
//   - Validation messages and artifactsapi.MsgKeyExists reach clients and
//     stay byte-identical.
//
// # References
//
//   - Decision 0003: docs/research/decisions/0003-repository-model.md
//     (validation, defaults and protected keys).
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md
//     (a repository delete and the trash can).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules" and "Names the migration introduces".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 211 (core/repos), line 1015 (the mapping row: published as
//     api.Repositories) and rule 2 at lines 35 to 36.
//   - RTDEV-84660, which Remove cites: 7.x fixed a restore of a deleted
//     repository's root copying the root's properties onto what it
//     restored, so those properties go with the repository.
//   - Plan: docs/implementation/plan.md#p3-repositories-generic-artifacts-cli-aql-shape
//     (the descriptor PATCH and config apply) and
//     #p9d-modular-monolith-migration, epic ART
//     (https://github.com/weaveplatform/artifactweave/issues/941).
//   - https://github.com/weaveplatform/artifactweave/issues/1013 and
//     https://github.com/weaveplatform/artifactweave/issues/1088
//
// # Status
//
// Implemented P3 to P9c. Moved here in P9d.
//
// # Testing
//
//   - repos_test.go: merges, protected keys, members, key pairs,
//     environments and deletes, with their failures.
//   - remove_test.go: deletes with the trash can on and off, restores,
//     remote caches, a large repository and content changing during the
//     delete.
//   - api_test.go: one TestUnitRepositories* test per Repositories method,
//     each checking the binding answers as the direct call does.
package repos
