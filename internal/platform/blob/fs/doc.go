// Package fs is the local-disk blob store. Blobs live at
// <root>/sha256/<ab>/<cd>/<hex>; the two shard levels keep directories small
// enough for tools and filesystems that slow down on huge listings. Upload
// sessions live at <root>/_uploads/<id>/ on the same filesystem, so committing
// is an fsync and a rename rather than a copy.
//
// # Role
//
// platform layer, adapter: the local-disk implementation of platform/blob's
// Store, and the default blob backend. The composition root opens it for a
// blob tier or a mirror member whose backend is fs, and tests and the format
// contract suite use it as a real store on a temporary directory. It
// imports platform/blob, the standard library and, on Windows,
// golang.org/x/sys/windows.
//
// # Inputs
//
//   - The root directory, which New creates with its sha256 and _uploads
//     directories: blob.fs.root (by default <data_dir>/blobs) for the hot
//     tier, and a root of their own for the cold tier and for each fs
//     member of a mirror.
//   - What platform/blob's Store and Upload take: upload options, the bytes
//     appended, declared digests, upload IDs and the digests of stored
//     blobs.
//
// # Outputs
//
//   - Store, from New: a blob.Store. It implements neither blob.Signer nor
//     blob.PartSizer, because a local disk has no URL to sign and appends
//     without parts.
//
// # Invariants
//
//   - The layout above is persisted state. A store opened on an existing
//     root finds its blobs only at <root>/sha256/<ab>/<cd>/<hex>, so a
//     changed layout would start cleanly and then answer ErrNotFound for
//     every stored blob.
//   - A session directory holds the data file and the state file, which
//     records as JSON the size, the saved hasher state and the declared
//     SHA-256. The state is written to state.tmp and renamed over state. When
//     the state does not describe the data (a crash between the two writes,
//     or a damaged state file), the data is authoritative: resume rehashes it
//     rather than lose the session.
//   - Commit fsyncs the data, renames it into its shard and fsyncs the shard
//     directory; Windows cannot fsync a directory, and its rename is durable
//     when it returns. Content that is already stored is not written again.
//   - Windows behaviour lives in the _windows.go files. publish moves the
//     data with MoveFileEx and without REPLACE_EXISTING, because replacing a
//     blob can deny a concurrent reader access to it; a commit that loses
//     the race to another session's identical blob keeps the winner. A read
//     retries the brief sharing violation of a concurrent publication for up
//     to a second, and fails at once on any other error.
//   - Errors fs creates start "blob/fs:" or wrap a platform/blob sentinel.
//     Errors from platform/blob pass through unwrapped, as their texts
//     already start "blob:". Every text stays byte-identical.
//
// # References
//
//   - Decision 0004: docs/research/decisions/0004-storage-sql-metadata-and-blobs.md
//     ("Blobs": the fs backend, temp-then-rename into a two-level hex tree).
//   - docs/operations/configuration-reference.md (blob.fs.root).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules": the platform imports no module.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 147 (platform/blob/fs) and line 1049 (the mapping row).
//   - Plan: docs/implementation/plan.md#p2-stores, task 3, and
//     #p9d-modular-monolith-migration, epic PLT
//     (https://github.com/weaveplatform/artifactweave/issues/938).
//   - https://github.com/weaveplatform/artifactweave/issues/980
//
// # Status
//
// Implemented P2. P8 made a commit on Windows keep a blob that another
// session had already published rather than replace it under a reader (pull
// request 601), and P9c made reads on Windows retry the sharing violation of
// a concurrent publication (pull request 812). Moved here in P9d.
//
// # Testing
//
//   - TestAccContract: the blobtest contract on a temporary directory.
//   - TestAccConcurrentCommitAndRead: a committed blob is readable at once,
//     while other sessions are still publishing the same content.
//   - TestAccResumeRepairsState: a state that records fewer bytes than the
//     data file holds, or a damaged state file, is repaired by rehashing.
//   - TestAccNewFailures, TestAccBeginFailures, TestAccResumeFailures,
//     TestAccAppendFailures, TestAccCommitFailures, TestAccDeleteFailure and
//     TestAccMoreFailures: each operating system call failing, injected
//     through the package's variables, and the error it returns.
//   - TestAccNewRejectsUnusableRoot, TestAccStatAndOpenIOErrors,
//     TestAccUnreadableFiles and TestAccSyncDirectory: a root that is a file,
//     a directory where a blob should be, unreadable files (skipped on
//     Windows and for root) and directory sync.
//   - On Windows only, in the Windows unit job:
//     TestUnitOpenBlobRetriesTransientSharingViolation and
//     TestUnitOpenBlobDoesNotRetryOtherErrors for reads, and
//     TestUnitPublishPreservesExistingBlob,
//     TestUnitPublishRelativeAndLongPaths and TestUnitPublicationPaths for
//     publication.
package fs
