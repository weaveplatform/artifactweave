// Package jetbrainspluginshttp serves the JetBrains Marketplace host
// protocol for remote JetBrains Plugins repositories. The IDE sends this
// protocol when idea.plugins.host points at Artifactory's
// api/jetbrainsplugins route: GET and HEAD of compatible updates
// (api/search/updates/compatible), catalog search (api/search/plugins),
// the plugin ID lists (files/pluginsXMLIds.json and
// files/jbPluginsXMLIds.json), an update's metadata
// (files/<plugin>/<update>/meta.json) and a plugin archive
// (pluginManager), each read through the remote cache and checked before
// it is cached.
//
// # Role
//
// artifacts module, ecosystem layer: the native JetBrains Marketplace
// protocol of the JetBrains plugins slice, which is remote only and has no
// format package. It is named jetbrainspluginshttp so that it does not
// shadow net/http. Outside tests, only its own http subtree, the module's
// http adapters and app may import it (decision 0044, dependency rules);
// the server root does until the router composition moves.
//
// # Inputs
//
//   - Driving: the server (migration/app/router.go), which mounts
//     Protocol through the router kit (pkgroute) at
//     /artifactory/api/jetbrainsplugins/{repoKey} for repositories of
//     package type jetbrainsplugins, with the remote source service.
//   - Driven: the router kit (pkgroute.Kit), which resolves the repository
//     and the caller, checks read permission, reads through the source,
//     serves the result and writes errors; and the remote source service,
//     which fetches and caches the upstream Marketplace's metadata and
//     archives under the source policy.
//   - Settings: none. The repository's url names the upstream Marketplace,
//     normally https://plugins.jetbrains.com.
//
// # Outputs
//
//   - Protocol: the GET and HEAD routes, a source for the remote class
//     only, and the Artifactory error profile.
//   - Answers: the upstream's metadata as cached, and a plugin archive
//     whose Content-Disposition file name is derived from the plugin ID,
//     never from an upstream header or URL.
//
// # Invariants
//
//   - A request is bounded and checked before anything is fetched: each
//     route takes only its own query keys, each once, with a valid build,
//     plugin ID and numeric IDs; anything else answers 400, and an unknown
//     route 404.
//   - The cache identity of a compatible-update or archive request
//     includes its query, operating system and architecture included, so
//     platforms never share a cached answer; the IDE's uuid is telemetry
//     and is dropped.
//   - Metadata is at most 16 MiB of JSON of the expected shape, and a
//     compatible-update list or update metadata must name the plugin or
//     update the request asked for. An archive is at most 1 GiB, read to
//     the end of every entry (4 GiB expanded, 20,000 entries), and its
//     META-INF/plugin.xml, at the top level or in a lib/ JAR, must name
//     the requested plugin with a valid name, version and build range.
//   - Redirects go only to Marketplace file paths on the upstream's own
//     origin, or from plugins.jetbrains.com to its download host,
//     downloads.marketplace.jetbrains.com.
//   - The IDE sends no credentials to a replacement Marketplace host, so
//     the repository's ordinary anonymous read permission governs access.
//   - The source variants (jetbrains-metadata-v1, jetbrains-archive-v1)
//     and cache paths are part of the remote cache key, so they stay
//     byte-identical.
//
// # References
//
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md
//     (JetBrains Plugins is remote only, and its reads cross the shared
//     source boundary and cache); decision 0032:
//     docs/research/decisions/0032-package-format-handlers.md (the router
//     kit and format handlers).
//   - docs/implementation/jetbrains-plugins.md (configuring the IDE,
//     anonymous access, the IDE protocol and the native evidence) and
//     docs/client-recipes/jetbrainsplugins.md (the client recipe).
//   - JFrog JetBrains plugins repositories:
//     https://docs.jfrog.com/artifactory/docs/jetbrains-plugins-repositories.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 234 (ecosystem/jetbrainsplugins), line 379 (the slice row) and
//     line 1025 (the mapping row).
//   - Plan: docs/implementation/plan.md#p31-native-build-and-plugin-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/774 and
//     https://github.com/weaveplatform/artifactweave/issues/1044
//
// # Status
//
// Implemented P9c, which delivered the plan's P31 group (issues #770 to
// #775). Staged in P9d.
//
// # Testing
//
//   - request_test.go: the IDE request grammar and cache identity,
//     architecture and platform separation, unbounded or ambiguous
//     requests, cache keys for opaque plugin IDs, and the redirect guard.
//   - metadata_test.go: compatible updates bound to the requested plugin,
//     update metadata, the plugin ID catalogs, size limits, search results
//     and build wildcards.
//   - archive_test.go: the descriptor bound to the requested plugin,
//     unsafe, traversing and corrupt entries, nested lib/ JARs,
//     cancellation and spooling.
//   - route_test.go (external): the remote install flow cold, warm and
//     offline, distinct platform archives, wrong archive identity and
//     missing read grants, upstream failure and refresh, refused writes,
//     safe file names, legacy IDs, encoded slashes, cache deletion, and a
//     pinned IDE installing through the host.
//
// The protocol's native IDE evidence is the @jetbrainsplugins acceptance
// partition (test/acceptance/features/jetbrainsplugins.feature).
package jetbrainspluginshttp
