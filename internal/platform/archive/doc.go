// Package archive reads zip, tar and tar.gz archives: the walker the item
// service explodes and serves archive entries with, and a bounded walker
// and reader the format handlers (decision 0032) read packages with, such
// as an npm tarball's package.json and file count, a NuGet package's
// .nuspec or a jar's POM. Every count an archive's headers give is the
// uploader's claim, so the bounded walker counts what it actually
// decompresses.
//
// # Role
//
// platform layer: archive reading that no module owns. In the artifacts
// module, the item service explodes deployed archives and serves archive
// entries with it, the folder archive download takes its kind, media type
// and extension from Kind, the format handlers read package archives with
// it, and the Helm, NuGet, PyPI and Terraform servers read stored and
// remote package content through it. The target layout also has security
// (P26) read archives here. It imports the standard library and the
// kernel, for the item path rule that Clean applies, and no module.
//
// # Inputs
//
//   - Archive content as an io.ReaderAt with its size, for Walk, Scan,
//     ReadFile and KindOf, or a tar stream the caller decompressed, as an
//     io.Reader, for ScanTar. ReaderAt gives random access over stored
//     content that can only seek.
//   - The archive's Kind: an archiveType parameter (ParseKind), or the
//     content's first SniffLen bytes and its name (Sniff, KindOf).
//   - An entry name as the archive spells it, for Clean.
//   - Limits, for Scan, ScanTar, ReadEntry and ReadFile: how many entries a
//     walk may pass, how many bytes a tar walk may read once decompressed
//     and how large the file read may be. DefaultLimits suit a package's
//     descriptor.
//
// # Outputs
//
//   - Kind, with ParseKind, Sniff and KindOf, and the media type and file
//     extension of each kind (ContentType, Ext).
//   - Entry: one entry as the archive lists it, with the size its header
//     claims, Open, which reads it, and Wrap, which reports an error from
//     opening or reading it as the walk would.
//   - Walk: every entry, in archive order, until the callback returns an
//     error.
//   - Scan and ScanTar: Walk within Limits. ReadEntry: one entry whole,
//     within Limits.File. ReadFile: the first regular file whose cleaned
//     name a predicate accepts, with that name.
//   - Clean: an entry name as a relative path, or a refusal.
//   - ReaderAt: an io.ReaderAt over an io.ReadSeeker, which reads at the
//     end of the content as bytes.Reader does.
//   - The sentinels ErrUnusable ("archive: unusable archive"), which every
//     refusal wraps, and ErrNotFound ("archive: no such entry"), which
//     ReadFile returns when no file matches. Callers test them with
//     errors.Is. A read of the content that fails is no refusal: Walk,
//     Scan, ScanTar, ReadFile, ReadEntry and Wrap return its error as the
//     reader gave it.
//
// # Invariants
//
//   - Every count an archive's headers give is the uploader's claim, an
//     entry's Size included, so the bounded walker counts what it
//     decompresses: Scan and ScanTar count the entries they pass and every
//     byte the tar stream yields, the bytes of skipped entries included,
//     and ReadEntry reads at most one byte past Limits.File. A zip walk
//     decompresses only the entries a caller opens, so Inflated bounds tar
//     walks alone.
//   - An entry that escapes its folder (an absolute path, a drive letter or
//     a ".." segment), or whose name is not a valid item path
//     (kernel.SplitItemPath), makes the archive unusable: Clean refuses it
//     with ErrUnusable, and an explode refuses the whole archive. ReadFile
//     passes over such an entry, which cannot be the file it looks for.
//   - A failed read is not an unusable archive. Each walk keeps the first
//     read of its content that failed, other than at the content's end,
//     and returns that read's error where a parser's would be. Every other
//     error a parser reports is ErrUnusable, whichever error it is, so a
//     corrupt archive is refused even when the parser names it with
//     io.EOF, io.ErrUnexpectedEOF or flate.CorruptInputError. A walk reads
//     no further than the size it is given, so an offset that a corrupt
//     archive gives past its end meets the end of the content, not a store
//     that might fail the read. ScanTar returns the errors of the stream it
//     is given as they are, since the caller, which decompresses that
//     stream, is the one that can tell them apart.
//   - A tar entry's reader is valid only until the walk moves on, so a
//     callback reads the entry before it returns.
//   - ReaderAt serialises reads, because they share one seek position. It
//     tells the end of the content by the size that one seek to the end
//     gives, never by a read's error, since io.ReadFull and S3's reader
//     both say io.ErrUnexpectedEOF: a read past the end returns the bytes
//     before it with io.EOF, a read that ends at the end returns nil, as
//     bytes.Reader's and *os.File's do, and content that ends before that
//     size is a failed read, the reader's own error or
//     io.ErrUnexpectedEOF, never io.EOF. So content smaller than SniffLen
//     reads the same from every blob store, those without ReadAt
//     included.
//   - Error texts reach clients: the item API answers an unusable archive
//     with a 400 whose message is the error's text, so the texts stay
//     byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (package format handlers; Inspect parses an archive entry before the
//     write transaction).
//   - Research 02: docs/research/02-artifactory-core-api-inventory.md (the
//     archive deploy, the archive entry download and the folder archive
//     download), and
//     https://docs.jfrog.com/artifactory/reference/deployartifactsfromarchive
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules": the platform imports no module.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 156 (platform/archive) and line 985 (the mapping row).
//   - Plan: docs/implementation/plan.md#p3-repositories-generic-artifacts-cli-aql-shape,
//     task 2 and tracking item 6, #p7-npm-go-maven-locals,
//     #p8-pypi-nuget-helm-terraform-locals and
//     #p9d-modular-monolith-migration, epic PLT
//     (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/47,
//     https://github.com/weaveplatform/artifactweave/issues/979,
//     https://github.com/weaveplatform/artifactweave/issues/1224 and
//     https://github.com/weaveplatform/artifactweave/issues/1239
//
// # Status
//
// Implemented P3: the walker, the archive kinds and their sniffer, the
// entry name check and the reader over seek-only content, for archive
// explode, archive entry downloads and folder archive downloads (issue
// 47). P7 added the bounded reader that the format handlers read
// packages with (Limits and ReadFile), and Scan and ReadEntry for npm's
// tarball inspection. P8 added ScanTar for PyPI's tar.bz2 source
// distributions. Moved here in P9d.
//
// # Testing
//
//   - TestUnitClean: names that clean to a relative path, backslashes
//     included, and the absolute, drive letter, ".." and invalid item path
//     names that Clean refuses.
//   - TestUnitSniffAndKinds: each signature and each name that tells a
//     kind, ParseKind, and each kind's media type and extension.
//   - TestUnitWalk: zip, tar and tar.gz walks list a directory, a file and
//     a link in archive order; a callback's error ends the walk; garbage
//     and a truncated tar are unusable.
//   - TestUnitWalkReadFailures and TestUnitReadEntryReadFailure: a read
//     that fails, before a walk lists the entries or after, returns its
//     own error, a store's io.ErrUnexpectedEOF included, and never
//     ErrUnusable.
//   - TestUnitWalkCorruptArchives and TestUnitReadEntryCorrupt: corrupt
//     content stays ErrUnusable whichever error the parser names it with,
//     an offset past the content's end never reaches the store, and a
//     refused entry is named in the refusal.
//   - TestUnitReaderAt: a reader that already reads at offsets is used as
//     it is, a seek-only reader reads at every offset as bytes.Reader
//     does, at and past the end of the content included, and content
//     whose end a seek cannot find fails to read.
//   - TestUnitReaderAtCutShort: content cut short before its size, whether
//     the store says io.ErrUnexpectedEOF or io.EOF, is a failed read from
//     ReadAt, and a walk wherever the cut either returns that failure,
//     never ErrUnusable, or has read every entry; it never takes the cut
//     for the end of the content.
//   - TestUnitReadFile and TestUnitReadFileInflationBudget: the first file
//     the predicate accepts, ErrNotFound, each limit, and a tar.gz whose
//     skipped entries inflate past the budget.
//   - TestUnitScan, TestUnitScanTar and TestUnitReadEntryFailures: the
//     limits of a scan over a sniffed or a caller-decompressed archive, the
//     errors of a caller's stream returned as they are, and an entry a
//     caller made that cannot be opened or read.
//
// The item service's explode and archive entry tests, and the format
// handlers' package inspection tests, exercise it too; the Helm, npm, NuGet
// and PyPI readers' tests read packages smaller than SniffLen through
// ReaderAt over a seek-only reader.
package archive
