// Package agentpackages validates and indexes APM Agent Packages ZIP
// archives. A package version is one archive, stored at
// <owner>/<name>/<name>-<version>.zip, that carries its apm.yml descriptor
// at the root, or the apm.lock.yaml fallback of a manually supplied archive.
// The path names the package owner/name and its version, and a descriptor
// that names another package or version is refused. The item tree remains
// the source of truth.
//
// # Role
//
// artifacts module, ecosystem layer: the APM Agent Packages archive
// admission and index of the Agent Packages slice. Its Handler plugs into
// the item service through the format hooks (decision 0032), so a version
// published through APM and one written through the item routes meet the
// same checks; the slice's http package
// (artifacts/ecosystem/agentpackages/http) serves the APM v1 registry
// protocol over what it keeps.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for agentpackages repositories to Handler, and
//     the work queue, which runs every format's reindex through it; and the
//     protocol, which checks identities and versions (ValidComponent,
//     ValidVersion), builds archive paths (Path), bounds an upload
//     (MaxArchiveBytes) and decodes the version metadata the index holds
//     (Metadata).
//   - Driven: the format hooks' environment (format.Env): its blobs hold the
//     archives, and its store holds the items, their properties and the
//     package index (artifacts/core/pkgindex). The generic blackout and
//     include and exclude checks (artifacts/ecosystem/generic) run after the
//     format's own. YAML (gopkg.in/yaml.v3) decodes the descriptors.
//   - Settings: none.
//
// # Outputs
//
//   - Handler: the hooks that read an archive once before its transaction
//     (Inspect), admit only an archive at a canonical path whose descriptor
//     agrees with it (Admit), refuse a second write of a published version
//     (GuardWrite), index a version and set its properties once its archive
//     is stored (Deployed) and rebuild the index from the stored archives
//     (Reindex). Deletes need no hook: the item service prunes a version
//     whose archive is gone.
//   - The package index: a package per owner/name, with a version per stored
//     archive, whose metadata is the archive's Metadata as JSON: its
//     description, tags and primitive types (the folders under .apm/), its
//     path, SHA-256 and size.
//   - The agentpackages.name, agentpackages.version, agentpackages.sha256
//     and agentpackages.description item properties on each archive.
//   - The path and archive rules: ValidComponent, ValidVersion, Path,
//     ParsePath, ReadArchive, Metadata, ErrArchive and the archive bounds.
//
// # Invariants
//
//   - An archive is read to its end, every member's CRC and size included,
//     within fixed bounds (64 MiB stored and inflated, 512 entries and a
//     1 MiB descriptor), with no link and no duplicate, absolute or
//     traversing path.
//   - apm.yml takes precedence over apm.lock.yaml; its name, when given,
//     must be the package's name or owner/name, and its version the path's.
//   - A published version is immutable through APM and the item routes
//     alike (409); a restore bypasses the guard and reindexes the saved
//     bytes.
//   - The property names are stored, so they stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks and the write guard).
//   - docs/operations/agent-packages.md (connecting APM, the archive rules,
//     the API and its permissions), and docs/research/33-package-formats.md,
//     "Agent Packages" (the contract and its open questions).
//   - JFrog Agent Packages repositories:
//     https://docs.jfrog.com/artifactory/docs/agent-packages-repositories.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 232 (ecosystem/agentpackages), line 379 (the slice row) and line
//     997 (the mapping row).
//   - Plan: docs/implementation/plan.md#p9b-newly-identified-artifactory-formats
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/692 and
//     https://github.com/weaveplatform/artifactweave/issues/1046
//
// # Status
//
// Implemented P9b (newly identified Artifactory formats). Staged in P9d.
//
// # Testing
//
//   - archive_test.go: archive reading and the canonical path, and unsafe
//     or corrupt archives.
//   - handler_test.go: admission and the hooks' no-content branches.
package agentpackages
