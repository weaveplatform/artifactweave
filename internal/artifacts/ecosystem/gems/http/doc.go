// Package gemshttp serves RubyGems and Bundler's native repository protocol
// on Artifactory's /artifactory/api/gems/{repo} prefix, on gems local,
// federated, remote and virtual repositories: gem push and yank, the gems
// themselves, the compact index Bundler reads, the legacy Marshal indexes
// gem reads, and the gem, versions and search JSON. The archive and index
// rules are the format package's (artifacts/ecosystem/gems); this package
// answers HTTP with them.
//
// # Role
//
// artifacts module, ecosystem layer: the native RubyGems and Bundler
// protocol of the RubyGems slice. It is named gemshttp so that it does not
// shadow net/http. Outside tests, only its own http subtree, the module's
// http adapters and app may import it (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type gems. JFrog's reindex
//     operation, /api/gems/{repoKey}/reindex, is served beside the
//     protocol, not by it.
//   - Driven: the router kit (pkgroute.Kit), whose store (pkgroute.Store)
//     the handlers also reach through DB(), and which resolves the
//     repository and the caller, checks permissions, stores a pushed gem
//     through the item service, where the format's hooks admit it, deletes
//     a yanked one, reads items and blobs, builds a virtual repository's
//     member requests, serves items and writes errors; the remote source
//     service, which fetches a remote's upstream compact index and gems
//     into the remote cache; and the format package, whose rules read every
//     gem and render and check every index.
//
// # Outputs
//
//   - Protocol and Options: the routes, the source of each repository
//     class, the reserved reindex and updateIndex paths and the Artifactory
//     error profile.
//   - Answers: a push's "Successfully registered gem" text, a yank's
//     "Successfully deleted gem" text, a gem, the compact index (names,
//     versions and info/<name>), the Marshal indexes (specs.4.8.gz,
//     latest_specs.4.8.gz, prerelease_specs.4.8.gz and
//     quick/Marshal.4.8/<file>.gemspec.rz), and JSON for a gem's latest
//     version (api/v1/gems/<name>.json), its versions
//     (api/v1/versions/<name>) and a name search (api/v1/search.json).
//     Index answers are private and kept by no cache (Cache-Control:
//     private, no-store), with an MD5 ETag and a SHA-256 Repr-Digest.
//
// # Invariants
//
//   - Only local and federated repositories take a push or a yank (405),
//     with write or delete permission under gems/. A pushed gem of at most
//     256 MiB must read as valid (400) and is stored at the path its
//     identity gives, never over a stored version (409). A yank's form, at
//     most 4 KiB, or its query, names the gem, its version and its
//     platform (ruby when none is given).
//   - Only the api/v1/search paths take a query (400); any other path that
//     is neither a gem nor an index answers 404.
//   - Index answers list only the gems the caller may read. A local or
//     federated repository renders them from its stored gems and fails
//     closed when one cannot be read. A remote renders them from its
//     upstream's compact index, whose info files must match the MD5 the
//     versions file lists, and fetches a gem only when that index lists its
//     version, delivering it only when it validates and matches the listed
//     SHA-256. A virtual repository renders them from its members in order:
//     the first member to list a path supplies it, the caller must be able
//     to read it in both repositories, and a member cycle is refused (400).
//   - A listing is bounded: more than 10,000 stored files, or upstream gem
//     names, fails as an invalid result.
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/testing/gems.md (connecting gem and Bundler, remote and virtual
//     repositories, and the evidence), and docs/client-recipes/gems.md (the
//     client recipe, whose evidence is test/acceptance/gems_steps_test.go).
//   - JFrog RubyGems repositories:
//     https://docs.jfrog.com/artifactory/docs/rubygems-repositories; the
//     RubyGems compact index specification:
//     https://guides.rubygems.org/rubygems-org-compact-index-api/; and the
//     RubyGems API, which defines push: https://guides.rubygems.org/rubygems-org-api/.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 233 (ecosystem/gems) and line 1017 (the mapping row).
//   - Plan: docs/implementation/plan.md#p30-language-package-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/764,
//     https://github.com/weaveplatform/artifactweave/issues/1049 and
//     https://github.com/weaveplatform/artifactweave/issues/1089
//
// # Status
//
// Implemented P9c, which delivered the plan's P30 group (issues #762 to
// #768). Moved here in P9d.
//
// # Testing
//
//   - gems_test.go: the gem and versions JSON documents, unknown index
//     paths, and the virtual source's refusal of a single-item lookup.
//
// The guard suite test/server/artifacts/gems drives the protocol through
// the whole server: local, federated and virtual reads, push and its
// permissions, refused requests, yank, token authentication, a virtual
// repository's member filtering and cycles, stored gems that fail closed,
// the remote's index checks, cache, offline reads and redirects, and the
// gem and Bundler clients installing a pinned rake
// (TestAccRubyClientsInstallPinnedRake, which skips unless gem and bundle
// are installed). The @gems acceptance scenarios drive gem and Bundler
// against all four classes.
package gemshttp
