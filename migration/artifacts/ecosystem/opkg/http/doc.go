// Package opkghttp serves the Opkg protocol over the item kit: a feed's
// Packages and Packages.gz, a virtual feed's Release and Packages.sig, and
// the IPKs of virtual and remote repositories. A virtual repository's feed
// is composed from its members' and signed with its key pair when it names
// one; any other repository's Packages is served as stored or fetched.
//
// # Role
//
// artifacts module, ecosystem opkg, HTTP layer: the Opkg slice's protocol
// handlers, beside the format rules of artifacts/ecosystem/opkg. It is
// named opkghttp so that it does not shadow net/http. Outside tests, only
// its own http subtree, the module's http adapters and app may import it
// (decision 0044, dependency rule 9); the server root counts as app until
// it moves there.
//
// # Inputs
//
//   - Driving: the router, which lists TryOpkgItem ninth among the item
//     kit's overlays (itemkit.Deps.Overlays), so that an item GET or HEAD in
//     an Opkg repository reaches it before the generic lookup.
//   - Driven: the item kit (artifacts/adapters/http/itemkit), which resolves
//     the request and the caller's access, reads items and blobs, serves
//     stored files and remote streams and writes errors; the kit's remote
//     source service, which fetches a remote's feed and IPKs under the kit's
//     Opkg source request (itemkit.OpkgSourceRequest); access/api's Keys,
//     through which the format's SignFeed reads a virtual repository's GPG
//     key pair; the generic download policy (artifacts/ecosystem/generic);
//     and the format package (artifacts/ecosystem/opkg), whose rules parse
//     every feed path, decode member feeds and encode and sign the virtual
//     feed.
//
// # Outputs
//
//   - TryOpkgItem: the overlay that serves an Opkg repository's Packages
//     and Packages.gz, every feed file of a virtual repository, and the
//     IPKs of a virtual or remote repository. It answers 404 for an IPK
//     path the format refuses, and leaves every other request to the
//     overlays after it and the generic lookup.
//
// # Invariants
//
//   - A caller receives a local, federated or remote repository's feed only
//     when they may read every IPK it lists. A virtual feed lists only the
//     IPKs the caller may read, both in the virtual repository and in the
//     member that holds them, and an IPK path belongs to the first member
//     that lists it.
//   - A missing member, or one whose feed is unpublished or unreadable to
//     the caller, is skipped; a member of another package type, a member
//     cycle or a chain of more than eight nested repositories is refused.
//   - An IPK is served only when a feed the caller can see lists it, and
//     only with the SHA-256 that feed gives.
//   - A feed is read up to 36 MiB, a remote's is validated before it is
//     used, and the remote source's authority is checked again before a
//     feed is disclosed. Feed responses are private and never stored by a
//     shared cache.
//   - A request with matrix parameters or a query string answers 404.
//   - Statuses and error and log text stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (format routes come before the generic item lookup, in a fixed order);
//     decision 0007: docs/research/decisions/0007-remote-repositories.md
//     (remote repositories).
//   - docs/testing/opkg.md (connecting opkg, virtual feeds, signing and the
//     acceptance evidence).
//   - JFrog Opkg repositories:
//     https://docs.jfrog.com/artifactory/docs/opkg-repositories.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 45 (the ecosystem http rule) and line 405 (the root's opkg.go).
//   - Plan: docs/implementation/plan.md#p29-linux-package-repositories
//     and #p9d-modular-monolith-migration, epic WEB
//     (https://github.com/weaveplatform/artifactweave/issues/944).
//   - https://github.com/weaveplatform/artifactweave/issues/760 and
//     https://github.com/weaveplatform/artifactweave/issues/1082
//
// # Status
//
// Implemented P9c, which delivered the plan's P29 group (issues #757 to
// #760), as the server root's opkg.go. P9d moved it here (WEB-13).
//
// # Testing
//
// opkg_test.go calls the overlay and its helpers without the router, over a
// kittest world (fixture_test.go, which keeps the names of the root
// fixtures the tests used): the remote source's checks of the published
// OpenWrt feed and IPK; the virtual member graph and the overlay's routing;
// local member IPKs that are missing or drifted; a remote feed's faults
// and revoked authority; and a remote IPK pinned to its indexed SHA-256 and
// cached size. Its item GETs run through the kit with TryOpkgItem as the
// only overlay. The route suite test/server/artifacts/opkg checks through
// the router that a virtual feed is signed only with the configured GPG
// key, and the @opkg acceptance partition
// (test/acceptance/features/opkg.feature) is the native opkg evidence.
package opkghttp
