// Package vcshttp implements the remote-only Artifactory VCS archive
// protocol. It builds the upstream address of a project's reference
// listing, archive or selected file (RefEndpoints, GitRefsURL, ArchiveURL,
// PeerFileURL) for GitHub, GitHub Enterprise, GitLab, Bitbucket, Bitbucket
// Server (STASH and OLDSTASH), a peer Artifactory VCS repository or a
// CUSTOM template; parses the references a provider lists (ParseRefs,
// RefEndpoint.Parse); checks an archive in full before the remote cache
// admits it (ValidateArchive, ValidateCommitArchive, ValidateFileZIP); and
// selects one file from an archive (FileZIP, WrappedFileZIP,
// CustomFileZIP).
//
// # Role
//
// artifacts module, ecosystem layer: the VCS archive, file, reference and
// peer protocol rules of the VCS slice, which is remote only. It is named
// vcshttp so that it does not shadow net/http. The slice has no format
// package, so the item service gives VCS repositories the generic hooks
// (decision 0032). The routes under /artifactory/api/vcs are still the
// root files migration/artifacts/ecosystem/vcs/http/vcs_routes.go, vcs_download.go, vcs_nested.go
// and vcs_peer_file.go, which serve them with this package. Outside tests,
// only its own http subtree, the module's http adapters and app may import
// it (decision 0044, dependency rules); the root files do until they move.
//
// # Inputs
//
//   - Driving: the server's root files: vcs_routes.go, which lists a
//     project's tags and branches (ValidProject, RefEndpoints, then each
//     endpoint's Variant and Parse, within MaxRefsBytes and MaxRefs);
//     vcs_download.go, which resolves a branch, tag or release to its
//     commit (ValidRef, ValidCommitID), fetches that commit's archive
//     (ArchiveURL, ValidateCommitArchive) and selects a file from it within
//     MaxArchiveBytes (ValidFilePath, FileZIP, WrappedFileZIP,
//     CustomFileZIP, ErrInvalidPath); vcs_nested.go, which routes GitLab
//     subgroup paths (ValidProject, ValidRef, ValidFilePath,
//     ValidExtension); and vcs_peer_file.go, which asks a peer for a
//     selected file (PeerFileURL, ValidateFileZIP, within MaxFileZIPBytes).
//   - Driven: none. It builds addresses from a repository's configuration
//     (artifacts/api/repo) and reads only the bytes it is given; the root
//     files fetch through the remote source service.
//   - Settings: none. A repository's url and vcsGitProvider, and for CUSTOM
//     its vcsGitDownloadUrl template, select the provider and its address.
//
// # Outputs
//
//   - The name rules: ValidProject, ValidRef, ValidCommitID, ValidFilePath
//     and ValidExtension.
//   - The provider addresses: RefEndpoints, GitRefsURL, ArchiveURL and
//     PeerFileURL, which fail with ErrInvalidPath.
//   - The reference rules: Ref, RefEndpoint with Variant and Parse, and
//     ParseRefs, which fail with ErrInvalidRefs; MaxRefsBytes and MaxRefs.
//   - The archive rules: ValidateArchive, ValidateCommitArchive,
//     ValidateFileZIP, FileZIP, WrappedFileZIP and CustomFileZIP, which fail
//     with ErrInvalidArchive, or ErrInvalidPath for a file the archive does
//     not hold; MaxArchiveBytes, MaxExpandedBytes, MaxArchiveFiles,
//     MaxSelectedFileBytes and MaxFileZIPBytes.
//
// # Invariants
//
//   - Every provider address starts from the configured remote URL, which
//     must be http or https with a host and no user information, query,
//     fragment or encoded path, and no project, reference or file name may
//     change its authority or path. The root files also apply the remote's
//     destination and redirect policy.
//   - An archive is read in full before it is admitted, a tar.gz to the end
//     of its gzip stream and every ZIP member to its declared size, within
//     256 MiB, 1 GiB expanded and 100,000 entries; every entry path is
//     relative and clean, and a tar holds only directories, regular files
//     and git archive's single global commit record.
//   - A commit marker in an archive (git archive's PAX comment, or a ZIP
//     comment that is a commit ID) must name the commit that was asked for.
//   - A reference listing is at most 8 MiB and 65,536 references and names
//     each reference once; an annotated tag resolves to the commit its
//     peeled entry names.
//   - A peer's selected file follows a reference that can move, so it is
//     never treated as an immutable commit archive.
//   - The Variant values are part of the remote cache key, so they stay
//     byte-identical.
//
// # References
//
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md
//     (VCS is remote only, and its reads cross the shared source boundary
//     and cache); decision 0032:
//     docs/research/decisions/0032-package-format-handlers.md (the format
//     hooks, which VCS repositories take from generic).
//   - docs/testing/vcs.md (the REST contract, the providers, GitLab
//     subgroups, the peer and the acceptance evidence),
//     docs/research/39-vcs-legacy.md (the STASH, OLDSTASH and peer
//     contracts) and docs/client-recipes/vcs.md (the client recipe).
//   - JFrog VCS repositories:
//     https://docs.jfrog.com/artifactory/docs/vcs-repositories.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 234 (ecosystem/vcs), line 379 (the slice row), line 409 (the
//     root files that join this package) and line 1024 (the mapping row).
//   - Plan: docs/implementation/plan.md#p33-automation-and-source-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/786 and
//     https://github.com/weaveplatform/artifactweave/issues/1044
//
// # Status
//
// Implemented P9c, which delivered the plan's P33 group (issues #782 to
// #788). Staged in P9d.
//
// # Testing
//
//   - archive_test.go: provider tar and ZIP archives, traversal and
//     corruption, exact and rootless file selection, name validation,
//     provider addresses kept on the configured origin, invalid
//     configuration, unsafe ZIP entries, and missing, duplicate and corrupt
//     selections.
//   - archive_git_test.go: real git archive output bound to its commit with
//     rootless paths, global PAX records, a peer's selected-file ZIP, and
//     CUSTOM exact and wrapped layouts; the VCS client recipe cites it.
//   - paths_test.go: the STASH and OLDSTASH addresses.
//   - peer_test.go: the peer's endpoints and payloads, malformed or
//     ambiguous references, and the selected-file address.
//   - refs_test.go: ParseRefs with branches and peeled tags, and malformed
//     or ambiguous advertisements.
//
// testdata holds unchanged git archive output, the legacy and refreshed
// archive, index and readme fixtures, with git-archives.json, which
// records each commit and archive digest, and generate.py, which builds
// them (testdata/README.md). The root internal/server VCS suites and the
// acceptance steps also read it. The REST evidence is
// TestAccVCSRemoteRESTAndSubgroup in the internal/server root and the @vcs
// acceptance scenarios (test/acceptance/features/vcs.feature,
// vcs_legacy.feature and vcs_recipes.feature).
package vcshttp
