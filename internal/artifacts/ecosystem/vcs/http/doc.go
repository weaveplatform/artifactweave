// Package vcshttp implements the remote-only Artifactory VCS archive
// protocol: the VCS partial strict server (VCSAPI), whose 17 operations
// list a project's tags, branches and references and serve its archives
// and selected files, and the route for GitLab subgroup owners
// (VCSNested), over the item kit. It builds the upstream address of a
// project's reference listing, archive or selected file (RefEndpoints,
// GitRefsURL, ArchiveURL, PeerFileURL) for GitHub, GitHub Enterprise,
// GitLab, Bitbucket, Bitbucket Server (STASH and OLDSTASH), a peer
// Artifactory VCS repository or a CUSTOM template; parses the references a
// provider lists (ParseRefs, RefEndpoint.Parse); checks an archive in full
// before the remote cache admits it (ValidateArchive,
// ValidateCommitArchive, ValidateFileZIP); and selects one file from an
// archive (FileZIP, WrappedFileZIP, CustomFileZIP).
//
// # Role
//
// artifacts module, ecosystem vcs, HTTP layer: the VCS slice's protocol
// handlers, with its archive, file, reference and peer rules; the slice is
// remote only. It is named vcshttp so that it does not shadow net/http. The
// slice has no format package, so the item service gives VCS repositories the
// generic hooks (decision 0032). Outside tests, only its own http subtree, the
// module's http adapters and app may import it (decision 0044, dependency rule
// 9).
//
// # Inputs
//
//   - Driving: the router, which embeds VCSAPI in the artifactory spec's
//     strict server and mounts VCSNested at GET /artifactory/api/vcs/*,
//     where the generated routes do not match a GitLab subgroup owner.
//     Within the package, vcs_routes.go lists a project's references
//     (ValidProject, RefEndpoints, then each endpoint's Variant and Parse,
//     within MaxRefsBytes and MaxRefs); vcs_download.go resolves a branch,
//     tag or release to its commit (ValidRef, ValidCommitID), fetches that
//     commit's archive (ArchiveURL, ValidateCommitArchive) and selects a
//     file from it within MaxArchiveBytes (ValidFilePath, FileZIP,
//     WrappedFileZIP, CustomFileZIP, ErrInvalidPath); vcs_nested.go routes
//     GitLab subgroup paths (ValidProject, ValidRef, ValidFilePath,
//     ValidExtension); and vcs_peer_file.go asks a peer for a selected file
//     (PeerFileURL, ValidateFileZIP, within MaxFileZIPBytes).
//   - Driven: the item kit (artifacts/adapters/http/itemkit), which
//     resolves the repository and the caller's access, reads a cached
//     archive's properties through the content store, opens cached blobs
//     and writes errors; and the kit's remote source service
//     (artifacts/core/remote/source), which fetches and caches reference
//     listings, archives and peer files. The address and archive rules
//     build from a repository's configuration (artifacts/api/repo) and read
//     only the bytes they are given.
//   - Settings: none. A repository's url and vcsGitProvider, and for CUSTOM
//     its vcsGitDownloadUrl template, select the provider and its address.
//
// # Outputs
//
//   - VCSAPI: the VCS partial strict server, whose operations answer the
//     reference listings as JFrog's JSON, and archives and selected files
//     (a ZIP that holds the file) as application/octet-stream.
//   - VCSNested: the handler of the GitLab subgroup path forms, which
//     answers a selected file as application/zip and a form it does not
//     own with 404.
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
//     change its authority or path. The partial also applies the remote's
//     destination and redirect policy: a reference listing, a peer's file
//     and a STASH, OLDSTASH or peer archive follow no redirect to another
//     URL, and any other archive only one on the configured origin under
//     the project's path, or to codeload.github.com for github.com, without
//     user information or a fragment.
//   - A properties filter only selects an archive that is already cached;
//     it never fetches one to create a match.
//   - The caller's read permission is checked on the project before the
//     remote source is read, and on an archive's or file's path before it
//     is fetched; a reference the caller may not read is left out of a
//     listing.
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
//     byte-identical, as do statuses and error and log text.
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
//     root files that join this package; vcs_peer_file.go joins them too)
//     and line 1024 (the mapping row).
//   - Plan: docs/implementation/plan.md#p33-automation-and-source-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/786,
//     https://github.com/weaveplatform/artifactweave/issues/1044 and
//     https://github.com/weaveplatform/artifactweave/issues/1085
//
// # Status
//
// Implemented P9c, which delivered the plan's P33 group (issues #782 to
// #788). Staged in P9d (ECO-20), which made the server root's VCS handlers
// the VCS partial strict server (WEB-08) and moved them here (WEB-16).
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
//   - vcs_routes_test.go calls the partial's code without the router, over
//     a kittest world's store and blob store (fixture_test.go, which keeps
//     the names of the root fixtures the tests used): the archive redirect
//     policy and its fallback for a malformed base, exact reference URLs,
//     the property filter's parsing and its match against a cached item's
//     properties, archive requests refused before the source, and a
//     result's revalidation, usage and missing cached blob.
//
// testdata holds unchanged git archive output, the legacy and refreshed
// archive, index and readme fixtures, with git-archives.json, which
// records each commit and archive digest, and generate.py, which builds
// them (testdata/README.md). The route suite test/server/artifacts/vcs and
// the acceptance steps also read it. The REST evidence is
// TestAccVCSRemoteRESTAndSubgroup in that route suite, which serves the
// operations and the subgroup route through the router, and the @vcs
// acceptance scenarios (test/acceptance/features/vcs.feature,
// vcs_legacy.feature and vcs_recipes.feature).
package vcshttp
