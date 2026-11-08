// Package generic is the format of generic repositories: files without
// package metadata, so nothing is indexed. Every other format embeds its
// Handler for the deploy policy that applies to every package type
// (decision 0032).
//
// # Role
//
// artifacts module, ecosystem layer: the deploy and download policy that
// every format embeds (includesPattern, excludesPattern and blackedOut on a
// write, CheckDownload on a read). It has no http package: the item routes
// serve generic content.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands
//     generic repositories to Handler; every other format, which embeds
//     Handler or calls CheckBlackedOut and CheckPatterns from its own
//     Admit; the item service and core remote, which fall back to Handler
//     when no format registry is set; and the download paths that
//     call CheckDownload before they look for an item: the source boundary
//     (artifacts/core/remote/source), the item and protocol routes, and the
//     application's remote wiring.
//   - The repository configuration (repo.Config): its blackedOut flag and
//     its include and exclude patterns (repo.PatternsOf).
//
// # Outputs
//
//   - Handler: the format.Handler of generic content, whose hooks do
//     nothing and whose Admit applies the deploy policy; Classify reports
//     every file as unique content.
//   - CheckBlackedOut, CheckPatterns and CheckDownload: the refusals, as
//     format.RejectionError values with Artifactory's statuses and
//     messages.
//
// # Invariants
//
//   - A download is refused before any lookup, whether or not the item
//     exists and for an administrator too, as Artifactory does.
//   - The refusal statuses and messages are Artifactory's
//     (BlackedOutException, IncludeExcludeException and the virtual
//     repository's "No repository found"), so they stay byte-identical.
//   - The package imports only the format contract, the repository
//     configuration and shared types, never core remote or the source
//     boundary, so those may import it without a cycle (decision 0044).
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md,
//     "Generic deploy policy" and "Generic download policy" (lines 112 to
//     118), the latter amended for
//     https://github.com/weaveplatform/artifactweave/issues/328.
//   - Decision 0003: docs/research/decisions/0003-repository-model.md
//     (include and exclude patterns).
//   - Decision 0006: docs/research/decisions/0006-authorization-native-permission-model.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Artifacts and remote" (core/remote/source keeps calling
//     CheckDownload).
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 226 (ecosystem/generic), line 386 and line 1000 (the mapping
//     row).
//   - Plan: docs/implementation/plan.md#p3-repositories-generic-artifacts-cli-aql-shape
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/1030
//
// # Status
//
// Implemented P3 (generic artifacts), with the download policy amended for
// https://github.com/weaveplatform/artifactweave/issues/328 (decision
// 0032). Staged in P9d.
//
// # Testing
//
//   - generic_test.go: the deploy policy through Admit, the hooks doing
//     nothing, and CheckDownload for blacked-out, pattern-refused, folder,
//     root and virtual requests.
//   - contract_test.go: the format contract suite (artifacts/core/formattest).
package generic
