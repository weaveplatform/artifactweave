// Package cargohttp serves the HTTP sparse registry and Cargo web API on
// Cargo local, federated and remote repositories, at
// /artifactory/api/cargo/{repoKey}: the sparse index's config.json and
// index files, the native publish, crate downloads, yank and unyank, and a
// crate's version list. Crate validation and the index rules are the format
// package's (artifacts/ecosystem/cargo); this package answers HTTP with
// them.
//
// # Role
//
// artifacts module, ecosystem layer: the native Cargo sparse registry and
// web API of the Cargo slice. It is named cargohttp so that it does not
// shadow net/http. Outside tests, only its own http subtree, the module's
// http adapters and app may import it (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type cargo. JFrog's reindex
//     operation, /api/cargo/{repoKey}/reindex, is served beside the
//     protocol, not by it.
//   - Driven: the router kit (pkgroute.Kit), whose store (pkgroute.Store)
//     the handlers also reach through DB(), and which resolves the
//     repository and the caller, checks permissions, reads items and their
//     properties, stores a published crate through the item service, where
//     the format's hooks admit it, annotates a yanked version, serves items
//     and writes errors; the remote source service, which fetches a
//     remote's upstream config.json, index files and crates into the remote
//     cache; and the format package, whose rules check every name, version,
//     path, crate and record.
//
// # Outputs
//
//   - Protocol: the routes, the source of each repository class, the
//     reserved reindex operation and the Artifactory error profile.
//   - Answers: config.json, naming this repository's API and download URLs
//     and whether it requires authentication; a crate's index file, one
//     record per readable version; the publish answer, with empty warnings;
//     a version's crate bytes; {"ok": true} for a yank or unyank; and a
//     local crate's versions with their records. The protocol's own
//     refusals use Cargo's {"errors": [{"detail": ...}]} body; the kit's,
//     such as a permission refusal, and the download policy's keep the
//     Artifactory envelope.
//
// # Invariants
//
//   - An index file lists only the versions the caller may read, at most
//     10,000; a crate with none answers 404.
//   - The download policy (decision 0032), after read permission: the
//     blackout and the patterns refuse config.json at config.json and an
//     index file at index/<prefix>/<name>, where 7.x keeps them, a remote's
//     included, with the policy's 404 naming that path; a pattern on one
//     crate leaves its version listed. A blacked-out repository refuses a
//     crate's versions. A remote crate answers its own path's policy before
//     the config.json and index file it is found through are read, and
//     those reads check only the blackout.
//   - A publish needs write permission on the crate's path. Its body, a
//     4-byte length, the metadata JSON (at most 1 MiB), a 4-byte length and
//     the crate, must carry a crate whose Cargo.toml matches the metadata
//     (400), and a stored version is never replaced (409).
//   - Yank and unyank need annotate permission and change only the
//     version's yanked flag. A remote repository is read-only (405) and has
//     no version list (405).
//   - A remote reads its upstream as an HTTP sparse index: its config.json
//     must name an http or https download URL without credentials, its
//     index files must hold valid records of the crate asked for, and a
//     crate is delivered only when it is valid and its SHA-256 is its
//     record's checksum. The upstream lines it serves keep the fields this
//     server does not interpret.
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/operations/cargo.md (connecting Cargo, the repository classes,
//     and the remote's sparse index root).
//   - JFrog Cargo repositories:
//     https://docs.jfrog.com/artifactory/docs/cargo-repositories, and the
//     Cargo sparse index and web API:
//     https://doc.rust-lang.org/cargo/reference/registry-index.html and
//     https://doc.rust-lang.org/cargo/reference/registry-web-api.html.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 232 (ecosystem/cargo) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p30-language-package-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/762,
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
//   - cargo_test.go: publish frames and metadata, validation and the
//     read-only classes, stored index integrity and permissions, and
//     publish, sparse resolution and yank across the classes.
//   - remote_test.go: the remote's sparse cache, offline reads, redirects
//     and integrity checks, corrupt upstream index files and crates, and
//     download URL templates.
//   - remote_unit_test.go: the checks of an upstream config.json and index
//     file.
//   - remote_storage_test.go: a remote crate whose cached copy fails a read
//     while it is checked answers 500 "internal error" and is not cached,
//     while a crate that breaks the rules still answers 502
//     (https://github.com/weaveplatform/artifactweave/issues/1269).
//   - download_policy_test.go: the download policy on hosted and remote
//     repositories.
//
// The router's guard TestAccCargoNativeSpecRoutes (internal/app) and the
// @cargo acceptance scenarios, with the native Cargo client, drive it through
// the whole server.
package cargohttp
