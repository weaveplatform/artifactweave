// Package storekit holds the remote cache and layout fencing code that the
// artifacts module's SQL and in-memory adapters share: the cache's
// compare-and-swap rules and blob references over each adapter's own cache
// rows, and the fence that a layout edit puts on the remote repositories
// that use the layout.
//
// # Role
//
// artifacts module, adapter layer: the remote cache logic and layout
// fencing that adapters/sql and adapters/inmem share, so both backends keep
// one set of rules. Each adapter hands over its stores as Stores and its
// cache rows as RemoteCacheRecords: its RemoteCache accessor returns
// NewRemoteCache over them, and its LayoutWatch calls FenceLayoutSources.
// The target layout does not name this package; decision 0044 places it.
// The package imports the standard library, the kernel, the platform's tx
// package and the module's core and api only.
//
// # Inputs
//
//   - NewRemoteCache and FenceLayoutSources: an adapter's Stores and
//     RemoteCacheRecords.
//   - FenceLayoutSources: the name of the layout document that was created,
//     changed or deleted.
//   - The context of the caller's transaction, if there is one.
//
// # Outputs
//
//   - Stores and RemoteCacheRecords, what an adapter supplies.
//   - NewRemoteCache: the port.RemoteCacheStore of the adapter.
//   - FenceLayoutSources: a new source generation and the next policy
//     revision for each remote repository that uses the layout, with its
//     connection failures and offline windows cleared.
//
// # Invariants
//
//   - Every call joins the caller's unit of work: the cache runs each write
//     in a unit of work of Stores, which joins the transaction that the
//     caller's context carries, and FenceLayoutSources, which the platform's
//     config store calls from a watch inside the configuration document's
//     write, commits or rolls back with the layout edit (decision 0007).
//   - A layout edit fences every remote repository that uses the layout as
//     its own or its upstream layout (a repository without a layout
//     reference uses its package type's default), even when the
//     repository's reference has not changed: a layout defines both the
//     upstream representation and the canonical authority, so in-flight
//     publication under the old definition must fail its check.
//   - An entry that holds a raw blob rather than an item keeps a reference
//     on it: each write takes one for the entry's new blob and gives back
//     the one its old blob held, in the write's unit of work, so garbage
//     collection never removes content that a cache entry still names
//     (decision 0010).
//   - The error texts are those the metadata store used before the module
//     owned the code.
//
// # References
//
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md
//     (source generations, publication fences and cache entries).
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md
//     (blob references and garbage collection).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence": layout fencing as a config watch that the composition
//     registers; "Names the migration introduces", which lists Stores).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 601 to 613 (the artifacts adapters, which do not name this
//     package) and line 1055 (the metadata store split into module ports
//     and adapters).
//   - Plan: docs/implementation/plan.md#p9-remote-repositories and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1062
//
// # Status
//
// Implemented P9: the remote cache, as the metadata store's (pull request
// 646), and layout fencing (pull request 680). Staged in P9d: Stores.
//
// # Testing
//
// The package has no tests of its own. The artifacts contract's remote
// cache cases run the cache on both adapters (adapters/inmem
// TestUnitContract, adapters/sql TestAccContract) and on the metadata
// store's backends, whose RemoteLayoutFences cases edit layouts through the
// platform's config store and check the fences.
package storekit
