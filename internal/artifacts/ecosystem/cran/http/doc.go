// Package cranhttp serves the CRAN protocol over the item kit: every
// class's PACKAGES and PACKAGES.gz, listing only the packages the caller
// has Read permission on, with a virtual repository's members merged in
// order; a virtual or remote repository's packages; the native source and
// binary uploads (POST /api/cran/{repoKey}/sources and /binaries); and the
// artifactory spec's legacy archive migration
// (GET /api/cran/{repoKey}/move-archives).
//
// # Role
//
// artifacts module, ecosystem cran, HTTP layer: the CRAN slice's protocol
// handlers, beside the format rules of artifacts/ecosystem/cran. It is named
// cranhttp so that it does not shadow net/http. Outside tests, only its own
// http subtree, the module's http adapters and app may import it (decision
// 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the router, which embeds CranAPI in the artifactory spec's
//     strict server, lists TryCranItem sixth among the item kit's overlays
//     (itemkit.Deps.Overlays), so that an item GET or HEAD in a CRAN
//     repository reaches it before the generic lookup, and mounts the
//     uploads with RegisterCranRoutes among the item routes.
//   - Driven: the item kit (artifacts/adapters/http/itemkit), whose store
//     (itemkit.Store) the overlay is handed and the handlers read
//     repositories and items from, moving archives in one of its
//     transactions, and which resolves the request, the caller's access and
//     an upload's target repository, reads items and blobs, waits for a
//     repository's index rebuild, serves stored files and remote streams,
//     deploys an upload and moves archives through the item service, and
//     writes errors; the kit's remote source service, which fetches a
//     remote's or a remote member's index and packages under the kit's CRAN
//     source request (itemkit.CranSourceRequest); the generic download
//     policy (artifacts/ecosystem/generic); pkgroute, for an upload's
//     declared checksums and the source errors; and the format package
//     (artifacts/ecosystem/cran), which decodes and encodes indexes, takes
//     an uploaded package's identity from its archive and finds the archives
//     still in the legacy layout.
//   - Settings: CranAPI.ArchiveMoverEnabled, which the router sets from
//     cran.archive_mover_enabled (off by default).
//
// # Outputs
//
//   - CranAPI: the CRAN partial strict server over the kit, whose
//     MoveCranArchives answers the archive migration.
//   - TryCranItem: the overlay that serves the indexes and a virtual or
//     remote repository's packages, and leaves every other request to the
//     overlays after it and the generic lookup.
//   - RegisterCranRoutes: mounts POST /artifactory/api/cran/{repoKey}/sources
//     and /binaries.
//
// # Invariants
//
//   - An index lists only the packages the caller has Read permission on,
//     and a remote's index checks each one against the caller's current
//     permission through the remote source (source.Result.CheckPermission)
//     before listing it. The download policy does not filter an index: a
//     package whose file the include and exclude patterns reject stays
//     listed and only its GET is refused, as in 7.x's Debian and RPM
//     indexes (RTFACT-30830, RTFACT-30656). A local or federated
//     repository's index is served once its rebuild is done, and every
//     index is read up to 32 MiB.
//   - An index, and a remote or virtual package, is read under the download
//     policy at its own path, after the Read permission check (DV-057): a
//     blacked-out repository, or patterns that reject the path, answer the
//     item route's 404 and message, and a virtual repository's own patterns
//     answer that no repository serves the request.
//   - A virtual repository merges its members' indexes in order: a missing
//     member, or one whose index the caller may not read or the member's
//     download policy refuses, is skipped; a member cycle, a chain of more
//     than eight virtual repositories or a member of another package type
//     is refused; and a package name belongs to the first member that lists
//     it. A virtual package's GET leaves out each row whose file a member's
//     or the virtual repository's download policy refuses before names are
//     claimed, so it passes over that member to the next one that lists the
//     package. It checks a member's policy at the package's own path, never
//     at the index it reads to find the package, so there only a
//     blacked-out member is skipped for its index.
//   - A virtual or remote repository serves a package only when the index
//     of the repository that holds it lists that path, and pins a remote's
//     download to the MD5 that index gives.
//   - An upload is spooled up to 512 MiB and read in full before the item
//     service commits it, under the file name its archive's name and
//     version give in the feed the URL names; it needs write on both.
//   - The archive migration answers 404 while ArchiveMoverEnabled is off,
//     needs an administrator and a local or federated CRAN repository, and
//     moves every legacy archive in one transaction or none: an existing
//     destination or a concurrent change answers 409.
//   - Statuses and error and log text stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (format routes come before the generic item lookup, in a fixed order,
//     and its CRAN archive migration section); decision 0033:
//     docs/research/decisions/0033-persistent-work-queue.md (an index read
//     waits for the queued rebuild).
//   - docs/testing/cran.md (connecting R, the upload forms and the archive
//     migration).
//   - JFrog CRAN repositories:
//     https://docs.jfrog.com/artifactory/docs/cran-repositories, and the
//     archive migration API:
//     https://docs.jfrog.com/artifactory/reference/movecranarchives.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 45 (the ecosystem http rule) and line 406 (the root's cran.go and
//     cran_upload.go, which cran_archive.go joins).
//   - Plan: docs/implementation/plan.md#p32-data-and-model-repositories
//     and #p9d-modular-monolith-migration, epic WEB
//     (https://github.com/weaveplatform/artifactweave/issues/944).
//   - https://github.com/weaveplatform/artifactweave/issues/778,
//     https://github.com/weaveplatform/artifactweave/issues/1084 and
//     https://github.com/weaveplatform/artifactweave/issues/1090
//
// # Status
//
// Implemented P9c, which delivered the plan's P32 group (issues #777 to
// #780 and #789), as the server root's cran.go, cran_upload.go and
// cran_archive.go. P9d made it the CRAN partial strict server with its own
// upload route registrar (WEB-08), and moved it here (WEB-15).
//
// # Testing
//
// The tests call the protocol's code without the router, over in-memory
// stores (fixture_test.go, which keeps the names of the root fixtures the
// tests used):
//
//   - cran_test.go: the kit's CRAN source request checks the published
//     index and archive, and a virtual repository refuses member cycles and
//     members of another type.
//   - cran_routes_test.go: the read authority behind each listed package,
//     the source request's compressed index and Windows binary, and an
//     upload without a store or an authenticated caller.
//   - cran_archive_test.go: an archive deleted after the listing is not
//     moved back, and a migration over more than a page of items is
//     cancelled, then completed through the archive migration operation.
//     Its requests run through the artifacts module's item routes and the
//     artifactory strict handler with the CRAN partial alone.
//
// The route suite test/server/artifacts/cran checks the protocol through
// the router, the download policy's answers and the packages it keeps
// listed among it, and the @cran acceptance partition
// (test/acceptance/features/cran.feature) is the native R evidence.
package cranhttp
