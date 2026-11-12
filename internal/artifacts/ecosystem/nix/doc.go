// Package nix validates Nix binary-cache metadata and archive integrity:
// narinfo files (Parse, Info) and the NAR archives they name, compressed or
// not (ArchivePath, ReadArchive, Archive). It signs and verifies a narinfo
// with Ed25519 over Nix's fingerprint of its store path, and its Handler
// keeps each store path's narinfo and archive consistent in a repository,
// where a hosted repository stores both in the store path's folder,
// binary-cache/<store hash>/, as Artifactory 7.x does (InfoItem,
// ArchiveItem).
// It also names and checks a channel's snapshots: the path of each
// snapshot's nixexprs.tar.xz (Snapshot, ParseSnapshotDir,
// ParseSnapshotPath) and the tarball itself (ReadChannel).
//
// # Role
//
// artifacts module, ecosystem layer: the Nix narinfo metadata and archive
// rules of the Nix slice. Its Handler plugs into the item service through
// the format hooks (decision 0032); the slice's http package
// (artifacts/ecosystem/nix/http) serves the binary-cache protocol and
// channels over what it keeps.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for nix repositories to Handler, and the work
//     queue, which runs every format's reindex through it; and the
//     protocol, which parses, signs and verifies narinfo, checks archives,
//     and reads a stored narinfo and its archive (ReadInfo,
//     ReferencedArchive).
//   - Driven: the format hooks' environment (format.Env): its blob store,
//     from which every narinfo and archive is read again and checked; its
//     store, in which Handler finds a narinfo's archive and keeps the
//     package index (artifacts/core/pkgindex); and its lease store, which
//     serialises competing first publications of a store path. And the
//     generic deploy policy (artifacts/ecosystem/generic), which Handler
//     embeds and applies first.
//   - Settings: none.
//
// # Outputs
//
//   - Handler: the hooks that admit nix-cache-info only with the content
//     the cache serves, a narinfo only where the repository's layout
//     stores it and when it parses (and, when a client deploys it, its
//     archive is already stored), and an archive only where the layout
//     stores it, or in a hosted repository at nar/<file> where the client
//     uploads it, and when it decompresses to a valid NAR and hashes to
//     its path (Inspect, Admit, GuardWrite); keep a store path's signed
//     identity and an archive's bytes immutable; index a store path while
//     its narinfo and archive are both stored and agree, and release the
//     upload held at nar/<file> once its narinfo is published (Deployed,
//     Deleted, Reindex);
//     refuse a generic download from a remote and check a narinfo's
//     archive before a generic download serves the narinfo (Resolve); and
//     class each path (Classify). A channel snapshot's tarball is admitted
//     only when ReadChannel passes it, and is not indexed.
//   - The package index: a package per store path name, with a version
//     per store hash whose metadata is its narinfo's Info as JSON and whose
//     files are the narinfo and its archive.
//   - The rules the protocol shares: Info, Parse, IsInfoPath, ArchivePath,
//     the layout (BinaryCache, InfoItem, ArchiveItem, IsInfoItem,
//     IsArchiveItem), Nix32, Archive, ReadArchive, ReadInfo,
//     ReferencedArchive, CacheInfo, ErrInvalid and the size limits; and
//     for channels Snapshot,
//     ValidChannel, ValidFlavor, ParseSnapshotDir, ParseSnapshotPath,
//     IsChannelPath, ReadChannel, ChannelFile and MaxChannelBytes.
//
// # Invariants
//
//   - A narinfo is checked whole, and its CA field is dropped: no
//     signature covers it, and a client could use it to skip signature
//     trust.
//   - An archive is admitted only when it decompresses to exactly one
//     well-formed NAR within the limits and its SHA-256 is the one its path
//     names; decoders are bounded to 64 MiB of memory. An xz file is read
//     whole, as liblzma reads it: every stream must end in an index that
//     lists the blocks read and a footer that matches it (xz.go).
//   - A hosted repository stores a store path's narinfo and archive only
//     in its folder under binary-cache, and an archive at nar/<file> only
//     until the narinfo that names it is published; a remote's cache keeps
//     the client's paths.
//   - A published store path keeps its signed fingerprint (store path, NAR
//     hash and size, and references), and an archive never changes.
//   - The index lists a store path only while its narinfo and archive are
//     both stored and agree.
//   - A channel snapshot is stored only as
//     channels/<channel>/<nixpkgs|nixos>/<snapshot>/nixexprs.tar.xz, and
//     only as an xz stream of one tar archive with an entry, nothing after
//     it but zero padding.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks; the protocol and storage decision); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/testing/nix.md (connecting Nix, signatures, the limits, remote
//     and virtual caches, the lifecycle and the evidence).
//   - JFrog Nix repositories:
//     https://docs.jfrog.com/artifactory/docs/nix-repositories, and the Nix
//     narinfo format:
//     https://nix.dev/manual/nix/2.35/protocols/binary-cache/narinfo.html.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 236 (ecosystem/nix), line 385 (the slice row) and line 997 (the
//     mapping row).
//   - Plan: docs/implementation/plan.md#p33-automation-and-source-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - The binary-cache folder: the JFrog CLI's jf nix,
//     jfrog-cli-artifactory artifactory/commands/nix/command.go
//     (binaryCacheDirPrefix, lines 53-55, and its AQL, lines 526-580).
//   - The .xz file format 1.2.1: https://tukaani.org/xz/xz-file-format.txt.
//   - https://github.com/weaveplatform/artifactweave/issues/788,
//     https://github.com/weaveplatform/artifactweave/issues/1037,
//     https://github.com/weaveplatform/artifactweave/issues/1263,
//     https://github.com/weaveplatform/artifactweave/issues/1296 and
//     https://github.com/weaveplatform/artifactweave/issues/1297
//
// # Status
//
// Implemented P9c, which delivered the plan's P33 group (issues #782 to
// #788). Moved here in P9d. Channels added by #1263; the binary-cache
// layout by #1296 and the whole xz stream by #1297.
//
// # Testing
//
//   - metadata_test.go: narinfo round trips, signing and verification,
//     hashes and paths, optional fields and bounds, and ambiguous or unsafe
//     metadata.
//   - archive_test.go: the native client's cache fixture
//     (testdata/README.md records how it was made), each compression, and
//     malformed, unsafe or oversized NARs.
//   - handler_test.go: admission, the generic read fences, storage
//     failures and the publication lock's failures.
//   - layout_test.go: the binary-cache layout's paths, inspection by
//     layout, indexing a store path's folder, releasing the held upload,
//     and a remote cache's paths.
//   - xz_test.go: whole xz files of each check type, several blocks and
//     streams, and every part of the container broken or cut short.
//   - contract_test.go: the format's lifecycle through the item service,
//     and concurrent first publications of one store path.
//   - channel_test.go: snapshot paths, channel tarballs and their faults,
//     and a tarball's admission without indexing.
package nix
