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
//     acyclic nested P2 virtual repositories. A member's file is served
//     under __p2/<n>/ by the kit's item GET, which checks the member's own
//     permissions again.
//   - A folder, matrix parameters, a query, a path P2 does not class, or a
//     P2 repository that is neither remote nor virtual answers 404.
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
//     https://docs.jfrog.com/artifactory/docs/p2-repositories.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 45 (the ecosystem http rule) and line 408 (the root's p2.go and
//     p2_external.go).
//   - Plan: docs/implementation/plan.md#p31-native-build-and-plugin-repositories
//     and #p9d-modular-monolith-migration, epic WEB
//     (https://github.com/weaveplatform/artifactweave/issues/944).
//   - https://github.com/weaveplatform/artifactweave/issues/773,
//     https://github.com/weaveplatform/artifactweave/issues/1084 and
//     https://github.com/weaveplatform/artifactweave/issues/1090
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
//     fails closed when its member store fails.
//   - p2_external_helpers_test.go: child URLs and their origins, the child
//     digest's inputs, resource paths within a site or an archive, the
//     __p2_ext/ path parser, checksum pinning and archive suffix shapes.
//   - p2_external_error_test.go: reads that fail closed at the source and
//     delivery fences, cached metadata served from its stored bytes, bounded
//     streams, and archive entries checked before publication.
//
// The route suite test/server/artifacts/p2 checks the protocol through the
// router, and the @p2 acceptance partition
// (test/acceptance/features/p2.feature), which drives the Eclipse p2
// director, is the native evidence.
package p2http
