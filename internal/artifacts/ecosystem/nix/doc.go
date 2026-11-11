// Package nix validates Nix binary-cache metadata and archive integrity:
// narinfo files (Parse, Info) and the NAR archives they name, compressed or
// not (ArchivePath, ReadArchive, Archive). It signs and verifies a narinfo
// with Ed25519 over Nix's fingerprint of its store path, and its Handler
// keeps each store path's narinfo and archive consistent in a repository.
//
// # Role
//
// artifacts module, ecosystem layer: the Nix narinfo metadata and archive
// rules of the Nix slice. Its Handler plugs into the item service through
// the format hooks (decision 0032); the slice's http package
// (artifacts/ecosystem/nix/http) serves the binary-cache protocol over what
// it keeps.
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
//     the cache serves, a narinfo only when it parses (and, when a client
//     deploys it, its archive is already stored), and an archive only when
//     it decompresses to a valid NAR and hashes to its path (Inspect,
//     Admit, GuardWrite); keep a store path's signed identity and an
//     archive's bytes immutable; index a store path while its narinfo and
//     archive are both stored and agree (Deployed, Deleted, Reindex);
//     refuse a generic download from a remote and check a narinfo's
//     archive before a generic download serves the narinfo (Resolve); and
//     class each path (Classify).
//   - The package index: a package per store path name, with a version
//     per store hash whose metadata is its narinfo's Info as JSON and whose
//     files are the narinfo and its archive.
//   - The rules the protocol shares: Info, Parse, IsInfoPath, ArchivePath,
//     Nix32, Archive, ReadArchive, ReadInfo, ReferencedArchive, CacheInfo,
//     ErrInvalid and the size limits.
//
// # Invariants
//
//   - A narinfo is checked whole, and its CA field is dropped: no
//     signature covers it, and a client could use it to skip signature
//     trust.
//   - An archive is admitted only when it decompresses to exactly one
//     well-formed NAR within the limits and its SHA-256 is the one its path
//     names; decoders are bounded to 64 MiB of memory.
//   - A published store path keeps its signed fingerprint (store path, NAR
//     hash and size, and references), and an archive never changes.
//   - The index lists a store path only while its narinfo and archive are
//     both stored and agree.
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
//   - https://github.com/weaveplatform/artifactweave/issues/788 and
//     https://github.com/weaveplatform/artifactweave/issues/1037
//
// # Status
//
// Implemented P9c, which delivered the plan's P33 group (issues #782 to
// #788). Moved here in P9d.
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
//   - contract_test.go: the format's lifecycle through the item service,
//     and concurrent first publications of one store path.
package nix
