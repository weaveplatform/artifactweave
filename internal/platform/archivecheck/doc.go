// Package archivecheck validates bounded archive structure before
// native-client extraction.
//
// archive/zip reads a ZIP through its central directory, while a streaming
// extractor, such as the tar that installs a Vagrant box, reads the local
// records in the order they lie in the file. A crafted ZIP can show the two
// readers different names or entries, so the registry would index one
// archive and the client would extract another. ZIP admits an archive only
// when both readings agree, within limits that are checked before
// archive/zip allocates.
//
// # Role
//
// platform layer: ZIP admission for content that native clients extract.
// In the artifacts module, the CocoaPods format admits its pod source
// archives, uploaded or cached from a remote, and the Vagrant format its
// ZIP boxes through it before indexing them. The target layout does not
// name the package: the migration plan places it beside platform/archive,
// and decision 0044 records it among the packages the target layout does
// not name. It imports only the standard library.
//
// # Inputs
//
//   - A ZIP as an io.ReaderAt with its size.
//   - ZIPLimits: the largest archive, the largest central directory and the
//     most entries the caller accepts. Each must be set; the CocoaPods and
//     Vagrant formats pass their own.
//   - A context, which cancels the directory scan and the layout walk.
//
// # Outputs
//
//   - ZIP: the validated *zip.Reader. Its Deflate entries decompress through
//     a strict decompressor registered on that reader alone, so other
//     readers keep the standard library's.
//   - ErrInvalid ("invalid ZIP archive"), which every refusal of the layout
//     or the limits wraps. A failed read or parse returns its own error,
//     wrapped with what was being read.
//
// # Invariants
//
//   - The physical local-record layout must match the central directory:
//     each local header repeats its directory record's name, flags, method,
//     sizes and checksum, and the records run back to back from the start
//     of the archive to the directory. So the server and a client extractor
//     cannot see different file names or hidden entries.
//   - Directory limits are checked before the parser allocates: the footer
//     must end the archive, and the directory's size and record count,
//     ZIP64's included, are checked against ZIPLimits and counted record by
//     record before archive/zip reads the directory, because a footer can
//     understate what the directory holds.
//   - Reading a Deflate entry through the returned reader fails unless the
//     stream ends exactly where the entry's compressed data ends, so no
//     record can hide after it.
//   - Encryption, multi-disk archives and extra fields that could change an
//     entry's path or type for a native extractor are refused. Only ZIP64
//     sizes, timestamps, numeric owner IDs, NTFS times, comments and a
//     Unicode path that repeats the entry's name are accepted, in the local
//     and the central records alike.
//   - Error texts reach clients: the Vagrant format's 400 carries them, so
//     they stay byte-identical, including the read failures that name
//     Vagrant. The CocoaPods format answers with its own text.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md,
//     lines 181 to 185 (native archive admission).
//   - docs/testing/cocoapods.md and docs/testing/vagrant.md: the limits each
//     format passes, and the native clients that extract what it admits.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Package placement and names" (a package the target layout does not
//     name) and "Dependency rules" (the platform imports no module).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 156 (platform/archive, which this package sits beside).
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration, epic
//     PLT (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/771 (CocoaPods),
//     https://github.com/weaveplatform/artifactweave/issues/785 (Vagrant) and
//     https://github.com/weaveplatform/artifactweave/issues/979
//
// # Status
//
// Implemented P9c, for the CocoaPods source archives of issue 771 and the
// Vagrant boxes of issue 785. Staged in P9d.
//
// # Testing
//
//   - TestUnitZIPAdmissionLimits: each limit must be set, and an empty,
//     negative or oversized archive is refused.
//   - TestUnitZIPDirectoryLimitsBeforeParserAllocation,
//     TestUnitZIPRejectsUncountedDirectoryGap and
//     TestUnitZIPRejectsInconsistentDirectoryMetadata: oversized and
//     malformed directories, ZIP64 locators and records, trailing bytes
//     after the footer, and a footer that understates the directory's
//     records are refused before archive/zip parses the directory.
//   - TestUnitZIP64LargeOffsets: ZIP64 offsets above 4 GiB, on a sparse
//     file.
//   - TestUnitZIPDirectoryReadFailures and
//     TestUnitZIPLocalLayoutReadFailuresAndCancellation: read failures and
//     cancellation in the directory scan and the layout walk.
//   - TestUnitZIPLocalLayoutMatchesNativeExtraction,
//     TestUnitZIPRejectsUnindexedOrRepeatedStoredRecords,
//     TestUnitZIPRejectsOverlappingStoredEntries and
//     TestUnitZIPRejectsUnrepresentablePayloadSpan: local headers that
//     differ from the directory, and unindexed, repeated, overlapping and
//     unrepresentable records.
//   - TestUnitZIPDataDescriptorForms: each data descriptor form is
//     accepted, and a descriptor with a corrupt byte is refused.
//   - TestUnitZIPExtraFieldsCannotChangeNativePathsOrTypes and
//     TestUnitZIPRejectsCentralOnlyNativePathMetadata: extra fields that
//     could change a native path or type, in the local or the central
//     record.
//   - TestUnitZIPDirectoryMayReorderPhysicalEntries: a directory that lists
//     the records in another order is accepted.
//   - TestUnitStrictZIPDeflateConsumesOnlyExactStream and
//     TestUnitZIPCannotHideLocalRecordsInsideDeflateSpan: the strict
//     decompressor reads an exact stream and refuses trailing bytes and a
//     corrupt stream, and a record hidden after a Deflate stream inside its
//     entry's span is refused.
//
// The CocoaPods source archive tests and the Vagrant box tests exercise it
// too, with archives each format builds.
package archivecheck
