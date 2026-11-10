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
//     only when they may read every package it lists. A virtual index lists
//     only the packages the caller may read, both in the virtual repository
//     and in the member that holds them, and a package path belongs to the
//     first member that lists it, the member a package GET serves it from.
//   - A missing member, or one whose index is unpublished or unreadable to
//     the caller, is skipped; a member of another package type, a member
//     cycle or a chain of more than eight nested repositories is refused.
//   - A virtual repository serves a package only when an index the caller
//     can see lists it.
//   - An index is read up to 36 MiB, a remote's is validated before it is
//     used, and the remote source's authority is checked again before an
//     index is disclosed. Index responses are private and never stored by a
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
// the layout and callers who may not download refused; local index content
// that is a folder, lost, empty, unreadable or malformed; a remote index
// whose authority is rechecked, streamed or cached; virtual indexes that
// skip unreadable members and fail closed on cycles, wrong member types,
// store faults and unusable signing keys; and virtual packages served from
// a listed local file or relayed from a remote member. Its item GETs run
// through the kit with TryAlpineItem as the only overlay. The @alpine
// acceptance partition (test/acceptance/features/alpine.feature) drives its
// indexes and packages through the router with apk.
package alpinehttp
