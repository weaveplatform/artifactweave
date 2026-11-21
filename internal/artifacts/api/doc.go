// Package api is the artifacts module's published surface for other modules:
// the repository operations they call (Repositories, with RepoFilter and the
// shared names ErrChanged, MsgKeyExists and IsNull), layout checks and bounded
// package-index snapshots and guarded file removal, which API holds as the
// module binds them. It also supplies immutable event facts and secret-free
// project repository projections.
// The repository configuration model is its subpackage repo.
//
// # Role
//
// artifacts module, api layer: what other modules may call. Other modules
// reach artifacts only through this package and its subpackages, and it
// imports only the standard library, kernel and its own repo subpackage (decision
// 0044, dependency rules 5 and 6), so it declares its own types rather than
// naming the module's core or store types: LayoutDefinition mirrors the
// layout engine's Definition and RepoFilter the store's repository filter.
// The repo subpackage imports kernel/antpath, which the api rule allows, so a
// dependency listing of this package shows that kernel package too.
//
// # Inputs
//
// The package declares contracts and immutable facts; their implementations
// consume current authority, transactional context, index identities, bounded
// page requests and descriptive storage facts. The artifacts module implements
// the interfaces:
// internal/artifacts/core/repos's NewRepositories binds Repositories over
// the store and access's Keys and Projects, and artifacts/core/layout's
// Checker implements Layouts; pkgindex.NewReader supplies PackageIndex, and
// removal.New supplies ItemRemoval over the normal item service and current access.
//
// # Outputs
//
//   - Repositories: reading, listing, creating, updating and deleting
//     repository configurations, requesting a reindex, and the checks and
//     merge rules that every entry point applies before it writes one
//     (Protected, PrepareCreate, CheckMembers, CheckKeypairs,
//     CheckEnvironments, Merge and OwesReindex). RepoFilter narrows a
//     listing.
//   - ErrChanged, MsgKeyExists and IsNull: the sentinel a delete returns when
//     the content changed under it, the message of a create whose key is
//     taken, and the test for a null value in an update body or descriptor,
//     which callers of Repositories use with its results.
//   - Layouts, with LayoutDefinition: the check that configuration runs on a
//     layout definition before it stores it.
//   - PackageIndex: bounded repository/package/version/file snapshots and
//     comparator retention cutoffs. Callers hold current authority; snapshots
//     disclose no credentials and never authorize deletion.
//   - StorageUsage supplies canonical logical byte totals. StorageWatch
//     prepares content/ownership accounting before mutation and records actual
//     row deltas for whole-operation quota settlement, without credentials.
//   - ItemRemoval: current credential/native Delete and exact file facts fence
//     normal item/trash/index/event mutations. The owner's selection check and
//     durable acknowledgement join the deletion transaction; failures roll it back.
//   - API: the contracts as the module binds them, consumed by configuration,
//     retention and the application composition.
//
// # Invariants
//
//   - Repositories methods preserve the error behind them, so callers keep their own
//     wrapping and errors.Is finds the store's sentinels and ErrChanged.
//     Every text (ErrChanged, MsgKeyExists, the validation and layout
//     messages) stays byte-identical, because clients and logs show it.
//     PackageIndex names projection failures while preserving errors.Is.
//   - Every Repositories method that takes a context takes part in the store
//     transaction the context carries, so the configuration descriptor's
//     deletes, writes and reindex requests commit or roll back together
//     (decision 0009).
//   - LayoutDefinition and RepoFilter have the fields of the types they
//     mirror, in the same order and of the same types, so the module
//     converts them with plain conversions that stop compiling if either
//     side changes.
//
// # References
//
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Public types and published api surface" (artifacts/api publishes
//     repo, Repositories and Layouts, and no Settings), "Dependency rules"
//     and "Names the migration introduces".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 174 to 185 (artifacts/api), line 1015
//     (internal/artifacts/core/repos published as api.Repositories) and
//     line 1053 (the layout engine published through api.Layouts).
//   - Decision 0003: docs/research/decisions/0003-repository-model.md
//     (validation, defaults and protected keys stay in the module; the api
//     only exposes them).
//   - Decision 0009: docs/research/decisions/0009-configuration-as-code.md
//     (the descriptor and the REST APIs call the same operations; a null
//     deletes an element).
//   - Decision 0013: docs/research/decisions/0013-projects-and-environments.md
//     (a repository's environments).
//   - Decision 0040: docs/research/decisions/0040-terraform.md (the reindex
//     an update that sets terraformType owes).
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration, epic
//     ART (https://github.com/weaveplatform/artifactweave/issues/941).
//   - https://github.com/weaveplatform/artifactweave/issues/1009 and
//     https://github.com/weaveplatform/artifactweave/issues/1094 (API)
//
// # Status
//
// Repositories, Layouts and API implemented in P9d. Not created, because no
// other module calls them today (decision 0044): the target layout's Items,
// Packages, Builds and Search; Gate (hooks.go), with the download and deploy
// gates of P17; and
// Repositories.SetProject, which a project attach will call (P11). The
// artifacts settings are not published here (decision 0044). P11 events.go
// publishes typed artifact/property, Docker and build topics and immutable
// fact values, independently of vendor webhook envelopes.
// P11 ItemRemoval supplies retention's guarded deletion boundary; retention
// retains policy interpretation, run state and reporting.
//
// # Testing
//
//   - TestUnitIsNull: null with and without surrounding white space, and
//     values that are not null.
//   - The implementations are tested where they live:
//     internal/artifacts/core/repos's TestUnitRepositories* tests, one per
//     Repositories method, and artifacts/core/layout's
//     TestUnitLayoutCheckerMatchesCompile.
package api
