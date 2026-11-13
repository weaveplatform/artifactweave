// Package p2http serves the Eclipse p2 protocol over the item kit: a
// remote repository's update site, read through the remote source, from a
// site.zip!/ archive or from the external children its composite metadata
// names, and a virtual repository's composite metadata over its members.
//
// # Role
//
// artifacts module, ecosystem p2, HTTP layer: the P2 slice's protocol
// handlers, beside the path and bound rules of artifacts/ecosystem/p2. It is
// named p2http so that it does not shadow net/http. Outside tests, only its
// own http subtree, the module's http adapters and app may import it (decision
// 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the router, which lists TryP2Item tenth and last among the
//     item kit's overlays (itemkit.Deps.Overlays), so that an item GET or
//     HEAD in a P2 repository reaches it before the generic lookup.
//   - Driven: the item kit (artifacts/adapters/http/itemkit), whose store
//     (itemkit.Store) the overlay is handed and a virtual repository's
//     members are read from, and which resolves the request and the caller's
//     access, reads blobs, serves a virtual member's file through its item
//     GET, and writes errors; the kit's remote source service, which fetches
//     and caches a remote's metadata, artifacts and site archives and its
//     external children, under the kit's P2 source request
//     (itemkit.P2SourceRequest) or the archive and external requests built
//     here; the generic download policy (artifacts/ecosystem/generic); the
//     maven format, which says which local repositories hold a Maven update
//     site; pkgroute, for the source errors; and the format package
//     (artifacts/ecosystem/p2), which classes paths, checks child locations,
//     extracts archive entries, reads the declared checksums and rewrites
//     composites.
//   - Settings: none. A remote's url (an update-site directory or a
//     site.zip!/ archive) and its p2ChildOrigins, the other origins its
//     composite children may name, are repository configuration.
//
// # Outputs
//
//   - TryP2Item: the overlay that serves a virtual repository's p2.index,
//     composite metadata and member files, and a remote's composite
//     metadata, external children and site archive entries, and answers
//     404 for the paths P2 refuses; a remote's own update-site files it
//     leaves to the generic lookup.
//
// # Invariants
//
//   - forceP2Authentication is not checked here. A request without
//     credentials to a P2 virtual repository with the flag on never reaches
//     the overlay: the item kit's ForceAuthentication answers it 401 before
//     the router routes it, as 7.x's force-authentication filter does. A
//     virtual repository's read of a member under <member key>/ goes to the
//     kit's GetItem past that check, so a nested P2 virtual member's own
//     flag does not apply [I].
//   - External composite children are checked before they are served. A
//     child location must be valid (p2.ValidChildLocation) and resolve,
//     within p2.MaxChildURL bytes and without user information or a
//     fragment, to the remote's own origin or one of its p2ChildOrigins; a
//     redirect must stay on the child's origin, and the remote's
//     credentials never go to another origin. A composite reaches the
//     client only with every child rewritten to a path under the
//     repository's __p2_ext/, so a client never follows an unchecked URL.
//   - A child's digest binds the source's identity, the parent and the
//     child's exact URL with its query, so a forged or stale path resolves
//     to nothing. Resolving one rereads each parent composite, at most
//     p2.MaxDepth deep, and refuses a cycle.
//   - A child's artifact is pinned to the SHA-256 and SHA-512 its site's
//     artifacts metadata declares, checked on the delivered bytes, a cached
//     copy's included. Metadata is read up to p2.MaxMetadata, and artifacts
//     and archives up to p2.MaxArtifact; an archive entry is validated
//     before it is cached or served.
//   - A virtual repository lists, in order, the members the caller may
//     traverse: P2 remotes, local Maven or Generic update sites, and
//     acyclic nested P2 virtual repositories, leaving out a blacked-out
//     one. A member's file is served under <member key>/, as 7.x serves
//     p2-virtual/download.eclipse.org/releases/luna/p2.index from the
//     repository keyed download.eclipse.org, so the path is the same for
//     every caller and does not move when another member is left out. The
//     kit's item GET serves it and checks the member's own permissions
//     again; a key that names no listed member answers 404.
//   - Without p2Urls a virtual repository's composite lists each of those
//     members at its key. With them it lists, in their order and once
//     each, the path 7.x serves each update site under (p2.SiteChild): a
//     local://<key>[/<suffix>] entry at <key>[/<suffix>], an http or https
//     one at the key of the first configured member remote whose url
//     starts it and the rest of the URL. The member is chosen among all
//     configured members, so a site's path does not depend on the caller,
//     and the site is listed only when that member is listed for the
//     caller. An entry no member serves, and a member no entry names, are
//     left out of the composite [I]; the member's files stay readable.
//   - The download policy refuses after the read permission, with a 404
//     and its message, as on the generic item route: a remote's blackout
//     and patterns refuse its archive entries, a virtual repository's
//     patterns refuse its own paths, a member's <member key>/ paths
//     included ("No repository found to serve the request"), and a member
//     that refuses a file under its key is passed over, so the virtual
//     answers "Could not find resource". Patterns never refuse p2.index,
//     as no path ending .index.
//   - A server built without a remote source service answers a remote
//     composite, child or archive entry read, made directly or through a
//     virtual repository, 500, a fault of its own, not the upstream's. A
//     composite child path that is not well formed is refused before any
//     read, as it is with a service.
//   - A folder, a path P2 does not class, or a P2 repository that is
//     neither remote nor virtual answers 404.
//   - A file takes a query and matrix parameters as any other file's GET
//     does, once read permission and the download policy are settled
//     (#1350): a query, such as skipUpdateStats (RTFACT-25636), changes
//     nothing, and matrix parameters are matched against the file's
//     properties (JFrog's guide, Using Properties in Deployment and
//     Resolution). A virtual repository's p2.index and composite files
//     carry none [I], so only a mandatory key refuses them, and a member's
//     file under <member key>/ is asked of the member with both, as its own
//     GET would be. A remote matches the copy its cache holds before the
//     upstream is asked; a file at the remote's own URL passes the query,
//     under Propagate Query Params, and the matrix parameters to the
//     upstream (#1371), and an external child or a site archive entry,
//     fetched from another URL, passes neither [I].
//   - The __p2_ext/ paths and the digest's input are the cache paths of
//     external content, and the source variants name its validation, so
//     both stay byte-identical, as do statuses and error and log text.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (format routes come before the generic item lookup, in a fixed order);
//     decision 0007: docs/research/decisions/0007-remote-repositories.md
//     (every remote exit of a format uses the shared transport and cache,
//     a P2 remote's external children and site archives included).
//   - docs/testing/p2.md (remote and virtual repositories, composite
//     children and their origins, and the bounds).
//   - JFrog P2 repositories:
//     https://docs.jfrog.com/artifactory/docs/p2-repositories, and its
//     section "Allow Anonymous Access for P2" for forceP2Authentication,
//     which JFrog's virtual repository settings list for P2
//     (https://docs.jfrog.com/artifactory/docs/virtual-repositories) and
//     its remote repository settings do not
//     (https://docs.jfrog.com/artifactory/docs/remote-repositories).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 45 (the ecosystem http rule) and line 408 (the root's p2.go and
//     p2_external.go).
//   - Plan: docs/implementation/plan.md#p31-native-build-and-plugin-repositories
//     and #p9d-modular-monolith-migration, epic WEB
//     (https://github.com/weaveplatform/artifactweave/issues/944).
//   - RTFACT-7631 and RTFACT-29198, whose P2 virtual requests name the
//     member's key and the update site's sub-path, and whose anonymous
//     requests forceP2Authentication answers with a 401:
//     https://jfrog.atlassian.net/browse/RTFACT-7631 and
//     https://jfrog.atlassian.net/browse/RTFACT-29198; RTFACT-29915, the
//     local://<key>/<suffix> form of a p2Urls entry:
//     https://jfrog.atlassian.net/browse/RTFACT-29915.
//   - https://github.com/weaveplatform/artifactweave/issues/773,
//     https://github.com/weaveplatform/artifactweave/issues/1084,
//     https://github.com/weaveplatform/artifactweave/issues/1090,
//     https://github.com/weaveplatform/artifactweave/issues/1247,
//     https://github.com/weaveplatform/artifactweave/issues/1257 and
//     https://github.com/weaveplatform/artifactweave/issues/1280
//
// # Status
//
// Implemented P9c, which delivered the plan's P31 group (issues #770 to
// #775), as the server root's p2.go and p2_external.go. P9d moved it here
// (WEB-15).
//
// # Testing
//
// The tests call the protocol's code without the router, over in-memory
// stores (fixture_test.go, which keeps the names of the root fixtures the
// tests used):
//
//   - p2_test.go: the kit's P2 source request on the published Eclipse
//     GLSP files, the site.zip!/ suffix, and a virtual repository that
//     fails closed when its member store fails and leaves out a
//     blacked-out member.
//   - p2_external_helpers_test.go: child URLs and their origins, the child
//     digest's inputs, resource paths within a site or an archive, the
//     __p2_ext/ path parser, checksum pinning and archive suffix shapes.
//   - p2_external_error_test.go: reads that fail closed at the source and
//     delivery fences, cached metadata served from its stored bytes, bounded
//     streams, and archive entries checked before publication.
//   - p2_nosource_test.go: remote reads, direct and through a virtual
//     repository, on a server built without a remote source service.
//
// The route suite test/server/artifacts/p2 checks the protocol through the
// router, and the @p2 acceptance partition
// (test/acceptance/features/p2.feature), which drives the Eclipse p2
// director, is the native evidence.
package p2http
