// Package cargo validates crates and supplies Cargo sparse-index records. A
// crate version is one gzip-compressed TAR archive, stored at
// crates/<name>/<name>-<version>.crate, whose Cargo.toml must name the
// crate and version its path does. Its index record, the line Cargo reads
// from the crate's sparse index file, is kept as a property of the
// archive's item, so the item tree remains the source of truth.
//
// # Role
//
// artifacts module, ecosystem layer: the crate validation and sparse index
// rules of the Cargo slice. Its Handler plugs into the item service through
// the format hooks (decision 0032), so a crate published with cargo publish
// and one written through the item routes meet the same checks; the slice's
// http package (artifacts/ecosystem/cargo/http) serves the sparse registry
// and web API over what it keeps.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for cargo repositories to Handler, and the work
//     queue, which runs every format's reindex through it (JFrog's
//     /api/cargo/{repoKey}/reindex schedules one); and the protocol, which
//     checks names and versions (ValidName, ValidVersion), builds and parses
//     paths (IndexPath, CratePath, ParseCratePath), checks published and
//     fetched crates (Inspect) and encodes the records it serves (Record,
//     Encode).
//   - Driven: the format hooks' environment (format.Env): its blobs hold the
//     archives, and its store holds the items and their properties. The
//     generic blackout and include and exclude checks
//     (artifacts/ecosystem/generic) run before the format's own.
//   - Settings: none.
//
// # Outputs
//
//   - Handler: the hooks that read an archive once before its transaction
//     (Inspect), admit only a valid crate at a canonical path (Admit), keep
//     each stored archive's index record (Deployed), and recheck every
//     stored crate and rewrite its record (Reindex).
//   - The cargo.index item property (RecordProperty): a version's index
//     record as one JSON line, with the dependencies and features a native
//     publish declared; an archive written through the item routes without
//     one gets a record with none. The cargo.yanked property
//     (YankedProperty) holds the protocol's yank flag, which Deployed folds
//     into the record.
//   - The crate and index rules: Inspect, ValidName, ValidVersion,
//     IndexPath, CratePath, ParseCratePath, Record, Dependency, Encode,
//     ErrInvalid and the size bounds (MaxCrate, MaxIndex).
//
// # Invariants
//
//   - A crate is read to its end within fixed bounds (64 MiB compressed,
//     512 MiB expanded, 10,000 entries and a 1 MiB Cargo.toml): every entry
//     is a unique regular file or folder under <name>-<version>/, the TAR
//     ends with its two zero blocks, only zeros follow them, and the gzip
//     checksum verifies.
//   - A record is kept and served only when it is valid (Record.IsValid)
//     and names the crate, version and SHA-256 of the archive it belongs
//     to.
//   - The property names are stored, so they stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories, whose upstream crates Inspect checks).
//   - docs/operations/cargo.md (connecting Cargo, the repository classes and
//     their behaviour).
//   - JFrog Cargo repositories:
//     https://docs.jfrog.com/artifactory/docs/cargo-repositories, and the
//     Cargo index format:
//     https://doc.rust-lang.org/cargo/reference/registry-index.html.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 232 (ecosystem/cargo), line 379 (the slice row) and line 997 (the
//     mapping row).
//   - Plan: docs/implementation/plan.md#p30-language-package-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/762 and
//     https://github.com/weaveplatform/artifactweave/issues/1047
//
// # Status
//
// Implemented P9c, which delivered the plan's P30 group (issues #762 to
// #768). Staged in P9d.
//
// # Testing
//
//   - cargo_test.go: the sparse index and crate paths, record bounds,
//     manifest validation, and unsafe or corrupt archives and tails.
//   - handler_test.go: a crate's lifecycle and reindex through the item
//     service, and the writes the hooks refuse.
package cargo
