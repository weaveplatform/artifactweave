// Package conanhttp serves the Conan revision protocol at
// /artifactory/api/conan/{repoKey}, for Conan 2 and for Conan 1 with
// revisions enabled: ping, the credential exchange and check, recipe
// search, recipe and package revision lists and latest revisions, file
// lists, file download and upload, package search, and revision deletion.
// A revision becomes visible once its conanmanifest.txt is uploaded, which
// is accepted only when its MD5 entries match the uploaded files or the
// unpacked archive. Revision paths and the write policy are the format
// package's (artifacts/ecosystem/conan); this package answers HTTP with
// them.
//
// # Role
//
// artifacts module, ecosystem layer: the native Conan revision protocol of
// the Conan slice. It is named conanhttp so that it does not shadow
// net/http. Outside tests, only its own http subtree, the module's http
// adapters and app may import it (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type conan, local, federated,
//     virtual or remote. A repository with forceConanAuthentication
//     challenges an anonymous caller first, and the repository's reindex
//     ({repoKey}/reindex) is a JFrog operation beside the protocol, which
//     the protocol reserves.
//   - Driven: the router kit (pkgroute.Kit), whose store (pkgroute.Store)
//     the handlers also reach through DB(), and which resolves the
//     repository and the caller, checks permissions, reads, stores and
//     deletes items and writes errors; the token contract in Options
//     (access/api), which issues the token a password login is exchanged
//     for; the remote source service (Options.RemoteSources), which fetches
//     upstream metadata and files into the remote cache; and the format
//     package.
//
// # Outputs
//
//   - Protocol and Options: the routes, the source of each repository
//     class, the reserved reindex path, the authentication challenge and
//     the Artifactory error profile.
//   - Answers: the JSON documents Conan reads (revision lists, latest
//     revisions, file lists, search results and package search maps), the
//     files, the token or user name as plain text, 201 for an upload and
//     204 for a deletion.
//
// # Invariants
//
//   - A revision's files stay hidden until its manifest is stored, and a
//     package revision's until its recipe revision's manifest is stored
//     too. A manifest must list conanfile.py (a recipe) or conaninfo.txt
//     (a package), each entry must match an uploaded file or the unpacked
//     archive, and the archive may hold nothing the manifest does not
//     list.
//   - A revision file outside metadata/ is written once: different
//     content at the same path is refused with 409.
//   - A remote's metadata must be a complete, bounded JSON object before it
//     is cached, and every file it serves is pinned to the digest its
//     manifest gives; names and bytes reach a caller only through the
//     caller's permissions.
//   - A virtual repository reads its members in order, and uploads only to
//     its default deployment repository, which must be one of its members,
//     local or federated, and writable by the caller.
//   - A password login gets a revocable token that lasts at most an hour;
//     a token login gets its own token back.
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers); decision 0005:
//     docs/research/decisions/0005-authentication-and-tokens.md (the login
//     token's form and lifetime); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/testing/conan.md (connecting Conan 2 and Conan 1, the commit
//     marker, remote and virtual repositories, and the qualification).
//   - JFrog Conan repositories:
//     https://docs.jfrog.com/artifactory/docs/conan-repositories, and the
//     repository client support matrix:
//     https://docs.jfrog.com/artifactory/docs/repository-support-for-package-clients.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 233 (ecosystem/conan) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p31-native-build-and-plugin-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/770,
//     https://github.com/weaveplatform/artifactweave/issues/1038 and
//     https://github.com/weaveplatform/artifactweave/issues/1089
//
// # Status
//
// Implemented P9c, which delivered the plan's P31 group (issues #770 to
// #775). Staged in P9d.
//
// # Testing
//
//   - validation_test.go: the credential exchange's boundaries, manifest
//     parsing, permission errors, remote metadata checks and archive
//     safety.
//
// The routes are also tested end to end from test/server/artifacts/conan
// (conan_test.go, and conan_client_test.go with the pinned Conan 2 and
// Conan 1 clients, which make conan-accept runs) and from internal/app
// (conan_client_test.go and conan_remote_test.go).
package conanhttp
