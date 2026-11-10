// Package mavenhttp serves Maven's REST operations over the item kit:
// calculateMetadata, which queues the maven-metadata.xml rebuild of a
// folder and, unless noneRecursive is true, of everything under it; and
// generatePom, which writes the POM of a deployed jar through the item
// service. The four package types of the Maven group (maven, gradle, ivy
// and sbt) share them.
//
// # Role
//
// artifacts module, ecosystem maven, HTTP layer: the Maven slice's REST
// handlers, beside the format rules of artifacts/ecosystem/maven. It is
// named mavenhttp so that it does not shadow net/http. Outside tests, only
// its own http subtree, the module's http adapters and app may import it
// (decision 0044, dependency rule 9); the server root counts as app until
// it moves there.
//
// # Inputs
//
//   - Driving: the router, which calls MavenAPI's RegisterMavenRoutes before
//     it stubs the spec's hand-written operations, so that the two
//     operations claim the patterns the spec derives and no 501 stub is
//     registered under them (decision 0036).
//   - Driven: the item kit (artifacts/adapters/http/itemkit), which resolves
//     the repository and the caller's access, reads items, builds the item
//     service and writes errors; the item service (artifacts/core/item),
//     which deploys a generated POM with its checks, index and metadata
//     queue; and the format package (artifacts/ecosystem/maven), whose
//     Serves, CalculateMetadata and GeneratePOM decide the Maven group,
//     queue the metadata and build the POM.
//
// # Outputs
//
//   - MavenAPI: the kit wrapper whose RegisterMavenRoutes mounts POST
//     /artifactory/api/maven/calculateMetadata/{repoKey}/{folderPath} and
//     POST /artifactory/api/maven/generatePom/{repoKey}/{jarFilePath}. Both
//     answer 200 with an empty body: calculateMetadata once the rebuild is
//     queued, and generatePom once the POM is written, or when it is
//     already there.
//
// # Invariants
//
//   - calculateMetadata needs Manage, Delete/Overwrite, Deploy/Cache,
//     Annotate and Read on the folder, as JFrog's REST reference lists them
//     from 4.8 (research 27 section 9); Manage alone is not enough.
//   - generatePom needs Read on the jar, whose content the POM may come
//     from, and Deploy where the POM goes, which is what writing it by hand
//     would take; it never replaces a POM that is there.
//   - A missing path, or a repository that is not local, answers 404, and a
//     repository outside the Maven group 400, as Artifactory answers
//     (research 27 section 9).
//   - Statuses and error and log text stay byte-identical.
//
// # References
//
//   - Decision 0036: docs/research/decisions/0036-maven.md, and research 27
//     (docs/research/27-maven.md), section 9.
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (format routes and hooks).
//   - JFrog's REST reference for Maven:
//     https://docs.jfrog.com/artifactory/reference/calculatemavenmetadata.md
//     and https://docs.jfrog.com/artifactory/reference/generatemavenpomfile.md.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 45 (the ecosystem http rule), line 228 (ecosystem/maven) and
//     line 401 (the root's maven.go).
//   - Plan: docs/implementation/plan.md#p7-npm-go-maven-locals and
//     #p9d-modular-monolith-migration, epic WEB
//     (https://github.com/weaveplatform/artifactweave/issues/944).
//   - https://github.com/weaveplatform/artifactweave/issues/282 and
//     https://github.com/weaveplatform/artifactweave/issues/1086
//
// # Status
//
// Implemented P7 (issue #282), as the server root's maven.go. P9d moved it
// here (WEB-17).
//
// # Testing
//
// The package has no tests of its own, as the root file had none. The
// route suite test/server/artifacts/maven (maven_rest_test.go, and the
// status and matrix tables) drives both operations through the router;
// the format's rest_test.go covers CalculateMetadata and GeneratePOM; and
// the @maven acceptance partition (test/acceptance/features/maven.feature)
// calls them as a client does.
package mavenhttp
