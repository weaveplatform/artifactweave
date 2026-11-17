// Package rpmhttp serves the RPM protocol of virtual repositories over the
// item kit: the repodata a virtual RPM repository composes from its
// members, signed with its key pair when it names one, and the packages
// that repodata lists. It also answers the artifactory spec's Yum
// calculation (POST /api/yum/{repoKey}), which rebuilds a local or
// federated repository's repodata and builds a virtual repository's again.
//
// # Role
//
// artifacts module, ecosystem rpm, HTTP layer: the RPM slice's protocol
// handlers, beside the format rules of artifacts/ecosystem/rpm. It is named
// rpmhttp so that it does not shadow net/http. Outside tests, only its own
// http subtree, the module's http adapters and app may import it (decision
// 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the router, which embeds RPMAPI in the artifactory spec's
//     strict server and lists TryRPMItem eighth among the item kit's
//     overlays (itemkit.Deps.Overlays), so that an item GET or HEAD in a
//     virtual RPM repository reaches it before the generic lookup.
//   - Driven: the item kit (artifacts/adapters/http/itemkit), whose store
//     (itemkit.Store) the overlay is handed and reads repositories and items
//     from, and which resolves the request and the caller's access, reads
//     items and blobs, serves stored files and remote streams, runs the
//     reindex and writes errors; the kit's remote source service, which
//     fetches a remote member's repodata and packages under the kit's RPM
//     source request (itemkit.RPMSourceRequest); access/api's Keys, which
//     reads a virtual repository's GPG key pair; the generic download policy
//     (artifacts/ecosystem/generic); the router kit the item kit builds
//     for the overlay (itemkit.Kit.Packages, pkgroute.ItemKit), whose walk,
//     Merged and server-wide merged metadata cache the virtual repodata is
//     built through; and the format package (artifacts/ecosystem/rpm),
//     which decodes member repodata, merges it (rpm.Merger) and encodes and
//     signs the virtual view.
//
// # Outputs
//
//   - RPMAPI: the RPM partial strict server over the kit, whose
//     CalculateYumRepositoryMetadata answers the Yum calculation.
//   - TryRPMItem: the overlay that serves a virtual RPM repository's
//     repodata files and .rpm packages, and leaves every other request to
//     the overlays after it and the generic lookup.
//
// # Invariants
//
//   - A virtual repository's repodata lists only the packages the caller
//     has Read permission on in the member that holds them, and a package
//     path belongs to the first member that lists it. A caller reads the
//     repodata or a package through a virtual repository when they may
//     read it there or in a member the walk reaches
//     (itemkit.Access.VirtualReads; decision 0008, R3), and one refused is
//     answered 404 "Resource not found" while the virtual repository hides
//     unauthorized resources (R4). The download policy does not filter it: a
//     package whose file a member's or the virtual repository's patterns
//     reject stays listed, as in 7.x's virtual primary.xml.gz
//     (RTFACT-30656), and only its GET is refused.
//   - The virtual repodata walks its members as the package handlers do
//     (pkgroute.Collect; decision 0008): nested virtual repositories are
//     flattened in 7.x's order with a cycle skipped, and a missing member,
//     one of another package type, one whose repodata is unpublished,
//     unreadable to the caller or refused by its download policy (blacked
//     out, or patterns that reject its repomd.xml or primary file), and a
//     remote whose upstream misses or fails are passed over, a failure with
//     a warning [I]. A package GET checks a member's policy at the
//     package's own path, never at the repodata it reads to find the
//     package.
//   - The merged repodata is kept as one set, so repomd.xml always names
//     the primary served beside it, for the virtual repository's
//     virtualRetrievalCachePeriodSecs, one entry per set of members the
//     caller can read (pkgroute.Kit.Merged, R8); a rebuild that finds no
//     member's repodata drops it and answers empty repodata [I]. The Yum
//     calculation on a virtual repository drops the set and builds it
//     again, so the next read serves the new one [I].
//   - The repodata and package reads check Read permission first and then
//     the virtual repository's own patterns at the requested path, which
//     answer 404 "No repository found to serve the request for
//     <key>:<path>" (DV-057). A package GET passes over a member whose
//     download policy refuses the file to the next member that lists it.
//   - A package is served only when a member's repodata the caller can see
//     lists it, and only with the SHA-256 that member's repodata gives: a
//     local member's stored file must match it, and a remote member's
//     download is pinned to it.
//   - Member repodata is read up to 36 MiB, and a remote's is validated
//     before it is used.
//   - The repodata and package reads take a query and matrix parameters as
//     any other file's GET does, once read permission and the download
//     policy are settled (#1350): a query, such as skipUpdateStats
//     (RTFACT-25636), changes nothing, and matrix parameters are matched
//     against the file's properties (JFrog's guide, Using Properties in
//     Deployment and Resolution). The composed repodata carries none [I],
//     so only a mandatory key refuses it, and a package GET passes over a
//     member whose file does not match to the next member that lists it
//     [I]. A remote member's package matches the copy its cache holds
//     before the upstream is asked, and passes the query, under Propagate
//     Query Params, and the matrix parameters to the upstream (#1371).
//   - The composed repodata is stamped with the Unix epoch, not the time of
//     the request, so an unchanged view composes the same bytes each time.
//     Its signature, by a GPG pair only, covers repomd.xml, never the
//     packages.
//   - Statuses and error and log text stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (format routes come before the generic item lookup, in a fixed order);
//     decision 0007: docs/research/decisions/0007-remote-repositories.md
//     (remote repositories).
//   - docs/testing/rpm.md (connecting dnf or yum, virtual repositories, the
//     Yum calculation and signing).
//   - JFrog RPM repositories:
//     https://docs.jfrog.com/artifactory/docs/rpm-repositories, and the Yum
//     calculation API:
//     https://docs.jfrog.com/artifactory/reference/calculateyumrepositorymetadata.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 45 (the ecosystem http rule) and line 405 (the root's rpm.go).
//   - Plan: docs/implementation/plan.md#p29-linux-package-repositories
//     and #p9d-modular-monolith-migration, epic WEB
//     (https://github.com/weaveplatform/artifactweave/issues/944).
//   - https://github.com/weaveplatform/artifactweave/issues/759,
//     https://github.com/weaveplatform/artifactweave/issues/1082 and
//     https://github.com/weaveplatform/artifactweave/issues/1090
//
// # Status
//
// Implemented P9c, which delivered the plan's P29 group (issues #757 to
// #760), as the server root's rpm.go. P9d made it the RPM partial strict
// server, with the Yum calculation (WEB-08), and moved it here (WEB-13).
// P10 built the virtual repodata from the shared engine's walk, merge and
// cache (https://github.com/weaveplatform/artifactweave/issues/1551).
//
// # Testing
//
// rpm_test.go calls the overlay and its helpers without the router, over a
// kittest world (fixture_test.go, which keeps the names of the root
// fixtures the tests used): the remote source's checks of independent
// createrepo_c repodata and a genuine RPM; the virtual member graph;
// signing with a missing, an RSA and an invalid GPG key; remote member
// repodata that is corrupt, oversized, unreadable or revoked; a remote
// package pinned to its indexed SHA-256; and local member content that is
// incomplete or drifted. rpm_paths_test.go adds the paths the overlay
// leaves alone, packages served only when listed below their yum root,
// members skipped when blacked out, excluded or a folder, the virtual
// repository's own patterns answering that no repository serves the path,
// packages a local or remote member's patterns, or a virtual repository's
// own, reject kept listed while their GET passes over the refusing member,
// a package served from a member whose patterns reject only its repodata,
// remote packages whose bytes are not an RPM or whose cached copy is
// revoked, and the YUM calculation's path and virtual view checks.
// virtual_test.go covers the shared engine's walk: nested members and
// cycles, members passed over (another type, a remote that misses, fails
// or may not be read), a reader of the members alone and the hidden
// refusal, the merged repodata's cache (the period, -1, a rebuild that
// finds no member, callers' visibility) on a fake clock, and the Yum
// calculation's refresh. Their item GETs run through the kit with
// TryRPMItem as the only overlay. The route suite test/server/artifacts/rpm
// checks through the router that virtual repodata is signed with the
// configured GPG key and walks a remote and a nested member, and the @rpm
// acceptance partition (test/acceptance/features/rpm.feature) is the
// native dnf evidence.
package rpmhttp
