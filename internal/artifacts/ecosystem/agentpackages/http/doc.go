// Package agentpackageshttp serves the APM v1 registry protocol on Agent
// Packages local and federated repositories, at
// /artifactory/api/agentpackages/{repoKey}: a package's versions, a
// version's archive, publishing a version, and JFrog's search extension.
// Federated peer convergence belongs to P13. Archive validation and the
// package rules are the format package's (artifacts/ecosystem/agentpackages);
// this package answers HTTP with them.
//
// # Role
//
// artifacts module, ecosystem layer: the native APM v1 registry protocol of
// the Agent Packages slice. It is named agentpackageshttp so that it does
// not shadow net/http. Outside tests, only its own http subtree, the
// module's http adapters and app may import it (decision 0044, dependency
// rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type agentpackages.
//   - Driven: the router kit (pkgroute.Kit), which resolves the repository
//     and the caller, checks permissions, reads the package index and items,
//     stores a published archive through the item service, where the
//     format's hooks admit it, and writes errors; and the format package,
//     whose rules check every identity, version and path.
//
// # Outputs
//
//   - Protocol: the routes, local and federated repositories as the only
//     classes, and the APM error profile (RFC 7807 problem JSON).
//   - Answers: the readable versions of a package, newest first, with their
//     SHA-256 digests, publication times and sizes; a version's ZIP bytes;
//     201 with the published version's digest; and search results with
//     their total, limit and offset.
//
// # Invariants
//
//   - A version list and a search show only archives the caller may read; a
//     package with no readable version answers 404.
//   - A publish needs write permission on the archive's path, the
//     application/zip media type (415) and at most 64 MiB (413), and never
//     replaces a published version (409).
//   - A search's limit defaults to 20, and one above 100 is refused (400).
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers).
//   - docs/operations/agent-packages.md (connecting APM, the API and its
//     permissions), and docs/research/33-package-formats.md, "Agent
//     Packages" (the version JSON and the search envelope, which awaits a
//     target-instance capture in
//     https://github.com/weaveplatform/artifactweave/issues/216).
//   - JFrog Agent Packages repositories:
//     https://docs.jfrog.com/artifactory/docs/agent-packages-repositories.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 232 (ecosystem/agentpackages), lines 894 to 897 (a thin protocol
//     slice) and line 1017 (the mapping row).
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
// The package has no tests of its own: test/server/artifacts/agentpackages
// drives it through the whole router
// (TestAccAgentPackagesAPMProtocolAndLifecycle), and the @apm acceptance
// scenarios drive it with the native APM client.
package agentpackageshttp
