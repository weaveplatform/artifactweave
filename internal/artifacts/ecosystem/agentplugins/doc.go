// Package agentplugins validates and indexes local Agent Plugin ZIP bundles
// and renders their marketplace files. A plugin version is one bundle,
// stored at <name>/<version>/<name>-<version>.zip, that carries at least one
// harness manifest (.claude-plugin/plugin.json, .cursor-plugin/plugin.json
// or .codex-plugin/plugin.json) or a root plugin.json of the Agent Plugins
// Standard 1.0.0; the path, not the manifest, names the plugin and its
// version. The marketplace files list each plugin's newest version the
// reader may read, and are rendered for every request rather than stored.
//
// # Role
//
// artifacts module, ecosystem layer: the Agent Plugins bundle admission,
// manifest index and marketplace files of the Agent Plugins slice. Its
// Handler plugs into the item service through the format hooks (decision
// 0032). Its http subpackage, agentpluginshttp
// (artifacts/ecosystem/agentplugins/http), serves the marketplace files
// with Marketplace.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for agentplugins repositories to Handler, and the
//     work queue, which runs every format's reindex through it; and
//     agentpluginshttp, which serves the marketplace files at
//     /artifactory/api/agentplugins/{repoKey}/{marketplace} and, through
//     the item routes, at the repository root (IsMarketplace,
//     Marketplace).
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
//     (Inspect), admit only a bundle with a harness manifest or a conforming
//     root plugin.json at a canonical path and keep uploads off the
//     marketplace names (Admit), index a
//     version and set its properties once its bundle is stored (Deployed)
//     and rebuild the index from the stored bundles (Reindex). Deletes need
//     no hook: the item service prunes a version whose bundle is gone.
//   - The package index: a package per plugin name, with a version per
//     stored bundle, whose metadata is the bundle's Metadata as JSON: its
//     harnesses and their manifests, its conforming root plugin.json, its
//     path and its checksums.
//   - The agentplugins.name, agentplugins.version, agentplugins.type (the
//     harnesses, and generic, vscode and cursor for the standard layout),
//     agentplugins.sha256 and agentplugins.sha1 item properties on each
//     bundle.
//   - Marketplace: the JSON of one marketplace file for one reader.
//   - The path and bundle rules: ValidName, ValidVersion, Path, ParsePath,
//     ReadArchive, Metadata, StandardSchema, ErrArchive, ErrMarketplace and
//     the bundle bounds.
//
// # Invariants
//
//   - A bundle is read to its end, every member's CRC and size included,
//     within fixed bounds (64 MiB stored and inflated, 512 entries and a
//     1 MiB manifest), with no link and no duplicate, absolute or traversing
//     path.
//   - The path names the plugin and its version, even when a manifest says
//     otherwise.
//   - A bundle is indexed by its harness manifests and by a root
//     plugin.json that conforms to the Agent Plugins Standard 1.0.0, which
//     self-managed 7.173.8 added. A root plugin.json that does not conform
//     indexes nothing, so a harness bundle carrying one keeps its
//     harness-only listing, and a bundle with no other manifest is refused.
//   - vscode-marketplace.json lists the standard layout only, and
//     cursor-marketplace.json adds it to the Cursor manifests; an entry
//     takes its fields from the harness's own manifest first.
//   - A marketplace file is rendered for each reader and lists, for each
//     plugin, the newest version that reader may read, so a narrower read
//     grant never discloses a hidden bundle. A version whose ZIP the
//     repository's patterns reject stays listed, as 7.x's stored file lists
//     each plugin's latest version, and only the ZIP's download refuses.
//     The unified file advertises ZIP sources, and Claude's file URL
//     sources.
//   - The property names are stored, so they stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks).
//   - docs/research/33-package-formats.md, "Agent Plugins" (the contract,
//     the marketplace files and the 7.173.8 version boundary), and
//     docs/testing/phase9b-signoff.md (the qualification ledger).
//   - JFrog Agent Plugins repositories:
//     https://docs.jfrog.com/artifactory/docs/agent-plugins-repositories.
//   - The Agent Plugins Standard 1.0.0 and its manifest schema:
//     https://github.com/agentplugins/agent-plugins-spec (spec/1.0.0.md and
//     schemas/1.0.0/plugin.schema.json).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 232 (ecosystem/agentplugins), line 379 (the slice row), line 405
//     (the root agentplugins.go, now in ecosystem/agentplugins/http) and
//     line 997 (the mapping row).
//   - Plan: docs/implementation/plan.md#p9b-newly-identified-artifactory-formats
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/690,
//     https://github.com/weaveplatform/artifactweave/issues/691,
//     https://github.com/weaveplatform/artifactweave/issues/1046 and
//     https://github.com/weaveplatform/artifactweave/issues/1255
//
// # Status
//
// Implemented P9b (newly identified Artifactory formats). Moved here in P9d.
// The Agent Plugins Standard and vscode-marketplace.json added by issue 1255.
//
// # Testing
//
//   - archive_test.go: bundle reading and paths, invalid bundles, and the
//     bounds and integrity checks.
//   - standard_test.go: which root plugin.json conforms to the standard,
//     the layouts a bundle is indexed by and its agentplugins.type values,
//     and the files and fields each layout is listed with.
//   - handler_test.go: invalid bundles and repository policies at
//     admission, a missing blob, and a reindex fault.
//   - marketplace_test.go: the newest readable version and permission
//     filtering.
package agentplugins
