// Package skills indexes the canonical ZIP bundles of local Skills
// repositories. A skill version's bundle is stored at
// <slug>/<version>/<slug>-<version>.zip, and the path and the bundle's
// SHA-256 fingerprint are its identity; a SKILL.md in the bundle, at its
// root or in a folder, can add metadata from its YAML front matter, and a
// bundle without one is still indexed. Once a bundle is stored, its
// metadata is recorded as skill.* item properties and its version is
// indexed under its slug, which the ClawHub-compatible API answers from.
//
// # Role
//
// artifacts module, ecosystem layer: the Skills bundle index of the Skills
// slice. Its Handler plugs into the item service through the format hooks
// (decision 0032), so every write in a Skills repository reaches it
// whichever API carried it; the slice's http package
// (artifacts/ecosystem/skills/http) builds bundles from published files and
// serves the Skills API over the index it keeps.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for skills repositories to Handler, and the work
//     queue, which runs every format's reindex through it; and the
//     protocol, which checks slugs and versions (ValidSlug and
//     ValidVersion), builds and parses bundle paths (Path and ParsePath),
//     bounds what it reads and builds by the bundle limits, and decodes the
//     indexed Metadata.
//   - Driven: the format hooks' environment (format.Env): its blobs hold
//     the bundles, and its store holds the items, their properties and the
//     package index (artifacts/core/pkgindex). The generic deploy policy
//     (artifacts/ecosystem/generic), which Handler embeds, is the last
//     check Admit applies.
//   - Settings: none.
//
// # Outputs
//
//   - Handler: the hooks that read a bundle once before its transaction
//     (Inspect), admit a ZIP only at a canonical bundle path and when it
//     reads as valid (Admit), record a stored bundle's properties and index
//     its version (Deployed), and read every stored bundle again to rebuild
//     the index (Reindex). Deletes need nothing more: a bundle is its
//     version's only file, and the item service prunes a version left
//     without files.
//   - The package index: a package per slug, with a version per skill
//     version, whose metadata is the bundle's Metadata as JSON and whose
//     only file is the bundle.
//   - The skill.* item properties on each stored bundle: name, version and
//     fingerprint, and the description, author, tags, display name, summary
//     and changelog it has.
//   - The rules the protocol shares: Metadata, ReadArchive, ValidSlug,
//     ValidVersion, Path, ParsePath, MaxArchiveBytes, MaxSkillBytes,
//     MaxFiles and ErrArchive.
//
// # Invariants
//
//   - A bundle is read within fixed bounds (32 MiB stored and expanded, 256
//     entries and a 5 MiB SKILL.md) without extracting it. Its entries are
//     regular files and folders, with no link, special file or duplicate,
//     and no absolute, non-canonical or traversing path or one holding a
//     backslash or NUL, so no path outside the skill can reach a later
//     single-file read. Every file entry is read through to its end, so a
//     corrupt one is refused.
//   - The SKILL.md nearest the root, the first by name among equals, gives
//     the metadata: its front matter's fields, overridden by those under
//     metadata.skills, metadata.clawdbot and metadata.clawdis in that order,
//     each text field within 4,096 bytes and at most 64 tags of 256 bytes.
//   - The path and the stored content are authoritative: a ZIP is admitted
//     only at the Path of a valid slug and version (409), and the slug,
//     version and fingerprint always come from them, never from SKILL.md.
//     The free-text values and tags are truncated to the store's property
//     value limit, and tags are trimmed, sorted and deduplicated.
//   - The items keep the skill.* property names, so they never change.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks); decision 0007:
//     docs/research/decisions/0007-remote-repositories.md (remote
//     repositories, which this format does not serve: Skills repositories
//     are local only).
//   - docs/testing/skills.md (the publish operation, the bundle checks and
//     limits, the index's lifecycle and the evidence), and
//     docs/research/33-package-formats.md, "Skills" (the documented API and
//     the pinned client's wire shapes).
//   - JFrog Skills repositories:
//     https://docs.jfrog.com/artifactory/docs/skills-repositories.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 234 (ecosystem/skills), line 379 (the slice row) and line 997
//     (the mapping row).
//   - Plan: docs/implementation/plan.md#p9b-newly-identified-artifactory-formats
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/688 and
//     https://github.com/weaveplatform/artifactweave/issues/1051
//
// # Status
//
// Implemented P9b (newly identified Artifactory formats). Moved here in P9d.
//
// # Testing
//
//   - archive_test.go: metadata and safe paths, truncated and oversized
//     metadata, a bundle without SKILL.md accepted but corrupt entries
//     refused, the nearest SKILL.md and nested metadata, the supported
//     metadata blocks within their bounds, and canonical paths and
//     versions.
//
// The hooks' indexing, properties and lifecycle are tested through the
// protocol (artifacts/ecosystem/skills/http) and the @skills acceptance
// scenario.
package skills
