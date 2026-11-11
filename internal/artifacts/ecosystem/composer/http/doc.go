// Package composerhttp serves Composer 1 local catalogs and Composer 2
// metadata through the native Artifactory Composer URL,
// /artifactory/api/composer/{repoKey}, on local, federated, remote and
// virtual repositories: packages.json, a package's Composer 2 metadata, its
// archives and the reindex operation. Archive validation and the package
// rules are the format package's (artifacts/ecosystem/composer); this
// package answers HTTP with them.
//
// # Role
//
// artifacts module, ecosystem layer: the native Composer 1 and 2 catalog
// protocol of the Composer slice. It is named composerhttp so that it does
// not shadow net/http. Outside tests, only its own http subtree, the
// module's http adapters and app may import it (decision 0044, dependency
// rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type composer. Archives are
//     published through the generic item routes, not through this
//     protocol; the format's Handler admits them there.
//   - Driven: the router kit (pkgroute.Kit), whose store (pkgroute.Store)
//     the handlers also reach through DB(), and which resolves the
//     repository (a key with the -cache suffix names a remote's cache) and
//     the caller, checks permissions, lists items and reads their
//     properties, builds a virtual repository's member requests, serves
//     items and writes errors; the blob store in Options, from which the
//     hosted catalogs read every archive; the remote source service, which
//     fetches a remote's upstream packages.json, package metadata and
//     archives into the remote cache; and the format package.
//
// # Outputs
//
//   - Protocol and Options: the routes, the source of each repository
//     class and the Artifactory error profile.
//   - Answers: packages.json, which gives a Composer 1 user agent a local or
//     federated repository's full version map and any other caller only
//     the metadata-url template of the Composer 2 documents; a package's
//     stable or development versions, each with a dist entry that points
//     back through this repository; an archive's bytes; and a plain-text
//     acknowledgement of a reindex.
//
// # Invariants
//
//   - Hosted catalogs are built from the stored archives on every request
//     and list only archives the caller may read; an unknown package
//     answers 404, and a stored archive that no longer validates answers
//     502.
//   - A dist URL names this repository under its trusted base URL, so the
//     client downloads through it and its permissions; a hosted archive
//     carries its SHA-1 as the shasum.
//   - A virtual repository's members are distinct and read in order; the
//     first readable member that holds a package supplies all its versions.
//   - A remote accepts an upstream metadata-url only on the upstream's own
//     scheme and host, and dist URLs only as http or https without
//     credentials, query or fragment. Its entries are served without their
//     source field, and an archive is delivered only when it holds the
//     package and version asked for and matches its declared SHA-1, if
//     any.
//   - A catalog or archive request with a query string answers 400.
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/operations/composer.md (connecting Composer, the Composer 1
//     support window, virtual member order, the -cache alias and reindex).
//   - JFrog PHP Composer repositories:
//     https://docs.jfrog.com/artifactory/docs/php-composer-repositories, and
//     the Composer repository specification:
//     https://getcomposer.org/doc/05-repositories.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 233 (ecosystem/composer) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p30-language-package-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/763,
//     https://github.com/weaveplatform/artifactweave/issues/1047 and
//     https://github.com/weaveplatform/artifactweave/issues/1089
//
// # Status
//
// Implemented P9c, which delivered the plan's P30 group (issues #762 to
// #768). Moved here in P9d.
//
// # Testing
//
//   - composer_test.go: hosted metadata and dist entries, development
//     versions and stored integrity, virtual member order, missing members
//     and the -cache alias, refusals and virtual authority, the trusted base
//     URL, and route parsing.
//   - remote_test.go: the remote's metadata cache and integrity checks,
//     untrusted upstream metadata, access revoked at delivery, a remote
//     without a registry URL, source failures, and a virtual repository
//     that selects a remote's archive.
//
// The router's guard TestAccNativeComposerRoutesAvoidHandwrittenStubs
// (internal/app) and the @composer acceptance scenarios, with the native
// Composer client, drive it through the whole server.
package composerhttp
