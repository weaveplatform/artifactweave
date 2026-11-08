// Package pypi is the format of PyPI repositories (decision 0037). It
// reads each distribution's core metadata, a wheel's METADATA or an sdist's
// or egg's PKG-INFO, indexes the file as a file of its project's version
// wherever it is stored (decision 0018), with the per-file records the
// simple index, the PEP 658 metadata files and the JSON API are built
// from, and keeps the pypi.* properties. Every write reaches it through the
// item service in the write's own transaction, so a file deployed by twine,
// by a REST deploy or by jf rt u, or landed by a copy, a move or a restore,
// can be installed as soon as the write answers.
//
// A file of a distribution type whose content yields no Name and Version
// is indexed by its pypi.name and pypi.version properties when it carries
// both, as JFrog requires of manual uploads it cannot read; any other file
// is stored but not indexed.
//
// # Role
//
// artifacts module, ecosystem layer: the format rules of the PyPI slice
// (package type pypi). Its Handler plugs into the item service through the
// format hooks (decision 0032); the simple and upload protocol that serves
// what it keeps is the slice's http package (artifacts/ecosystem/pypi/http).
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for PyPI repositories to Handler, and the work
//     queue, which runs every format's reindex through it; and the
//     protocol, which reads the index records (DecodeMetadata), names and
//     versions (Normalize, NormalizeVersion, ValidName, IsPrerelease) and
//     an upload's distribution (ReadDist, UploadPath).
//   - Driven: the format hooks' environment (format.Env), through which
//     Handler reads stored distributions and keeps the package index
//     (artifacts/core/pkgindex), with AQL (artifacts/core/aql) for a
//     reindex; and the generic deploy policy (artifacts/ecosystem/generic),
//     which Handler embeds.
//   - Settings: the server's PyPI options (decision 0037), which reach
//     Handler through ecosystem.Settings.
//
// # Outputs
//
//   - Handler: the hooks that inspect and admit a write (Admit adds the
//     reserved namespaces and the enforcement settings to the generic
//     policy), index a distribution, drop the record of a file being
//     removed, follow pypi.yanked and the identity properties (Annotated),
//     and rebuild the index (Reindex).
//   - The pypi.* properties (PropName and the rest) and each file's
//     Metadata in the package index.
//   - The rules the protocol shares: Dist, ReadDist and ReadStored, Fields
//     and ParseFields, Type, CanonicalPath, UploadPath, IsDistribution,
//     Description and the name and version helpers.
//
// # Invariants
//
//   - A file is indexed in the write's own transaction, so a client can
//     install it as soon as the write answers.
//   - Names are compared in PEP 503 normal form (Normalize), and versions
//     in their normal form (NormalizeVersion).
//   - Statuses and messages a client sees stay byte-identical.
//
// # References
//
//   - Decision 0037: docs/research/decisions/0037-pypi.md ("Projects,
//     versions and the canonical path", "Which files are indexed",
//     "Properties and yanking", "Settings", "Enforce layout and naming
//     normalisation", "Reserved namespaces", "Reindex").
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the format hooks); decision 0018: docs/research/decisions/0018-package-index.md.
//   - docs/operations/pypi-remote.md (the remote cache this format indexes).
//   - JFrog PyPI repositories, as decision 0037 cites it:
//     https://docs.jfrog.com/artifactory/docs/pypi-repositories.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 231 (ecosystem/pypi), line 664 (virtual.go) and line 997 (the
//     mapping row).
//   - Plan: docs/implementation/plan.md#p8-pypi-nuget-helm-terraform-locals,
//     #p9-remote-repositories and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/1032
//
// # Status
//
// Implemented P8 (PyPI local repositories), with remote repositories in
// P9. Staged in P9d. Roadmap: virtual.go (P10), the Merger that merges PyPI
// simple indexes for virtual repositories.
//
// # Testing
//
//   - handler_test.go, admit_test.go and fault_test.go: indexing, the
//     admission rules, yanking, reindex and store faults.
//   - dist_test.go, json_test.go and name_test.go: reading distributions,
//     the index records, and names and versions.
//   - contract_test.go: the format contract suite (artifacts/core/formattest).
package pypi
