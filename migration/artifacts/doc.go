// Package artifacts is the artifacts module's root: its settings, which app
// derives from the configuration, and its constructor, New(Deps), with the
// module's background wiring, the remote sources and the garbage collection
// loop.
//
// # Role
//
// artifacts module, module root. Only app imports a module root; other
// modules reach artifacts through artifacts/api (decision 0044, dependency
// rules). No package of the module imports its root either, so app hands
// each setting to the code that reads it as a value.
//
// # Inputs
//
//   - Settings: app/config's Artifacts accessor builds them from the
//     configuration's download, search, aql_timeout, build, deploy, npm,
//     cran, huggingface, maven, nuget, remote, gc, conda, helm and pypi
//     sections.
//   - Deps, which app passes New: the metadata store, the hot tier's blob
//     store, the keyring (nil without a master key), which New adds to the
//     formats' settings for the Terraform backend, the access module's API,
//     the work queue, the garbage collector app builds over its multipart
//     and registry upload services, the Settings, the logger, and
//     PrepareNIMModelUpload, NIM's upload preparation, which lives in an
//     ecosystem http package that the module root may not import.
//
// # Outputs
//
//   - Settings, one field per configured value that the module's code
//     reads, named as that code names it, and the formats' settings.
//   - Module, built by New: API, the module's artifacts/api surface
//     (Repositories and Layouts); RegisterWork, which registers the item
//     service's derived work and cache maintenance with the work queue;
//     OpenRemoteSources, which opens the remote policies, the Maven
//     eager-companion queue and both Smart Remote runtimes; Start, which
//     runs the garbage collection loop; Close, which stops what
//     OpenRemoteSources started; and the accessors that the router and the
//     Debian snapshot work take: FormatSettings, GC, RemoteHTTP,
//     RemoteSources, RegistrySources, RemoteStatus and MavenEager.
//   - Deps.
//
// # Invariants
//
//   - New does no I/O: it binds and builds, and the store, the network and
//     the file system are reached only by the methods app calls after it.
//     It refuses only Deps without a store.
//   - app calls RegisterWork, OpenRemoteSources and Start in OpenRuntime's
//     order, after the work queue opens and before it runs: a work kind with
//     no handler waits (decision 0033), and the loops must start last so a
//     failed start closes everything it opened.
//   - The persisted names stay byte for byte: the setting
//     remote.internal_policy_key, which holds the remote policy key without
//     a master key (a new name would mint a new key and change every remote
//     cache identity), and the work kinds remote.smart and
//     remote.smart.registry (queued jobs must find their handler).
//
// # References
//
//   - Context map, artifacts row, as
//     https://github.com/weaveplatform/artifactweave/issues/958 records it:
//     owns repositories of every class, items, content, trash, garbage
//     collection, package index, the 49 ecosystems, remote retrieval, virtual
//     resolution, build info, AQL and searches; phases P3 to P10 and P28.
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", lines 172-256 (line 173: module.go), "Remote and virtual
//     repositories", lines 635-666, and "Composition root", lines 746-804
//     (app's gc.go becomes Start, app's remote.go the module's wiring of
//     remote sources).
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md, "Public
//     types and published api surface" (artifacts/api has no Settings),
//     "Composition and HTTP" (one New(Deps) per module, constructors do no
//     I/O, per-module settings are accessor methods on app/config.Config),
//     "Dependency rules", rule 7 (only app wires modules), and "Names the
//     migration introduces" (RegisterWork, OpenRemoteSources and the
//     accessors; the Routes the target names is not created in P9d).
//   - Decision 0009: docs/research/decisions/0009-configuration-as-code.md
//     (the application configuration the settings come from).
//   - Decision 0004:
//     docs/research/decisions/0004-storage-sql-metadata-and-blobs.md.
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md.
//   - Decision 0008: docs/research/decisions/0008-virtual-repositories.md.
//   - Decision 0010: docs/research/decisions/0010-retention-trash-and-gc.md
//     (the garbage collection loop under the storage-gc lease).
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md.
//   - Decision 0033: docs/research/decisions/0033-persistent-work-queue.md.
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration,
//     #p3-repositories-generic-artifacts-cli-aql-shape,
//     #p10-virtual-repositories,
//     #p28-existing-ecosystem-and-core-api-completion. P3 to P10 built the
//     module's packages.
//   - https://github.com/weaveplatform/artifactweave/issues/1093 (the
//     settings) and https://github.com/weaveplatform/artifactweave/issues/1094
//     (the constructor and the background wiring).
//
// # Status
//
// Implemented P9d: the module settings, New and Module, with the remote
// wiring and the garbage collection loop moved from app. The module's routes
// stay in the server; the multipart upload service and the garbage
// collector stay built by app.
//
// # Testing
//
//   - TestUnitNew: a store is required, New calls none of its methods, the
//     API is bound and the keyring reaches the Terraform backend's settings.
//   - TestAccLogGC, TestAccRemotePolicyKeyInitialization and
//     TestAccRemoteWiringInstallsFormatHooks: the log of each kind of
//     garbage collection pass, the policy key's first-start initialization
//     under concurrency, and the format hooks the remote wiring installs.
//   - TestUnitRegisterWorkStopsAtTheItemServiceError,
//     TestUnitOpenRemoteSourcesPolicyKeyFailures,
//     TestUnitOpenRemoteSourcesRefusesABadPrivateOrigin and the four
//     TestUnitRemote tests, over a store with one failing call: each error
//     path's error, a failed start that opens nothing and leaves the policy
//     key and its lease as they were, and the remote reads' fences refusing
//     a read that no longer stands.
//   - app's tests start the module through OpenRuntime, and the remote
//     suites under test/server/artifacts read remote content through it.
package artifacts
