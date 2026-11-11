// Package sql is the composite SQL store: the platform's store and every
// module's SQL store over one dialect store, so a transaction begun through
// any of them spans them all. It holds Open, which migrates every module's
// stream and runs their hooks, the module list that the migrator, the hooks
// and Reseal follow, and the registration of the artifacts module's layout
// watch with the platform's config store.
//
// # Role
//
// app, adapter layer (SQL): the composition root's metadata store on SQLite
// and PostgreSQL. Store embeds the *dialect.Store that holds the pool, the
// dialect and the keyring and runs the unit of work, and keeps the
// platform's and each module's SQL store over it as fields, with one
// accessor for each of their stores. The sqlite and postgres packages below
// it open Store on their driver; app opens it through them, and the tests of
// the modules reach it only through app/storetest, as a module may import
// nothing else from app. It imports the dialect layer, the keyring, the
// platform's SQL store and every module's adapters/sql, and never app.
//
// # Inputs
//
//   - Open: a pool, its dialect.Dialect and Options: whether to skip the
//     migrations, and the keyring that seals secret columns, or none.
//   - New: a pool whose streams are migrated already, its dialect and the
//     keyring.
//   - Reseal: a store opened with the keyring whose active version every
//     sealed value is to be under.
//
// # Outputs
//
//   - Store: the platform's Settings, Leases, WorkQueue, Configs, Instance,
//     Outbox, Uploads, Replays and RequestBudgets; the artifacts module's
//     Repos, RemoteCache, Items, Trash, Packages, Blobs, Replicas,
//     GitLFSLocks, BowerRegistrations, Builds, ArchivedItems, ColdBlobs and
//     ColdReplicas; the access module's Users, Groups, Permissions, Tokens,
//     Keys, Projects and APIRestrictions; Webhooks; Cleanup and Archive;
//     Replications, Peers, Federation and Platform; Bundles and
//     Distribution; Workers; and the dialect store's Tx, AfterCommit and
//     Close.
//   - Open: the store, pinged, with every stream migrated and every
//     module's AfterMigrate run unless Options.SkipMigrate; or the first
//     error, with the pool closed.
//   - Modules: every module's dialect.Module, in the canonical order.
//   - Reseal: how many sealed values it sealed again.
//
// # Invariants
//
//   - Migrations, AfterMigrate hooks and Reseal follow the canonical module
//     order of Modules: the platform, then artifacts, access,
//     notifications, retention, federation, releases and workers (decision
//     0044, "Persistence"). The names key the modules' rows in
//     schema_migrations, so a renamed module would find no history and
//     apply its baseline again.
//   - Every sealed column is registered by its module's dialect.Module
//     value, with the purpose and the row id its store seals under, which
//     are authenticated with the value, so Reseal and key rotation reach
//     every sealed value (decision 0017). TestAccSealedColumnsComplete fails
//     on a purpose that the source seals with and no module registers.
//   - New registers the artifacts module's layout watch with the platform's
//     config store, so a layout document that is created, changed or
//     deleted fences remote publication for the repositories that use it in
//     the same transaction (decision 0007).
//   - The platform's and the modules' stores are fields rather than
//     embedded, so none of their methods is promoted onto Store, where it
//     could shadow an accessor without a compile error; the embedded
//     dialect store gives Store only Tx, AfterCommit and Close.
//   - The tables follow the schema conventions of platform/sql/dialect, and
//     the SQL text, the table names, the purpose strings, the row ids and
//     the error texts are the modules' stores' own.
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//     (one SQL store over a dialect layer, after hostweave's).
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md
//     (layout fencing).
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md
//     (sealed columns, their purposes and row ids, and re-sealing).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": one dialect store shared by every module's stores, one
//     dialect.Module per module in the canonical order, layout fencing as a
//     config watch; "Names the migration introduces").
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 627 and 628 (the migrator runs the platform first, then the
//     modules in the order app registers them) and line 751 (app's
//     platform.go opens and migrates the SQL store).
//   - Plan: docs/implementation/plan.md#p2-stores and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1098
//
// # Status
//
// Implemented P2: the metadata store on SQLite and PostgreSQL, with Reseal
// (pull request 3); later phases added a store for each entity they
// introduced. Moved here in P9d: each module's stores split into its own
// adapter (issues 1056 to 1062), leaving this package the composition over
// them, which issue 1098 moved under app.
//
// # Testing
//
//   - reseal_test.go: TestUnitModulesRegisterInTheCanonicalOrder,
//     TestAccSealedColumnsComplete and TestAccResealSealsPlainValues.
//   - sealscan_test.go: TestUnitScanSealsReadsTheFixture,
//     TestUnitScanSealsReportsUnreadablePurposes and
//     TestUnitCheckSealedPurposes, over the packages in testdata/sealscan.
//   - store_test.go: TestUnitDialectStoreShadowsNoAccessor.
//   - The composite contract and suites run on Store in the sqlite and
//     postgres packages' tests.
package sql
