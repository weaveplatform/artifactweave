// Package helm is the format of classic Helm chart repositories (decision
// 0039); Helm OCI repositories are the Docker format's. It reads each chart
// archive's Chart.yaml, indexes every chart in the repository, in any
// folder, as a version of the package its name gives (decision 0018), with
// the per-version Metadata that index.yaml is built from, and sets the
// chart.* properties. Every write reaches it through the item service, so a
// chart deployed by a REST PUT, by checksum, from an archive, or landed by a
// copy, a move or a restore is indexed the same way.
//
// A chart's provenance file, <chart>.prov, is a sidecar the format keeps
// with its chart (format.SidecarKeeper): it follows the chart through a
// delete, a copy, a move and a restore, and is never a version file.
//
// Each write that changes a repository's charts queues the repository's
// index.yaml work, one item per repository (KindIndex).
//
// # Role
//
// artifacts module, ecosystem layer: the format rules of the classic Helm
// slice (package type helm). Its Handler plugs into the item service through
// the format hooks (decision 0032); the repository protocol that serves what
// it keeps is the slice's http package (artifacts/ecosystem/helm/http).
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for Helm repositories to Handler; the persistent
//     work queue, which runs the index.yaml work (KindIndex) and every
//     format's reindex through Handler (decision 0033); and the protocol,
//     which serves the derived index.yaml (IndexPath, Index, EncodeIndex,
//     Settings) and checks a remote repository's index and charts
//     (ReadChart, IsChartPath, IsSemver).
//   - Driven: the format hooks' environment (format.Env), through which
//     Handler reads stored charts, keeps the package index
//     (artifacts/core/pkgindex), queues and derives the index.yaml, and
//     finds chart archives by AQL (artifacts/core/aql) for a reindex; and
//     the generic deploy policy (artifacts/ecosystem/generic), which Handler
//     embeds.
//   - Settings: the server's Helm options (decision 0039, "Configuration"),
//     which reach the protocol through ecosystem.Settings; Handler takes
//     none.
//
// # Outputs
//
//   - Handler: the hooks that inspect and admit a write (Admit adds Enforce
//     Layout to the generic policy), index a chart, hand a version over
//     when its newest chart goes (Deleting), keep the .prov sidecar
//     (Sidecars), keep the root index.yaml out of replication (Classify),
//     derive the index.yaml (Work, Resolve, Owed) and rebuild the package
//     index (Reindex, ReindexPath).
//   - The chart.* properties (PropName, PropVersion, PropAppVersion), each
//     version's Metadata in the package index, and the derived index.yaml
//     at IndexPath.
//   - The rules the protocol shares: Chart and ReadChart, Index,
//     ChartVersion and EncodeIndex, IsChartPath, IsSemver and Settings.
//
// # Invariants
//
//   - A read of the root index.yaml of a local or federated repository
//     waits for the work its writes queued (Owed), so a client never reads
//     an index that lags a completed write.
//   - The stored index.yaml holds relative chart URLs; only the protocol
//     makes them absolute, when Settings turn relative URLs off.
//   - A .prov follows its chart and is never a version file.
//   - The persisted names (the helm.index work kind and the chart.*
//     properties) and every status and message a client sees, Enforce
//     Layout's refusals included, stay byte-identical.
//
// # References
//
//   - Decision 0039: docs/research/decisions/0039-helm.md ("Scope",
//     "index.yaml is per-repository derived work", ".prov is a sidecar the
//     Helm format owns", "Charts and the package index", "The shape of
//     index.yaml", "Enforce Layout", "Reindex", "Configuration").
//   - Decision 0033: docs/research/decisions/0033-persistent-work-queue.md
//     (the index.yaml work); decision 0032:
//     docs/research/decisions/0032-package-format-handlers.md (the format
//     hooks); decision 0018: docs/research/decisions/0018-package-index.md.
//   - docs/operations/helm-remote.md (the remote cache this format indexes).
//   - JFrog Helm chart repositories, which Enforce Layout and the partial
//     reindex follow:
//     https://docs.jfrog.com/artifactory/docs/kubernetes-helm-chart-repositories;
//     and the metadata read rule that decision 0039 cites:
//     https://docs.jfrog.com/artifactory/docs/docker-repositories.md.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 231 (ecosystem/helm), line 664 (virtual.go) and line 997 (the
//     mapping row).
//   - Plan: docs/implementation/plan.md#p8-pypi-nuget-helm-terraform-locals,
//     #p9-remote-repositories and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/1033
//
// # Status
//
// Implemented P8 (Helm local repositories), with remote repositories in P9.
// Moved here in P9d. Roadmap: virtual.go (P10), the Merger that merges the
// Helm index.yaml for virtual repositories.
//
// # Testing
//
//   - handler_test.go and fault_test.go: indexing, duplicates, overwrites,
//     transfers and restores, queueing, the remote cache, Owed, Inspect and
//     store faults.
//   - chart_test.go and enforce_test.go: reading a chart and Enforce Layout.
//   - derived_test.go and reindex_test.go: the derived index.yaml, its
//     encoding and settings, cold reads and parked work, and full and
//     partial reindexes.
//   - contract_test.go: the format contract suite (artifacts/core/formattest).
package helm
