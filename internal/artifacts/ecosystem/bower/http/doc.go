// Package bowerhttp serves the Bower registry and bower-art-resolver 2.0
// protocol at /artifactory/api/bower/{repoKey}: the package list, search
// and lookup, which name a package by an art:// URL, the refs that list a
// package's versions as tags, the binaries that deliver a version's
// archive, and registration of a package's Git URL with a remote or
// virtual repository.
//
// # Role
//
// artifacts module, ecosystem layer: the native Bower registry and
// bower-art-resolver protocol of the Bower slice. It is named bowerhttp so
// that it does not shadow net/http. Outside tests, only its own http
// subtree, the module's http adapters and app may import it (decision
// 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type bower, local, federated,
//     virtual or remote. The repository's reindex ({repoKey}/reindex) is a
//     JFrog operation beside the protocol, which the protocol reserves.
//   - Driven: the router kit (pkgroute.Kit), whose store (pkgroute.Store)
//     the handlers also reach through DB(), and which resolves the
//     repository and the caller, checks permissions, reads items and blobs,
//     keeps Bower registrations and writes errors; the remote source
//     service (Options.RemoteSources), which fetches registry pointers, Git
//     refs and archives, or an upstream Artifactory's refs and binaries,
//     into the remote cache; and the format package, whose rules check
//     every name, version, path and manifest.
//
// # Outputs
//
//   - Protocol and Options: the routes, the source of each repository
//     class, the reserved reindex path and the Artifactory error profile.
//   - Answers: package entries as JSON, refs as text lines, version
//     archives, and 201 for a registration, or 403 for a name already
//     registered.
//
// # Invariants
//
//   - A local or federated version's archive is built from its stored
//     tree only when the caller may read every file in it, within 4096
//     files and 128 MiB, and only when its bower.json is stored.
//   - A Git remote follows a registry pointer only within the configured
//     Git origin and path, and an archive enters the remote cache only
//     after its paths, size, file count, manifest and gzip integrity pass;
//     a stated manifest version must agree with the selected tag.
//   - A registration is immutable, needs write permission on the package in
//     the addressed repository and the selected remote, and treats a
//     private Stash SSH address as a locator, never as a transport.
//   - A virtual repository resolves its members in order, and, when it
//     rewrites external dependencies, admits only sources its allowlist
//     patterns match, through its selected remote's cache.
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/testing/bower.md (connecting the client and resolver, remote,
//     virtual and private Stash repositories, and the evidence), and
//     docs/research/38-bower-registration-and-external-dependencies.md
//     (registration and the external dependency rewrite).
//   - JFrog Bower repositories:
//     https://docs.jfrog.com/artifactory/docs/bower-repositories.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 232 (ecosystem/bower) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p30-language-package-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/768,
//     https://github.com/weaveplatform/artifactweave/issues/1040 and
//     https://github.com/weaveplatform/artifactweave/issues/1089
//
// # Status
//
// Implemented P9c, which delivered the plan's P30 group (issues #762 to
// #768). Moved here in P9d.
//
// # Testing
//
// The tests are in the package, by concern: bower_test.go (local,
// federated and virtual resolution, remote refs and archives),
// security_test.go (disclosure, virtual catalogs and archive
// permissions), git_test.go and remote_security_test.go (Git and
// Artifactory remotes), register_test.go (registration), the dependency_*
// and dependencies_test.go files (the external dependency rewrite), and
// archive_fault_test.go, failure_paths_test.go and source_fault_test.go
// (corrupt archives and failing sources).
package bowerhttp
