// Package alpinehttp serves the Alpine protocol over the item kit: each
// architecture folder's APKINDEX.tar.gz, and a virtual repository's
// packages. A virtual repository's index is composed from its members' and
// signed with its key pair when it names one; any other repository's index
// is served as stored or fetched.
//
// # Role
//
// artifacts module, ecosystem alpine, HTTP layer: the Alpine slice's protocol
// handlers, beside the format rules of artifacts/ecosystem/alpine. It is named
// alpinehttp so that it does not shadow net/http. Outside tests, only its own
// http subtree, the module's http adapters and app may import it (decision
// 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the router, which lists TryAlpineItem second among the item
//     kit's overlays (itemkit.Deps.Overlays), so that an item GET or HEAD in
//     an Alpine repository reaches it before the generic lookup.
//   - Driven: the item kit (artifacts/adapters/http/itemkit), whose store
//     (itemkit.Store) the overlay is handed and reads repositories and items
//     from, and which resolves the request and the caller's access, reads
//     items and blobs, serves stored files and remote streams and writes
//     errors; the kit's remote source service, which fetches a remote's
//     index and packages under the kit's Alpine source request
//     (itemkit.AlpineSourceRequest); access/api's Keys, through which the
//     format's SigningKey reads a virtual repository's RSA key pair; the
//     generic download policy (artifacts/ecosystem/generic); and the format
//     package (artifacts/ecosystem/alpine), whose rules parse every path,
//     decode member indexes and encode and sign the virtual index.
//
// # Outputs
//
//   - TryAlpineItem: the overlay that serves an Alpine repository's
//     APKINDEX.tar.gz files and a virtual repository's .apk packages. It
//     answers 404 for a package path the format refuses, and leaves every
//     other request to the overlays after it and the generic lookup.
//
// # Invariants
//
//   - A caller receives a local, federated or remote repository's index
//     only when they have Read permission on every package it lists. A
//     virtual index lists only the packages the caller has Read permission
//     on, both in the virtual repository and in the member that holds them,
//     and a package path belongs to the first member that lists it. The
//     download policy does not filter or refuse an index for the packages
//     it lists: a package whose file the include and exclude patterns
//     reject stays listed and only its GET is refused, as in 7.x's Debian
//     and RPM indexes (RTFACT-30830, RTFACT-30656).
//   - An index and a virtual package are read under the download policy at
//     their own path, after the Read permission check (DV-057): a
//     blacked-out repository, or patterns that reject the path, answer the
//     item route's 404 and message, and a virtual repository's own patterns
//     answer that no repository serves the request. A virtual package's GET
//     passes over a member whose download policy refuses the file to the
//     next member that lists it.
//   - A missing member, or one whose index is unpublished, unreadable to the
//     caller or refused by the member's download policy, is skipped; a
//     member of another package type, a member cycle or a chain of more
//     than eight nested repositories is refused. A virtual package's GET
//     checks a member's policy at the package's own path, never at the
//     index it reads to find the package, so there only a blacked-out
//     member is skipped for its index.
//   - A virtual repository serves a package only when an index the caller
//     can see lists it.
//   - An index is read up to 36 MiB, a remote's is validated before it is
//     used, and the remote source's authority is checked again before an
//     index is disclosed. Index responses are private and never stored by a
//     shared cache.
//   - A server built without a remote source service answers a remote
//     index or package read 500, a fault of its own, not the upstream's.
//   - An index and a virtual package take a query and matrix parameters as
//     any other file's GET does, once read permission and the download
//     policy are settled (#1350): a query, such as skipUpdateStats
//     (RTFACT-25636), changes nothing, and matrix parameters are matched
//     against the file's properties (JFrog's guide, Using Properties in
//     Deployment and Resolution). A virtual repository's composed index
//     carries none [I], so only a mandatory key refuses it, and a virtual
//     package's GET passes over a member whose file does not match to the
//     next member that lists it [I]. A remote's index, and a remote
//     member's package, match the copy its cache holds before the upstream
//     is asked, and pass the query, under Propagate Query Params, and the
//     matrix parameters to the upstream (#1371).
//   - Statuses and error and log text stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (format routes come before the generic item lookup, in a fixed order);
//     decision 0007: docs/research/decisions/0007-remote-repositories.md
//     (remote repositories).
//   - docs/testing/alpine.md (connecting apk, signing keys and the
//     acceptance evidence).
//   - JFrog Alpine Linux repositories:
//     https://docs.jfrog.com/artifactory/docs/alpine-linux-repositories.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 45 (the ecosystem http rule) and line 405 (the root's alpine.go).
//   - Plan: docs/implementation/plan.md#p29-linux-package-repositories
//     and #p9d-modular-monolith-migration, epic WEB
//     (https://github.com/weaveplatform/artifactweave/issues/944).
//   - https://github.com/weaveplatform/artifactweave/issues/757,
//     https://github.com/weaveplatform/artifactweave/issues/1082 and
//     https://github.com/weaveplatform/artifactweave/issues/1090
//
// # Status
//
// Implemented P9c, which delivered the plan's P29 group (issues #757 to
// #760), as the server root's alpine.go. P9d moved it here (WEB-13).
//
// # Testing
//
// alpine_test.go calls the overlay and its helpers without the router, over
// a kittest world (fixture_test.go, modelled on rpmhttp's): paths outside
// the layout and callers who may not download refused, and indexes and
// virtual packages the download policy refuses answering its 404; a
// package the patterns reject kept in a local, remote or virtual index
// while its GET is refused or passed over to the next member; a virtual
// package served from a member whose patterns reject only its index; local
// index content that is a folder, lost, empty, unreadable or malformed; a
// remote index whose authority is rechecked, streamed or cached; virtual
// indexes that skip unreadable members and fail closed on cycles, wrong
// member types, store faults and unusable signing keys; virtual packages
// served from a listed local file or relayed from a remote member; and
// remote index and package reads on a server built without remote sources.
// Its item GETs run through the kit with TryAlpineItem as the only overlay.
// The @alpine acceptance partition (test/acceptance/features/alpine.feature)
// drives its indexes and packages through the router with apk.
package alpinehttp
