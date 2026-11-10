// Package artifacts is the artifacts module's root. It holds the module's
// settings, which app derives from the configuration. The module's
// constructor, New(Deps), and its background wiring, the remote sources and
// the garbage collection loop, are planned here too.
//
// # Role
//
// artifacts module, module root. Only app imports a module root; other
// modules reach artifacts through artifacts/api (decision 0044, dependency
// rules). No package of the module imports its root either, so app hands
// each setting to the code that reads it as a value.
//
// # Inputs
//
//   - Settings: app/config's Artifacts accessor builds them from the
//     configuration's download, search, aql_timeout, build, deploy, npm,
//     cran, huggingface, maven, nuget, remote, gc, conda, helm and pypi
//     sections. app adds the keyring that the Terraform backend format
//     opens sealed states with.
//   - Planned: the Deps that app passes to New: the unit of work and the SQL
//     handle, the hot and cold blob stores, the CDN, the work queue, the
//     outbox, the access/api Authorizer and Keys, the artifacts settings and
//     the logger (target layout, lines 779-783).
//
// # Outputs
//
//   - Settings, one field per configured value that the module's code
//     reads, named as that code names it, and the formats' settings.
//   - Planned: a Module with API, Routes, Start and Close (target layout,
//     line 173). Start registers garbage collection with the work queue,
//     and the module wires its own remote sources (lines 764-766). The
//     module's layers are its subpackages.
//
// # References
//
//   - Context map, artifacts row, as
//     https://github.com/weaveplatform/artifactweave/issues/958 records it:
//     owns repositories of every class, items, content, trash, garbage
//     collection, package index, the 49 ecosystems, remote retrieval, virtual
//     resolution, build info, AQL and searches; phases P3 to P10 and P28.
//   - Target layout:
//     docs/research/architecture-options/5-modular-monolith.md, "Full package
//     tree", lines 172-256 (line 173: module.go), "Remote and virtual
//     repositories", lines 635-666, and "Composition root", lines 746-804.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md, "Public
//     types and published api surface" (artifacts/api has no Settings),
//     "Composition and HTTP" (per-module settings are accessor methods on
//     app/config.Config, so the YAML shape and the config validate output
//     do not change), "Dependency rules", rule 7 (only app wires modules),
//     and "Placeholders".
//   - Decision 0009: docs/research/decisions/0009-configuration-as-code.md
//     (the application configuration the settings come from).
//   - Decision 0004:
//     docs/research/decisions/0004-storage-sql-metadata-and-blobs.md.
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md.
//   - Decision 0008: docs/research/decisions/0008-virtual-repositories.md.
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md.
//   - Plan: docs/implementation/plan.md#p9d-modular-monolith-migration,
//     #p3-repositories-generic-artifacts-cli-aql-shape,
//     #p10-virtual-repositories,
//     #p28-existing-ecosystem-and-core-api-completion. P3 to P10 built the
//     module's packages.
//   - https://github.com/weaveplatform/artifactweave/issues/1093 (the
//     settings) and https://github.com/weaveplatform/artifactweave/issues/1094
//     (WEB-20, which adds module.go).
//
// # Status
//
// Implemented P9d (WEB-19): the module settings. The constructor and the
// background wiring do not exist yet; WEB-20 adds them.
package artifacts
