// Package nuget is the format of NuGet repositories (decision 0038). It
// reads each package's .nuspec, indexes a .nupkg or .snupkg at its
// canonical name as a file of its version (decision 0018) with the
// per-version Metadata the v3 and v2 feeds are built from, and keeps the
// nuget.* properties. Every write reaches it through the item service, so
// a package pushed by dotnet nuget push, deployed by jf rt upload, or
// landed by a copy, a move or a restore is indexed the same way.
//
// Every repository behaves as one created with 7.x's Enforce Layout, with
// no switch (DV-111): versions are normalised, a version has one package
// whichever route brings it, and a .nupkg or .snupkg that is no valid
// package, or is not at its canonical name, is refused (Admit). Files of
// any other name, legacy .symbols.nupkg included, are stored as plain
// items.
//
// A version is its package, the id lower-cased, and its version,
// normalised and lower-cased (PackageName, VersionKey). Its files are
// every file at one of its canonical names: its .nupkg and, when pushed,
// its .snupkg. Admit keeps that to one of each, but a trash restore, which
// no format refuses, can bring back a second; the one deployed first, a
// tie broken by path, is the one the Metadata describes, so the hooks and
// a reindex agree whatever the order of events.
//
// There is no unlisted state (decision 0038, "Delete and relist"), so no
// property changes the index and the format is no format.Annotator.
//
// # Role
//
// artifacts module, ecosystem layer: the format rules of the NuGet slice
// (package type nuget), including the PDB symbol keys its symbol server
// reads by. Its Handler plugs into the item service through the format
// hooks (decision 0032), so every write and delete in a NuGet repository
// reaches it whichever API carried it. The slice's protocol package,
// nuget/http, builds the v3 and v2 feeds from the index and the Metadata
// kept here. Its upstream rule, the v2 empty-feed projection, plugs into
// core remote's shared retrieval and cache policy (decision 0007) through
// the policy's response hook; this package imports core remote, which
// never imports it back (decision 0044).
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for NuGet repositories to Handler; the work
//     queue, which runs the PDB extraction (KindSymbols) and the
//     maxUniqueSnapshots cleanup (KindSnapshots) through Handler.Work, the
//     cleanup every nuget.snapshot_cleanup_interval as the application
//     schedules it; and the protocol package (nuget/http), which parses ids
//     and versions, reads pushed and cached packages, decodes the Metadata
//     its feeds are built from, keys PDB reads (PDBKey, SymbolPath) and
//     checks a remote repository's package before it is cached
//     (ValidateRemote); and core remote's policy (remote.Policy), which
//     hands each upstream response it fetches, to fill the cache or to
//     deliver without storing, to ProjectEmptyFeed through its
//     ProjectResponse hook, installed by the application on both remote
//     policies.
//   - Driven: the format hooks' environment (format.Env), through which
//     Handler reads stored packages, keeps the package index, the nuget.*
//     properties and the extracted PDBs, enqueues its work, removes the
//     pre-releases the cleanup drops, and reads a remote repository's
//     cache for its reindex; and core remote, whose PolicyError classifies
//     a failure to close the upstream body the projection replaces.
//
// # Outputs
//
//   - Names and versions: the file extensions (PackageExt, SymbolsExt and
//     LegacySymbolsExt), FileName, SymbolsFileName, PackageName,
//     VersionKey, ValidID and MaxIDLength; Version, ParseVersion and
//     NormalizeVersion.
//   - Packages: Nuspec with its Dependency, DependencyGroup and
//     PackageType, ParseNuspec, ReadPackage, ReadStored, SymbolsPackageType
//     and ErrNotPackage.
//   - The index document: Metadata and DecodeMetadata, with the
//     SymbolSource and SymbolFile records of the PDBs each .snupkg yields.
//   - The nuget.* property keys (PropID and the rest).
//   - Symbols: PDBKey and SymbolPath, a PDB's Microsoft symbol key and its
//     canonical path.
//   - Handler: the hooks that inspect, admit, serialize, index, classify,
//     delete and reindex NuGet files, and the work they owe (KindSymbols,
//     KindSnapshots, Deleter).
//   - ValidateRemote: the identity and archive checks of a remote
//     repository's .nupkg.
//   - ProjectEmptyFeed: the response hook that serves a v2 feed's bodyless
//     upstream 204 as the one fixed empty Atom feed.
//
// # Invariants
//
//   - A version has at most one .nupkg and one .snupkg whichever route
//     brings them: Admit refuses a duplicate with 409, and GuardWrite
//     serializes a version's writes so that two transactions cannot both
//     find it absent and store it.
//   - Reading a package is bounded (decision 0038): a zip of more than
//     2^20 entries, or a .nuspec over 1 MiB, is no valid package.
//   - Metadata follows from the version's files and their stored checksums
//     alone, so a reindex writes the same bytes.
//   - A remote package is cached as its upstream served it, signature
//     included, and only after its identity and every zip entry are
//     checked; it is never re-signed, and a well-formed signature is not
//     taken as trusted.
//   - Refusal texts (ErrNotPackage and Admit's reasons) reach dotnet and
//     the JFrog CLI in the response body, so they stay byte-identical.
//   - The empty-feed projection applies only to a request that opts in
//     (source.Resource.NuGetEmptyFeed, set for v2 feeds and queries) and
//     only to a bodyless 204. It is part of the representation's cache
//     identity, whose term ("nuget-empty-feed-v1") core remote keeps, and a
//     204 of any other request keeps the policy's generic handling.
//
// # References
//
//   - Decision 0038: docs/research/decisions/0038-nuget.md, and research 29
//     (docs/research/29-nuget.md).
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (format hooks and optional interfaces), and decision 0018:
//     docs/research/decisions/0018-package-index.md.
//   - docs/operations/nuget-remote.md (remote NuGet repositories), and
//     docs/implementation/remote-retrieval-contract.md, "NuGet v2 empty
//     feed projection" (decision 0038, #629), which cites the NuGet
//     client's reading of a 204 as an empty feed:
//     https://github.com/NuGet/NuGet.Client/blob/dev/src/NuGet.Core/NuGet.Protocol/LegacyFeed/V2FeedParser.cs.
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md
//     (format rules classify and validate; core remote owns transport and
//     cache policy), and decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Artifacts and remote".
//   - JFrog NuGet repositories and the migration tool that normalises a
//     repository's layout, as decision 0038 cites them:
//     https://docs.jfrog.com/artifactory/docs/nuget-repositories and
//     https://docs.jfrog.com/artifactory/docs/migration-tool.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 231 (ecosystem/nuget), line 379 (one slice per package type),
//     line 658 (remote_empty_feed.go), line 664 (the P10 virtual.go) and
//     lines 997 and 1014 (the mapping rows).
//   - Plan: docs/implementation/plan.md#p8-pypi-nuget-helm-terraform-locals,
//     #p9-remote-repositories and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/542 (the format
//     handler), https://github.com/weaveplatform/artifactweave/issues/571
//     (PDB symbol serving), https://github.com/weaveplatform/artifactweave/issues/629
//     (remote v2 and symbols), https://github.com/weaveplatform/artifactweave/issues/700
//     (reindex of cached remote packages),
//     https://github.com/weaveplatform/artifactweave/issues/1018 and
//     https://github.com/weaveplatform/artifactweave/issues/1021
//
// # Status
//
// Implemented P8, with remote retrieval, the remote v2 feed and symbols
// included, in P9. Moved here in P9d. Roadmap: virtual.go (P10), the Merger
// that merges NuGet registrations for virtual repositories.
//
// # Testing
//
//   - nuspec_test.go and version_test.go: nuspec parsing (namespaces,
//     casing, missing fields, symbols packages, invalid packages), valid
//     ids, and version normalisation and keys.
//   - handler_test.go: indexing on deploy, canonical names, duplicate
//     refusal, overwrite, symbols linking, a file leaving its version, a
//     second package a restore brings back, reindex and a remote
//     repository's cache.
//   - cleanup_test.go: the maxUniqueSnapshots cleanup.
//   - symbols_test.go, symbols_concurrency_test.go and pdb_test.go: PDB
//     extraction and its owners, overwrite, copy and reindex, opposite moves
//     on PostgreSQL, PDB keys and symbol paths, and FuzzPDBKey, whose seed
//     corpus is in code and runs in make test (FUZZ_PKGS).
//   - guard_test.go and fault_test.go: write lock failures roll back, and
//     store, drop and blob faults.
//   - contract_test.go: the format contract suite (artifacts/core/formattest).
//   - remote_test.go and remote_reindex_test.go: a remote package's
//     admission, payload limits, entry names and zip directory bounds, and
//     the reindex of a remote repository's cache, with the access test kit
//     (access/accesstest) for the reindex's credential and its revocation.
//   - remote_empty_feed_test.go: the empty-feed projection through core
//     remote's policy against a fake upstream (caching, refresh, offline
//     and no-store reads, validation and the publication fence, cache
//     identity, generic 204 handling, deceptive 204s, and each transport
//     body closed once); remote_fixture_test.go holds the policy fixture
//     they share with nothing else.
package nuget
