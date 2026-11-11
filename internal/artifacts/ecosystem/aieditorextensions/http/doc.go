// Package aieditorextensionshttp serves the VS Code gallery API for
// read-only remote extension repositories, on Artifactory's
// /artifactory/api/aieditorextensions/{repo} prefix: the gallery's
// extension query and the install assets it names. The IDE has no
// header-authentication channel for this gallery; repository policy still
// controls anonymous read access. The identity and path rules are the
// format package's (artifacts/ecosystem/aieditorextensions); this package
// answers HTTP with them.
//
// # Role
//
// artifacts module, ecosystem layer: the native VS Code gallery API of the
// AI editor extensions slice. It is named aieditorextensionshttp so that it
// does not shadow net/http. Outside tests, only its own http subtree, the
// module's http adapters and app may import it (decision 0044, dependency
// rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type aieditorextensions.
//   - Driven: the router kit (pkgroute.Kit), which resolves the repository
//     and the caller, checks permissions, reads through the remote source
//     and serves results and errors; the remote source service, which
//     fetches the upstream gallery's query answers and assets into the
//     remote cache; the request's base URL (platform/httpkit/baseurl), onto
//     which asset URLs are rewritten; and the format package, whose rules
//     check every identity and build every cache path.
//
// # Outputs
//
//   - Protocol: the routes, the remote source and the Artifactory error
//     profile.
//   - Answers: the gallery's JSON for an extension query (POST
//     _apis/public/gallery/extensionquery), with only the results the
//     caller may read and their asset URLs rewritten onto this repository's
//     route; and an extension's manifest, VSIX package or signature (GET
//     and HEAD of assets/{publisher}/{name}/{version}/{assetType}), served
//     unchanged.
//
// # Invariants
//
//   - Only a remote repository has a source, and only when the server has
//     a remote source service; any other class answers 501.
//   - A query is a JSON body of at most 64 KiB in the gallery's grammar,
//     naming either one extension or a search text, and takes no URL query
//     (400). It is cached under the SHA-256 of its canonical form, so
//     distinct queries never share an answer. An exact extension query is
//     checked against that extension's folder, a search against the
//     repository's, and every result against its own read permission.
//   - An upstream gallery answer is taken only when it validates, within
//     8 MiB of JSON: each extension's publisher, name, version, asset types
//     and asset origins. An asset is fetched only when the gallery lists
//     its version and type, from an approved origin; the VSIX must match
//     the gallery's SHA-256 claim and the signature's package digest the
//     same claim, and both ZIPs are bounded (512 MiB and 1 MiB). Of the
//     asset types the format names, only these three are served (404 for
//     the others); any other is refused (400).
//   - Caller headers and credentials are never sent upstream.
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories).
//   - docs/implementation/ai-editor-extensions.md (configuring VS Code and
//     anonymous access, the query and asset rules, and the evidence), and
//     docs/client-recipes/aieditorextensions.md (the client recipe, whose
//     evidence is the @aieditorextensions scenario and its install
//     script).
//   - JFrog AI Editor Extension repositories:
//     https://docs.jfrog.com/artifactory/docs/ai-editor-extension-repositories.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 232 (ecosystem/aieditorextensions) and line 1017 (the mapping
//     row).
//   - Plan: docs/implementation/plan.md#p31-native-build-and-plugin-repositories
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/775 and
//     https://github.com/weaveplatform/artifactweave/issues/1050
//
// # Status
//
// Implemented P9c, which delivered the plan's P31 group (issues #770 to
// #775). Staged in P9d.
//
// # Testing
//
//   - remote_test.go (external, aieditorextensionshttp_test): queries and
//     install assets under anonymous permissions, an invalid catalog or a
//     mismatched signature that publishes nothing, and malformed client
//     paths with separate query caches.
//   - catalog_test.go: the projection that keeps identities and rewrites
//     asset URLs, and refused origins, identities, checksums and malformed
//     upstream metadata.
//   - query_test.go: the query grammar, cache separation and the asset
//     query whitelist.
//   - validate_test.go: the asset validators, the ZIP path grammar,
//     streamed and malformed signatures, and the asset type and origin
//     grammar.
//
// The @aieditorextensions acceptance scenario drives VS Code installing an
// extension through a remote repository, cold and offline.
package aieditorextensionshttp
