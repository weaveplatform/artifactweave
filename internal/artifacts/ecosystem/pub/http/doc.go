// Package pubhttp serves the hosted Dart pub protocol through Artifactory's
// /artifactory/api/pub/{repo} URL, on pub local, federated, remote and
// virtual repositories: a package's versions, one version, an archive, and
// the three steps of dart pub publish. Archive validation and the package
// rules are the format package's (artifacts/ecosystem/pub); this package
// answers HTTP with them.
//
// # Role
//
// artifacts module, ecosystem layer: the native hosted pub protocol of the
// pub slice. It is named pubhttp so that it does not shadow net/http.
// Outside tests, only its own http subtree, the module's http adapters and
// app may import it (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type pub. JFrog's reindex
//     operation, /api/pub/{repoKey}/reindex, is served beside the protocol,
//     not by it.
//   - Driven: the router kit (pkgroute.Kit), whose store (pkgroute.Store)
//     the handlers also reach through DB(), and which resolves the
//     repository and the caller, checks permissions, reads the package
//     index and items, stores a published archive through the item service,
//     where the format's hooks admit it, builds a virtual repository's
//     member requests, serves items and writes errors; the blob store in
//     Options, which opens a remote's cached metadata; the remote source
//     service, which fetches a remote's upstream version documents and
//     archives into the remote cache; and the format package, whose rules
//     check every name, version, path and archive.
//
// # Outputs
//
//   - Protocol and Options: the routes, the source of each repository
//     class, the reserved reindex operation and the pub error profile.
//   - Answers: as application/vnd.pub.v2+json, a package's readable
//     versions, newest first, with the newest stable one as latest, and one
//     version, each with an archive URL that points back through this
//     repository, the archive's SHA-256 when known and its pubspec, and the
//     upload URL that starts a publish and the finalize answer; 204 with the
//     finalize URL once an upload is stored; and an archive's bytes.
//
// # Invariants
//
//   - Only local and federated repositories publish (405), with write
//     permission on the repository and the archive's path. An upload over
//     64 MiB is too large (413); any other, an empty one included, must be
//     a valid archive (400). A published version is never replaced (409),
//     and finalizing checks that the stored archive is still the one
//     uploaded (409).
//   - Version lists show only versions the caller may read; a package with
//     none answers 404. A hosted index entry that disagrees with its
//     archive answers 502.
//   - A virtual repository reads its members in order, and the first member
//     that holds a version supplies it.
//   - A remote reads an upstream version document only when it names the
//     package, holds 1 to 4,096 distinct valid versions whose pubspecs name
//     them, and gives archive URLs over http or https without credentials or
//     fragment. Its versions are filtered by the caller's permissions and
//     the source's, an archive is delivered only when its read is checked
//     again, it matches its declared SHA-256, if any, and it holds the
//     version asked for, and the metadata is revalidated before and during
//     delivery.
//     Archive links under the remote's own URL use its ordinary transport;
//     other links use the source's external URL policy.
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/testing/pub.md (connecting Dart, publishing, virtual and remote
//     repositories, and the evidence).
//   - JFrog pub repositories:
//     https://docs.jfrog.com/artifactory/docs/pub-repositories.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 234 (ecosystem/pub) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p30-language-package-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/765,
//     https://github.com/weaveplatform/artifactweave/issues/1048 and
//     https://github.com/weaveplatform/artifactweave/issues/1089
//
// # Status
//
// Implemented P9c, which delivered the plan's P30 group (issues #762 to
// #768). Moved here in P9d.
//
// # Testing
//
//   - pub_test.go: the remote's metadata authority, denied versions, broken
//     sources and stale authority, and its archive rechecks, mismatches and
//     revalidation; publishing and resolving on local and federated
//     repositories, rejected uploads, an empty one included, anonymous
//     publishing, identity and read rights on the version and archive
//     routes, malformed or changed finalize requests, read-only classes and
//     oversized bodies, and the archive size limit at its boundary.
//   - remote_test.go: untrusted upstream version documents and the
//     same-origin archive link rule.
//
// The @pub acceptance scenarios, with the native Dart client, drive it
// through the whole server.
package pubhttp
