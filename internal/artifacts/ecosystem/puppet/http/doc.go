// Package puppethttp serves the Forge v3 module protocol at
// /artifactory/api/puppet/{repoKey}: a module's releases, one release and
// its archive, a module record, an exact-name search, and a local or
// federated repository's paged lists of every release and module it
// hosts, in the JSON the Puppet client's and puppet_forge's Forge clients
// read. Every answer lists only the releases the caller may read, so the
// client resolves dependencies behind the repository's permissions.
// Archive validation and the module rules are the format package's
// (artifacts/ecosystem/puppet); this package answers HTTP with them.
//
// # Role
//
// artifacts module, ecosystem layer: the native Puppet Forge v3 protocol of
// the Puppet slice. It is named puppethttp so that it does not shadow
// net/http. Outside tests, only its own http subtree, the module's http
// adapters and app may import it (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type puppet, local, federated,
//     virtual or remote. The server also calls PuppetAPI's
//     RegisterPuppetMetadataRoutes after the generated routes, which mounts
//     Protocol again behind the generated Forge operations and serves the
//     repository's reindex (POST {repoKey}/reindex) beside it. An archive is
//     published through the generic item routes, not through this protocol;
//     the format's Handler admits it there.
//   - Driven: the router kit (pkgroute.Kit), whose store (pkgroute.Store)
//     the handlers also reach through DB(), and which resolves the
//     repository and the caller, checks permissions, reads the package
//     index, items and blobs, and writes errors; the remote source service
//     (Options.RemoteSources), which fetches an upstream Forge's release
//     pages and archives into the remote cache; the blob store
//     (Options.Blobs), which opens cached release pages; and the format
//     package, whose rules check every name, version, path, manifest and
//     archive. PuppetAPI runs over the item kit
//     (artifacts/adapters/http/itemkit): the second mount's Options come
//     from its blob store and remote source service, and the reindex is its
//     Reindex, which every format shares.
//
// # Outputs
//
//   - Protocol and Options: the routes, the source of each repository class
//     and the Artifactory error profile.
//   - PuppetAPI: the kit wrapper whose RegisterPuppetMetadataRoutes mounts
//     GET of the Forge v3 module and release operations under
//     /artifactory/api/puppet/{repoKey}, and POST
//     /artifactory/api/puppet/{repoKey}/reindex, which refuses any query
//     with 400, answers its text once the reindex has run, and writes
//     errors in the Artifactory envelope.
//   - Answers: one module's release list, the module search and the
//     whole-repository lists, v3/releases without module and v3/modules
//     without query (list.go), each 20 a page unless limit asks for up to
//     100, a larger limit served as 100 (RTFACT-30529), under the Forge's
//     pagination object with first, previous, current and next links
//     before the results (RTFACT-14557); release and module records with
//     the fields of JFrog's reference and 7.x's answers (record.go), a
//     release's metadata being its whole metadata.json with the fields
//     7.x writes first in its order (RTFACT-19130, #1325) [I]; archives.
//     One module's release list leaves out the top-level record fields
//     exclude_fields names, separated by commas or spaces, and its links
//     keep sort_by and exclude_fields after the module (#1327) [I]. One
//     module's release list and the module search take one module_groups,
//     which a Puppet Enterprise client sends on every request, and answer
//     as they do without it: no value narrows them and their links leave it
//     out (#1343) [I]. JSON answers write "<", ">" and "&" as they are, as 7.x does, and every
//     one is marked private, no-store.
//
// # Invariants
//
//   - A release is listed when the caller may read its archive, whatever
//     the include and exclude patterns say of the archive, which only the
//     archive route refuses (R3). A hosted release whose stored
//     index entry or archive does not match fails with 502 rather than
//     being advertised.
//   - The download policy (decision 0032) comes after Read permission. A
//     blacked-out local or federated repository refuses every route but
//     the module search with the blackout 404, a listing, the
//     whole-repository lists included, naming the request's own path
//     before anything is computed (R2a, R9); the search, a native search
//     route, keeps its answers and finds nothing (R2b). The archive route
//     checks the archive's own path before the release is looked up, so it
//     answers the blackout 404, the patterns 404 or a virtual repository's
//     own "No repository found" 404 (R6, R10). A remote's release pages
//     are checked where 7.x caches them, .puppet/<m>/releases.json
//     (RTFACT-13711), and for the blackout alone when they are read to find
//     an archive (R4) or to answer the search, which a blacked-out remote
//     refuses with the blackout 404 for the module folder (R2b).
//   - A hosted or virtual module lists at most 4,096 releases (503 beyond),
//     and a remote accepts no more from its upstream. A whole-repository
//     list reads at most 4,096 modules and 65,536 releases (503 beyond).
//   - The whole-repository lists are served by local and federated
//     repositories. A virtual repository answers them 404 after the
//     caller's read permission, as 7.x does (RTFACT-15072); a remote one
//     keeps the 400 it gave before they were served, since no source shows
//     what a remote lists.
//   - A hosted release's record takes its file_md5 from the archive item,
//     its downloads from the item's download count and its dates from the
//     item's creation and last update, in the Forge's form in UTC, and its
//     tags from metadata.json, as the Forge derives them; its readme,
//     changelog and license are null. A remote relays the upstream's
//     values of those fields, of tags and supported, and of the module's
//     owner gravatar_id and deprecated_at (RTFACT-19130), and answers a
//     value of another type as a hosted release would. A hosted or virtual
//     module record takes its downloads, dates, owner and links from its
//     releases [I]. A remote's relays those of the upstream's own record,
//     v3/modules/<m>, asked without its current release and release list
//     and read under the release pages' authority, and answers 404, and
//     an empty search, when the upstream answers 404 for it (#1326) [I].
//   - A remote builds each Forge query itself (module, limit, offset,
//     sort_by and exclude_fields), never forwards a client's, module_groups
//     included, never
//     follows an upstream next link, and reads at most 16 MiB a page. It
//     asks for 20 releases a page, the Forge's own default (RTFACT-14274),
//     without the fields no record answers, REFERENCE.md's text among
//     them, so that a module whose readme, changelog and license run to
//     hundreds of KB a release still fits (#1300); the Forge serves at
//     most 100 whatever limit is asked (it answered limit=1000 with
//     "limit": 100 on 2026-11-12). What 7.x's remote asks is [I] (#216).
//     Each page starts after the releases the last one held, and a page
//     short of 20 with more after it is accepted only when the upstream's
//     pagination.limit equals what the page held; short for any other
//     reason, it answers 502. A release whose metadata.json is above the
//     1 MiB an archive's may be is invalid upstream metadata. Metadata and archive URLs must stay under the
//     configured Forge origin and path, and an archive enters the cache only
//     once its size, SHA-256, identity and manifest match the release that
//     advertised it. A cache hit rechecks the source's authority and the
//     caller's read permission.
//   - A remote answers a release its upstream's release pages leave out,
//     once they have been read, from the upstream's record of that
//     release, v3/releases/<slug>, as RTFACT-19130 shows a 6.8.6 remote
//     answering a Puppet Enterprise module's release (#1344) [I]. The
//     record is read with the release pages' authority and policy path,
//     without the fields no record answers, and validated as a page's
//     release is; the upstream's 404 for it, or a caller the source
//     denies, answers "Puppet release not found" as before. A listed
//     release never reads it, and the lists and the search still answer
//     from the pages alone. The archive route serves such a release's
//     archive from the address the record names, read with the blackout
//     alone as the pages are for an archive and checked against the
//     record, and a virtual repository answers both the record and the
//     archive from its first remote member, in member order, whose
//     upstream's record holds the release, once every member's pages leave
//     it out (#1369) [I].
//   - A virtual repository resolves its members in order, leaving out a
//     blacked-out member and, outside the search, a remote whose patterns
//     reject its release pages. A version comes from the first member that holds it and whose
//     own policy accepts its archive; when none does, it stays listed from
//     the first member that holds it, and its archive answers "Puppet
//     release not found" (R5). Each archive download selects that member
//     again.
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/testing/puppet.md (connecting the Puppet client, remote and
//     virtual repositories, the remote's bounds and the evidence).
//   - JFrog Puppet repositories:
//     https://docs.jfrog.com/artifactory/docs/puppet-repositories.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 236 (ecosystem/puppet) and line 1017 (the mapping row). The
//     server-root table does not list the root's puppet_routes.go, which
//     joined this package as routes.go.
//   - Plan: docs/implementation/plan.md#p33-automation-and-source-repositories
//     and #p9d-modular-monolith-migration, epics ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943) and WEB
//     (https://github.com/weaveplatform/artifactweave/issues/944).
//   - https://github.com/weaveplatform/artifactweave/issues/784,
//     https://github.com/weaveplatform/artifactweave/issues/1045,
//     https://github.com/weaveplatform/artifactweave/issues/1086,
//     https://github.com/weaveplatform/artifactweave/issues/1089,
//     https://github.com/weaveplatform/artifactweave/issues/1287,
//     https://github.com/weaveplatform/artifactweave/issues/1288,
//     https://github.com/weaveplatform/artifactweave/issues/1300,
//     https://github.com/weaveplatform/artifactweave/issues/1325,
//     https://github.com/weaveplatform/artifactweave/issues/1326,
//     https://github.com/weaveplatform/artifactweave/issues/1327,
//     https://github.com/weaveplatform/artifactweave/issues/1343,
//     https://github.com/weaveplatform/artifactweave/issues/1344 and
//     https://github.com/weaveplatform/artifactweave/issues/1369
//
// # Status
//
// Implemented P9c, which delivered the plan's P33 group (issues #782 to
// #788). Moved here in P9d, which also moved the server root's Forge
// registrar and reindex route here (WEB-17).
//
// # Testing
//
//   - puppet_test.go: hosted routes, invalid routes and queries, remote
//     authority, integrity, pagination and failures, virtual member order,
//     permissions and changes, store failures and late read revocation.
//   - list_test.go: the whole-repository lists' pages, links and limits,
//     read permission, the blackout and patterns, the virtual and remote
//     answers, the one-module list's refusals they leave unchanged, and
//     store failures.
//   - paging_test.go: the one-module list's and the module search's pages,
//     links, limits and refusals, and exclude_fields.
//   - record_test.go: a hosted release's and module's exact records on
//     every route that answers them, directly and through a virtual
//     repository, and a remote's relayed fields and their defaults.
//   - metadata_test.go: a release's whole metadata.json and tags on every
//     route, and an index entry without the document.
//   - remote_module_test.go: a remote's metadata and tags, its module
//     record relayed from the upstream's, the upstream's 404 for it, the
//     queries a remote asks its upstream, and a module record's failures.
//   - remote_paging_test.go: the page sizes a remote asks for and accepts.
//   - remote_release_test.go: a remote's release that its release pages
//     leave out answered from the upstream's record, the query and
//     authority of that read, the upstream's 404 and a denied caller, and
//     the record's failures.
//   - upstream_archive_test.go: the archive of a release only the
//     upstream's record holds, through a remote and a virtual repository,
//     the record read's authority, the archive checked against the record,
//     member order and the members passed over, and the failures.
//   - groups_test.go: module_groups on one module's release list and the
//     module search answering as without it on every repository class, a
//     remote's upstream queries without it, and the refusals it leaves
//     unchanged.
//   - policy_test.go: the blackout on every hosted route and the module
//     search's empty answer under it, a release whose archive the patterns
//     reject kept listed, virtual members and patterns that refuse only an
//     archive, and the policy path and blackout-only lookup of a remote's
//     release pages and search. The route suite test/server/artifacts/puppet
//     checks a remote's policy, the search included, and a virtual
//     repository's over it, through the source boundary.
//   - remote_test.go: upstream document validation, the archive URL and
//     feed boundaries, version order, and a remote archive refused before
//     its source when its authority is missing.
package puppethttp
