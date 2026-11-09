// Package swifthttp serves the Swift Package Registry API on Artifactory's
// /artifactory/api/swift/{repo} prefix, on Swift local, federated, remote
// and virtual repositories: a package's release list, a release's
// metadata, its manifests and source archive, the identifier lookup, and
// publishing a release. Archive validation and the identity rules are the
// format package's (artifacts/ecosystem/swift); this package answers HTTP
// with them.
//
// # Role
//
// artifacts module, ecosystem layer: the native Swift Package Registry API
// of the Swift slice. It is named swifthttp so that it does not shadow
// net/http. Outside tests, only its own http subtree, the module's http
// adapters and app may import it (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type swift. JFrog's reindex
//     operation, /api/swift/{repoKey}/reindex, is served beside the
//     protocol, not by it.
//   - Driven: the router kit (pkgroute.Kit), which resolves the repository
//     and the caller, checks permissions, reads the package index, items,
//     their properties and blobs, stores a published archive through the
//     item service, where the format's hooks admit it, builds a virtual
//     repository's member requests, serves items and writes errors; the
//     remote source service, which fetches a remote's upstream release
//     lists, release metadata, manifests and archives into the remote
//     cache; and the format package, whose rules check every identity and
//     archive.
//
// # Outputs
//
//   - Protocol and Options: the routes, the source of each repository
//     class, the reserved reindex operation and the Swift registry error
//     profile (RFC 7807 problem JSON).
//   - Answers, with Content-Version 1: a release list whose URLs name this
//     repository; a release's metadata, with its source archive's SHA-256
//     and the metadata its publisher gave; a manifest, Package.swift or the
//     one for a swift-version, with Link headers to the alternates, or 303
//     to Package.swift when that version has none; the source archive; 404
//     for every identifier lookup, since no mapping is recorded; and 201
//     with the release's URL for a publish.
//
// # Invariants
//
//   - A path segment must be canonical, without escapes, semicolons,
//     backslashes or NUL (400), and only the identifier lookup and a
//     manifest's swift-version take a query (400).
//   - Only local and federated repositories publish, with PUT
//     /{scope}/{name}/{version} (405), and write permission on the
//     archive's path. The multipart form carries one source-archive part,
//     binary or base64, of at most 512 MiB, and an optional metadata JSON
//     object of at most 64 KiB; a signed release is refused (422) until
//     signatures are validated, an invalid archive is refused (422), and a
//     stored release is never replaced (409).
//   - Release lists show only the releases the caller may read; a stored
//     release whose archive is gone is not listed.
//   - A remote filters upstream release lists by the caller's permissions,
//     takes release metadata only with exactly one valid source-archive
//     resource, delivers an archive only when it validates and matches that
//     resource's SHA-256, and rechecks the caller's read when it delivers.
//   - A virtual repository's members are distinct local, federated or
//     remote repositories (400), read in order: release lists merge, the
//     first member that holds a version supplies it, and manifests are
//     served without the member's Link headers and with redirects onto the
//     virtual URL, so the client's next request meets the virtual
//     repository's permissions. A member that fails answers 502.
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/testing/swift.md (connecting SwiftPM, the native operations,
//     signed releases and the evidence), and docs/client-recipes/swift.md
//     (the client recipe, whose evidence is swift_test.go and
//     remote_test.go).
//   - JFrog Swift repositories:
//     https://docs.jfrog.com/artifactory/docs/swift-repositories, and the
//     Swift Package Registry specification:
//     https://github.com/swiftlang/swift-package-manager/blob/main/Documentation/PackageRegistry/Registry.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 234 (ecosystem/swift) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p30-language-package-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/766 and
//     https://github.com/weaveplatform/artifactweave/issues/1048
//
// # Status
//
// Implemented P9c, which delivered the plan's P30 group (issues #762 to
// #768). Staged in P9d.
//
// # Testing
//
//   - swift_test.go: native publish and read, malformed archives and
//     identities, the identifier lookup, and delete, restore and reindex;
//     and the SwiftPM client's anonymous and authenticated publishing
//     (TestAccSwiftPMAnonymousPublish, TestAccSwiftPMAuthenticatedPublish).
//   - remote_test.go: the remote's cache, offline reads and permissions,
//     absent or invalid checksums, a virtual repository's local-then-remote
//     order, and the SwiftPM client against remote and virtual repositories
//     offline (TestAccSwiftPMRemoteVirtualAndOffline).
//   - coverage_test.go: refused publish forms and writers, manifest
//     alternates and queries, bad remote representations, virtual member
//     refusals and failures, stale or unreadable releases, and read aliases
//     and authorization.
//   - cache_fence_test.go: input and cancellation bounds, and the cached
//     metadata's blob and permission fence.
//
// The TestAccSwiftPM tests skip unless ARTIFACTWEAVE_SWIFT_IMAGE names the
// SwiftPM client image or ARTIFACTWEAVE_SWIFT_CLIENT_LOCAL=1 is set with
// swift installed; the acceptance job's SwiftPM step runs them with the CI
// image. The @swift acceptance scenarios drive the package through the
// whole server.
package swifthttp
