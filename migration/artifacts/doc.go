// Package artifacts will hold the artifacts module's constructor, New(Deps),
// and the module's background wiring: its remote sources and the garbage
// collection loop.
//
// # Role
//
// artifacts module, module root. Only app imports a module root; other
// modules reach artifacts through artifacts/api (decision 0044, dependency
// rules).
//
// # Inputs
//
// None yet. Planned: the Deps that app passes to New: the unit of work and
// the SQL handle, the hot and cold blob stores, the CDN, the work queue, the
// outbox, the access/api Authorizer and Keys, the artifacts settings and the
// logger (target layout, lines 779-783).
//
// # Outputs
//
// None yet. Planned: a Module with API, Routes, Start and Close (target
// layout, line 173). Start registers garbage collection with the work queue,
// and the module wires its own remote sources (lines 764-766). The module's
// layers are its subpackages.
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
//     tree", lines 172-256, "Remote and virtual repositories", lines 635-666,
//     and "Composition root", lines 746-804.
//   - Decision 0044:
//     docs/research/decisions/0044-modular-monolith-migration.md,
//     "Placeholders" and "Composition and HTTP".
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
//   - https://github.com/weaveplatform/artifactweave/issues/1094: WEB-20 adds
//     module.go and rewrites this Status.
//
// # Status
//
// Placeholder: implemented by P9d.
package artifacts
