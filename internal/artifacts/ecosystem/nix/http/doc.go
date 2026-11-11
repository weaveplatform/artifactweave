// Package nixhttp serves the native Nix HTTP binary-cache protocol under
// /artifactory/api/nix/{repoKey}: nix-cache-info, narinfo files and the NAR
// archives they name for GET and HEAD, publication and deletion of each
// with PUT and DELETE, and a rebuild of the repository's index with POST
// reindex. A hosted repository signs each narinfo it serves with its
// Ed25519 key pair, and a remote one verifies upstream signatures against
// its own. Metadata and archive rules are the format package's
// (artifacts/ecosystem/nix); this package answers HTTP with them.
//
// # Role
//
// artifacts module, ecosystem layer: the native Nix HTTP binary-cache
// protocol of the Nix slice. It is named nixhttp so that it does not shadow
// net/http. Outside tests, only its own http subtree, the module's http
// adapters and app may import it (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type nix, local, federated,
//     virtual or remote.
//   - Driven: the router kit (pkgroute.Kit), which resolves the repository
//     and the caller, checks permissions, reads, stores and deletes items
//     and writes errors; the access contracts in Options (access/api): the
//     authorizer, which evaluates the caller's permissions again just
//     before a hosted or virtual repository's narinfo or archive is served,
//     and the key pairs, which sign a hosted narinfo and verify a remote's;
//     the remote source service (Options.RemoteSources), which fetches
//     upstream narinfo and archives into the remote cache; and the format
//     package.
//
// # Outputs
//
//   - Protocol and Options: the routes, the source of each repository
//     class and the Artifactory error profile.
//   - Answers: nix-cache-info; a narinfo, signed when a local or federated
//     repository names a primary key pair, with its archive URL rewritten
//     to nar/<store hash>/<file> so that every archive download passes its
//     narinfo's checks; the archive; and 201 for a publication, 204 for a
//     deletion and 200 for a reindex.
//
// # Invariants
//
//   - A narinfo and its archive are served only to a caller who may read
//     both, and only after the repository's configuration and the caller's
//     permissions (or, for a remote, the source's authority) are checked
//     again just before delivery.
//   - A configured signing key that cannot be read, is not Ed25519, or
//     whose key material or alias is invalid fails closed with 503.
//   - A remote's narinfo must carry a valid signature by one of its
//     configured key pairs; with none configured, upstream signatures are
//     kept for the client's own trusted-public-keys check. Its archive must
//     match the narinfo's hashes and sizes, and every upstream URL and
//     redirect must stay beneath the remote's URL, on its origin.
//   - A request with a query string is refused, publication needs a local
//     or federated repository, and a virtual repository cannot be
//     reindexed.
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/testing/nix.md (connecting Nix, signatures, the limits, remote
//     and virtual caches, the lifecycle and the evidence).
//   - JFrog Nix repositories:
//     https://docs.jfrog.com/artifactory/docs/nix-repositories, and Nix's
//     HTTP binary-cache store:
//     https://nix.dev/manual/nix/2.35/store/types/http-binary-cache-store.html.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 236 (ecosystem/nix) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p33-automation-and-source-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/788 and
//     https://github.com/weaveplatform/artifactweave/issues/1037
//
// # Status
//
// Implemented P9c, which delivered the plan's P33 group (issues #782 to
// #788). Moved here in P9d.
//
// # Testing
//
//   - nix_test.go: publication, signing and the lifecycle; denials and
//     malformed publications; federated and ordered virtual repositories;
//     remote integrity, caching and offline reads; untrusted upstream
//     metadata and bad archives; and a virtual repository's member order
//     evaluated again on each read.
//   - fault_test.go: a remote source's authority, hosted signing failures,
//     path and virtual policy, exact-path permissions and revocation,
//     faults that must not answer as a miss, and the request and delivery
//     fences.
package nixhttp
