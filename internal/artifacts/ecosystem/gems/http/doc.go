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
//     a yanked one, reads items and blobs, walks a virtual repository's
//     members (MergeMembers, Collect) and logs a member it passes over
//     for a fault, serves items and writes errors; the core virtual
//     package (artifacts/core/virtual), which names a virtual repository's
//     deployment repository; the remote source
//     service, which fetches a remote's upstream compact index and gems
//     into the remote cache; and the format package, whose rules read every
//     gem and render and check every index, and whose Merger merges a
//     virtual repository's members' gems.
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
//     The compact index and the Marshal indexes, which 7.x stores, answer
//     as a stored file in the package download service's form
//     (serveDocument, RTFACT-27459): X-Artifactory-Filename,
//     Content-Disposition, the X-Checksum headers, Last-Modified,
//     Accept-Ranges and Cache-Control: no-store, with the quoted MD5 as
//     the compact index's ETag and the bare SHA-1 as a Marshal index's.
//     The JSON answers are private and kept by no cache (Cache-Control:
//     private, no-store), with an MD5 ETag. Every index answer carries a
//     SHA-256 Repr-Digest.
//
// # Invariants
//
//   - Local and federated repositories take a push and a yank, with write
//     or delete permission under gems/; a remote takes neither (405). A
//     push to a virtual repository lands in its default deployment
//     repository, local or federated, with write permission there alone
//     (RTFACT-17787), and without one answers 405 with Allow: GET and
//     7.x's message (decision 0008, R6); a yank through one answers 405
//     [I]. A pushed gem of at most
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
//     SHA-256. A virtual repository renders them from its members in the
//     package handlers' walk, without priorityResolution (JFrog's RubyGems
//     page: "Priority resolution is currently not supported for RubyGems
//     virtual repositories"), nested virtual repositories flattened and a
//     cycle skipped (decision 0008, R1, R2): the first member to list a
//     path supplies it (gems.Merger), each row filtered by the caller's
//     Read in its member, never in the virtual repository, which no
//     permission target can name. Its indexes need Read on the root of
//     some member (RequireRoot). A member without the index, one the
//     caller may not read and one whose upstream fails or cannot be
//     reached are passed over (R3), the last with a warning [I]; a
//     virtual gem download tries the members in the same walk.
//   - The download policy, after read permission: a blacked-out local or
//     federated repository refuses its spec index files, quick gemspecs
//     and compact index with the blackout's 404, and a pattern on a spec
//     index file's or gemspec's own path refuses it, as 7.x refuses those
//     stored files; the JSON API and search keep their answers.
//     A virtual repository's own patterns refuse a spec index file or
//     gemspec, and its indexes leave out a blacked-out member and, for a
//     spec index file or gemspec, a member whose patterns reject that
//     path. A pattern on one gem leaves the gem listed. A virtual gem
//     download passes over a member that is blacked out or refuses the
//     gem. A remote's own index reads check only its blackout.
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
//   - ../virtual_test.go: the Merger.
//
// The guard suite test/server/artifacts/gems drives the protocol through
// the whole server: local, federated and virtual reads, push and its
// permissions, refused requests, yank, token authentication, a virtual
// repository's member filtering, nesting, cycles, members passed over, its
// order without priority and a push to its deployment repository, stored
// gems that fail closed,
// the remote's index checks, cache, offline reads and redirects, the
// download policy on local, virtual and remote reads, the stored index
// documents' file headers and revalidation (gems_index_headers_test.go),
// and the gem and Bundler clients installing a pinned rake
// (TestAccRubyClientsInstallPinnedRake, which skips unless gem and bundle
// are installed). The @gems acceptance scenarios drive gem and Bundler
// against all four classes.
package gemshttp
