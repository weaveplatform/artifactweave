// Package model holds the artifacts module's entity values: items with their
// properties, statistics and trash records, packages and their versions with
// the version comparators, builds, remote cache entries and source
// identities, Bower registrations, Git LFS locks, archived items, the trash
// can and folder download settings, and the key pair roles of repositories.
//
// # Role
//
// artifacts module, core layer, entity values. The module's store ports
// (artifacts/core/port) take and return them, and the artifacts services
// build on them. PackageType and the version comparators stay out of the
// kernel, because ComparatorFor takes a repository package type, and a
// kernel that named one would import a module (decision 0044,
// "Persistence"). The package imports the standard library,
// artifacts/api/repo (the package types), the kernel (checksums and item
// paths) and platform/blob (the blob row a raw remote cache entry owns).
//
// # Inputs
//
//   - Full item paths, repository keys as clients send them, property keys
//     and values, version strings with their package type, and build names,
//     numbers and project keys to encode.
//
// # Outputs
//
//   - Items: Item, RootItem, ResolveRepoKey, CacheSuffix, ItemStats,
//     TrashRecord and TrashEntry; Properties with Set, Add, Has, Keys, Equal
//     and Clone; ValidatePropertyKey, CheckPropertyKeyLength,
//     TruncatePropertyValue, PropertyKeyMax, PropertyValueMax and
//     BuildPropertyValueMax, with the sentinels ErrInvalidPropertyKey and
//     ErrPropertyKeyTooLong; and Matrix, a download's matrix parameters.
//   - Packages: Package, PackageVersion and VersionFile; VersionComparator
//     with StringComparator and CreatedOrder, ComparatorFor, SortVersions
//     and AssignOrderKeys; and CompareSemver, CompareNuGet, ComparePEP440,
//     CompareMaven and CompareMercury.
//   - Builds: Build, BuildRun, BuildName, BuildModule, BuildFile,
//     BuildFileRef, BuildPromotion and BuildRetention, with BuildInfoRepo
//     and EncodeBuildInfoSegment.
//   - Remote cache: RemoteKind and its four kinds, RemoteCacheKey,
//     RemoteCacheToken, RemoteCacheEntry, RemoteSmartState,
//     RemoteSourceState and RemoteSmartPeer; and the source identity:
//     RepositoryIncarnation, SourceGeneration, SourceKind with ExternalHTTP
//     and ProjectRepository, and SourceIdentity.
//   - BowerRegistration, GitLFSLock and ArchivedItem.
//   - TrashcanSettings, FolderDownloadSettings, and KeypairRole with
//     KeypairPrimary and KeypairSecondary.
//
// # Invariants
//
//   - Properties keep each key's values sorted and unique, so listings and
//     comparisons are deterministic.
//   - A property key is at most PropertyKeyMax characters and is refused
//     past it; a value is cut, never refused, to PropertyValueMax UTF-16
//     code units, or BuildPropertyValueMax for build and module properties,
//     as Artifactory cuts it on write.
//   - EncodeBuildInfoSegment equals the JFrog CLI's encoding byte for byte,
//     because the CLI finds a build's document item by that path.
//   - ComparatorFor gives each package type its own order, so retention,
//     tag limits and latest-version resolution never borrow another
//     ecosystem's rules (decision 0018).
//   - The JSON tags on the trash record and the remote cache values are
//     the stored document format, and the string constants (remote kinds,
//     source kinds, key pair roles) are stored, so neither ever changes.
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//     (the item model).
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md
//     (cached items, the remote cache and source identities).
//   - Decision 0014: docs/research/decisions/0014-build-info.md (builds).
//   - Decision 0018: docs/research/decisions/0018-package-index.md
//     (packages, versions and their comparators).
//   - Decision 0020: docs/research/decisions/0020-domain-model.md (one
//     internal value per entity).
//   - Decision 0024: docs/research/decisions/0024-cold-storage-tier.md
//     (archived items).
//   - Decision 0036: docs/research/decisions/0036-maven.md (the Mercury
//     order).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md
//     ("Persistence").
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 187 and 188 (the artifacts module's core/model) and line 1058
//     (the shared types split into each module's core/model).
//   - Plan: docs/implementation/plan.md#p2-stores,
//     #p5-full-aql-searches-build-info, #p9-remote-repositories,
//     #p9b-newly-identified-artifactory-formats and
//     #p9d-modular-monolith-migration, epic STO
//     (https://github.com/weaveplatform/artifactweave/issues/939).
//   - https://github.com/weaveplatform/artifactweave/issues/1055
//
// # Status
//
// Implemented P2: items, properties, trash records, packages and the
// version comparators, archived items, the trash can settings and the key
// pair roles among the shared domain types (pull request 3). Later phases
// added folder download settings (pull request 62), builds (pull request
// 193), the Mercury order (pull request 233), the property limits (pull
// request 348), download matrices (pull request 355), source identities and
// the remote cache (pull requests 644 and 646), Git LFS locks (pull request
// 849) and Bower registrations (pull request 912). Staged in P9d.
//
// # Testing
//
//   - model_test.go: item paths, repository key aliasing, properties and
//     their limits, the trash record document and archived item expiry.
//   - version_test.go: each comparator's order, ComparatorFor and sorting,
//     and AssignOrderKeys.
//   - build_test.go: the build-info segment encoding, BuildInfoRepo, a
//     run's document item and its identity.
//   - matrix_test.go: matrix parameters against a file's properties.
//
// The metadata store's contract subtests store and read these values on its
// in-memory, SQLite and PostgreSQL stores.
package model
