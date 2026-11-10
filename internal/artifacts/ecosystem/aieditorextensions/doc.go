// Package aieditorextensions owns VS Code gallery identities and cache
// paths. An extension is <publisher>.<name>, compared without case; a
// remote repository caches each version's gallery assets under
// extensions/<publisher>/<name>/<version>/, one file per asset type, and
// each gallery query's answer under gallery/queries/<SHA-256 of the
// query>.json.
//
// # Role
//
// artifacts module, ecosystem layer: the VS Code gallery identity and path
// rules of the AI editor extensions slice. Its Handler plugs into the item
// service through the format hooks (decision 0032); the slice's http
// package (artifacts/ecosystem/aieditorextensions/http) serves the VS Code
// gallery API through a remote repository's cache.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for aieditorextensions repositories to Handler;
//     and the protocol, which checks identities, segments and versions
//     (Identity, Segment and Version) and builds the cache paths
//     (ExtensionFolder, QueryPath and AssetCachePath).
//   - Driven: the generic deploy policy (artifacts/ecosystem/generic),
//     whose hooks Handler embeds.
//   - Settings: none.
//
// # Outputs
//
//   - Handler: the generic hooks, with Classify, which classes cached
//     gallery queries, manifests and signatures as metadata, whose content
//     can change under the same path, and every other path as unique
//     content.
//   - The identity and path rules: Serves, Identity, Segment, Version,
//     ExtensionFolder, QueryPath and AssetCachePath.
//
// # Invariants
//
//   - A segment, a publisher or a name, is 1 to 128 ASCII letters, digits,
//     hyphens and underscores, starting with a letter or digit, and a name
//     holds no dot. Identities and folders are lower-case.
//   - A version is at most 64 bytes: three numeric parts of at most 10
//     digits each and an optional hyphenated suffix of letters, digits,
//     dots and hyphens, with no "..".
//   - Only the nine gallery asset types AssetCachePath names have a cache
//     path.
//   - Repositories of this format are remote only; the protocol validates
//     every fetched representation before the remote cache publishes it.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories, the only class this format serves); decision 0008:
//     docs/research/decisions/0008-virtual-repositories.md (virtual
//     repositories, which this format does not serve).
//   - docs/implementation/ai-editor-extensions.md (configuring the gallery,
//     the cache paths, the asset checks and the evidence).
//   - JFrog AI Editor Extension repositories:
//     https://docs.jfrog.com/artifactory/docs/ai-editor-extension-repositories.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 232 (ecosystem/aieditorextensions), line 379 (the slice row) and
//     line 997 (the mapping row).
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
//   - path_test.go: identities, segments, versions and cache paths, and the
//     classification of cached gallery metadata.
package aieditorextensions
