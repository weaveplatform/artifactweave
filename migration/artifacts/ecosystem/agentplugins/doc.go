// Package agentplugins validates and indexes local Agent Plugin ZIP bundles
// and renders their marketplace files. A plugin version is one bundle,
// stored at <name>/<version>/<name>-<version>.zip, that carries at least one
// harness manifest (.claude-plugin/plugin.json, .cursor-plugin/plugin.json
// or .codex-plugin/plugin.json); the path, not the manifest, names the
// plugin and its version. The marketplace files list each plugin's newest
// version the reader may read, and are rendered for every request rather
// than stored.
//
// # Role
//
// artifacts module, ecosystem layer: the Agent Plugins bundle admission,
// manifest index and marketplace files of the Agent Plugins slice. Its
// Handler plugs into the item service through the format hooks (decision
// 0032). The slice has no protocol package yet: the root file
// internal/server/agentplugins.go serves the marketplace files with
// Marketplace.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for agentplugins repositories to Handler, and the
//     work queue, which runs every format's reindex through it; and the
//     server's root file agentplugins.go, which serves the marketplace files
//     at /artifactory/api/agentplugins/{repoKey}/{marketplace} and, through
//     the item routes, at the repository root (IsMarketplace,
//     IsReservedMarketplace, Marketplace).
//   - Driven: the format hooks' environment (format.Env): its blobs hold the
//     bundles, and its store holds the items, their properties and the
//     package index (artifacts/core/pkgindex), which Marketplace lists. The
//     generic blackout and include and exclude checks
//     (artifacts/ecosystem/generic) run after the format's own.
//   - Settings: none.
//
// # Outputs
//
//   - Handler: the hooks that read a bundle once before its transaction
//     (Inspect), admit only a bundle with a harness manifest at a canonical
//     path and keep uploads off the marketplace names (Admit), index a
//     version and set its properties once its bundle is stored (Deployed)
//     and rebuild the index from the stored bundles (Reindex). Deletes need
//     no hook: the item service prunes a version whose bundle is gone.
//   - The package index: a package per plugin name, with a version per
//     stored bundle, whose metadata is the bundle's Metadata as JSON: its
//     harnesses and their manifests, its path and its checksums.
//   - The agentplugins.name, agentplugins.version, agentplugins.type (the
//     harnesses), agentplugins.sha256 and agentplugins.sha1 item properties
//     on each bundle.
//   - Marketplace: the JSON of one marketplace file for one reader.
//   - The path and bundle rules: ValidName, ValidVersion, Path, ParsePath,
//     ReadArchive, Metadata, ErrArchive, ErrMarketplace and the bundle
//     bounds.
//
// # Invariants
//
//   - A bundle is read to its end, every member's CRC and size included,
//     within fixed bounds (64 MiB stored and inflated, 512 entries and a
//     1 MiB manifest), with no link and no duplicate, absolute or traversing
//     path.
//   - The path names the plugin and its version, even when a manifest says
//     otherwise.
//   - Only harness bundles are indexed. The 7.161.32 target has no root-only
//     Agent Plugins Standard bundle and no VS Code marketplace, which later
//     JFrog releases added; vscode-marketplace.json stays reserved.
//   - A marketplace file is rendered for each reader and lists, for each
//     plugin, the newest version that reader may read and the repository's
//     patterns allow, so a narrower read grant never discloses a hidden
//     bundle. The unified file advertises ZIP sources, and Claude's file URL
//     sources.
//   - The property names are stored, so they stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks).
//   - docs/research/33-package-formats.md, "Agent Plugins" (the contract,
//     the marketplace files and the 7.161.32 version boundary), and
//     docs/testing/phase9b-signoff.md (the qualification ledger).
//   - JFrog Agent Plugins repositories:
//     https://docs.jfrog.com/artifactory/docs/agent-plugins-repositories.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 232 (ecosystem/agentplugins), line 379 (the slice row), line 405
//     (the root agentplugins.go, bound for ecosystem/agentplugins/http) and
//     line 997 (the mapping row).
//   - Plan: docs/implementation/plan.md#p9b-newly-identified-artifactory-formats
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/690,
//     https://github.com/weaveplatform/artifactweave/issues/691 and
//     https://github.com/weaveplatform/artifactweave/issues/1046
//
// # Status
//
// Implemented P9b (newly identified Artifactory formats). Staged in P9d.
//
// # Testing
//
//   - archive_test.go: bundle reading and paths, invalid bundles, and the
//     bounds and integrity checks.
//   - handler_test.go: invalid bundles and repository policies at
//     admission, a missing blob, and a reindex fault.
//   - marketplace_test.go: the newest readable version and permission
//     filtering.
package agentplugins
