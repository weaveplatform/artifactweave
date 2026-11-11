// Package conda owns the Conda channel layout and the rules for its
// package archives and channel metadata. A package archive is stored at
// <subdir>/<name>-<version>-<build>.tar.bz2 or .conda, below an optional
// subchannel prefix, and is admitted only when its info/index.json agrees
// with that path. A hosted repository's repodata.json,
// current_repodata.json, their .zst forms and the opt-in CEP-16 shards are
// generated from the stored archives, never uploaded.
//
// # Role
//
// artifacts module, ecosystem layer: the Conda package, repodata and shard
// rules of the Conda slice. Its Handler plugs into the item service
// through the format hooks (decision 0032); the slice's http package
// (artifacts/ecosystem/conda/http) serves the native channel protocol over
// what it keeps.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the item
//     service's hooks for conda repositories to Handler, and the work queue,
//     which runs the channel metadata rebuild (Work) and every format's
//     reindex through it; the protocol, which parses paths and decodes and
//     encodes repodata and shards for each request; and the item routes of
//     artifacts/adapters/http, which keep stored shards off the generic item
//     API (ParseMetadataPath, IsShardFilename).
//   - Driven: the format hooks' environment (format.Env): its blobs, from
//     which archives are read; its store, through which Handler sets item
//     properties, lists a repository's files and keeps the package index
//     (artifacts/core/pkgindex); its work queue (Enqueue); and its derived
//     writes (Derive, Underive), which store the generated metadata. The
//     generic deploy policy (artifacts/ecosystem/generic) is applied first.
//   - Settings: Settings.ShardsEnabled, from the server's conda
//     shards_enabled setting (off by default), which turns on CEP-16 shard
//     generation.
//
// # Outputs
//
//   - Handler: the hooks that read and check an archive before the write
//     (Inspect, Admit), refuse an uploaded metadata file, keep stored
//     shards off copy and move (GuardTransferSource), keep the package
//     index and the conda.* item properties current and queue the channel
//     metadata rebuild (Deployed, Deleting, Deleted, Reindex), run that
//     rebuild and make a read wait for it (WorkKinds, Work, Owed), and
//     class each path (Classify).
//   - The package index: a package per name, with a version per version
//     whose files are its archives.
//   - The generated metadata of each channel and subdir: repodata.json,
//     current_repodata.json and their .zst forms, and, with shards on, the
//     shard index and a content-addressed shard per package name.
//   - The rules the protocol shares: PackagePath, MetadataPath,
//     ParsePackagePath, ParseMetadataPath, IndexPath, Record, ReadPackage,
//     Repodata and its decoders and encoders, and ShardIndex.
//
// # Invariants
//
//   - An archive's name, version, build and subdir come from its path and
//     must match its info/index.json, and the size and digests generated
//     metadata lists come from the stored bytes, never from the archive's
//     own fields.
//   - Every archive, repodata document and shard is read with bounded
//     sizes, entry counts and nesting before it is trusted.
//   - Generated metadata is rebuilt from the stored archives, so a
//     publish, delete, restore or reindex never leaves a stale record, and
//     a read waits for the owed rebuild (Owed).
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks; the protocol and storage decision); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/operations/conda.md (connecting a client, publishing, the
//     generated metadata and CEP-16 sharding).
//   - JFrog Conda repositories:
//     https://docs.jfrog.com/artifactory/docs/conda-repositories, and the
//     CEP-16 sharded repodata specification:
//     https://conda.org/learn/ceps/cep-0016/.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 233 (ecosystem/conda), line 379 (the slice row) and line 997 (the
//     mapping row).
//   - Plan: docs/implementation/plan.md#p32-data-and-model-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/777 and
//     https://github.com/weaveplatform/artifactweave/issues/1039
//
// # Status
//
// Implemented P9c, which delivered the plan's P32 group (issues #777 to
// #780 and #789). Moved here in P9d.
//
// # Testing
//
//   - conda_test.go and validation_test.go: paths, the archive envelope
//     and entries, info/index.json, the controlled and conda-forge fixtures
//     in testdata, and the repodata bounds and refusals.
//   - handler_test.go: inspection and admission, the lifecycle hooks,
//     reindex, the metadata worker and their failures.
//   - shards_test.go: shard round trips and identity, and the MessagePack
//     decoder's bounds.
package conda
