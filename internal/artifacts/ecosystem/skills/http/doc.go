// Package skillshttp serves the local ClawHub-compatible Skills publish API,
// and the discovery, list, search, resolve and download reads the JFrog
// CLI's agent skills commands use, on Artifactory's
// /artifactory/api/skills/{repo} prefix. The bundle rules and the index are
// the format package's (artifacts/ecosystem/skills); this package builds
// bundles from published files and answers HTTP with them.
//
// # Role
//
// artifacts module, ecosystem layer: the native ClawHub-compatible Skills
// API of the Skills slice. It is named skillshttp so that it does not
// shadow net/http. Outside tests, only its own http subtree, the module's
// http adapters and app may import it (decision 0044, dependency rule 9).
//
// # Inputs
//
//   - Driving: the server, which mounts Protocol through the router kit
//     (pkgroute) for repositories of package type skills.
//   - Driven: the router kit (pkgroute.Kit), which resolves the repository
//     and the caller, checks permissions, stores a published bundle through
//     the item service, where the format's hooks admit and index it, reads
//     the package index, items and blobs, serves items and writes errors;
//     the caller's principal (the kernel), which every route but discovery
//     requires to be authenticated; and the format package, whose rules
//     check every slug, version and path and whose Metadata the reads decode
//     from the index.
//
// # Outputs
//
//   - Protocol: the routes, the local source and the Artifactory error
//     profile.
//   - Answers: the discovery document (.well-known/clawhub.json); the skill
//     list (api/v1/skills), a skill's detail (api/v1/skills/{slug}), its
//     versions (.../versions) and one version, or the latest, with its
//     download URL (.../versions/{version}); one file of a version's bundle
//     (api/v1/skills/{slug}/file); search hits (api/v1/search); the version
//     a fingerprint names (api/v1/resolve); the bundle (api/v1/download);
//     a stored bundle's scan status, NOT_IN_ENTITLEMENT (xrayStatus); and a
//     publish's slug, version and fingerprint (201, POST api/v1/skills).
//
// # Invariants
//
//   - Only discovery is anonymous; every other route requires an
//     authenticated caller. List and search take the read grant on the
//     repository, and the scan status the grant on the bundle's path; the
//     other reads take the grant on the skill's folder, and a single-file
//     read the bundle's too. Every version is filtered by the caller's read
//     grant on its bundle path, so a broad folder grant never reveals a
//     version a narrower rule denies.
//   - A publish takes the write grant on the repository and on the bundle's
//     path, and the delete grant too to overwrite a version. It is
//     multipart/form-data (415) with one JSON payload part of at most
//     64 KiB (413) naming a valid slug and version, with bounded display
//     fields (400), and one or more files[] parts with distinct, safe
//     relative names (400) within the format's bounds (413). The files are
//     stored as a reproducible ZIP, sorted by name, uncompressed, with fixed
//     times and modes, so the same files give the same fingerprint.
//   - Lists and versions take a limit from 1 to 200, 200 by default, and
//     an opaque cursor; search takes a query of 1 to 256 bytes, matched
//     without case against slugs, display names, summaries and tags, a
//     limit clamped to 1 to 100, 20 by default, and an offset of at most
//     1,000,000 (400 for any other value).
//   - A single-file read takes a safe relative path (400) and serves only a
//     regular entry of exactly that name from the stored bundle (404
//     otherwise), read within the format's bound.
//   - Artifactory's download policy, after the caller's read permission: a
//     bundle's download, and a single-file read of it, from a blacked-out
//     repository or at a bundle path the include and exclude patterns
//     reject, answers 404 with the item routes' message. A blacked-out
//     repository's detail, versions, version and resolve answer the
//     blackout's 404, naming the skill's folder, before the index is read.
//     The list and search keep their answers, the patterns leave a version
//     listed, and the scan status answers for a stored bundle whatever the
//     policy says.
//   - Statuses and error texts stay byte-identical.
//
// # References
//
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (the router kit and format handlers).
//   - docs/testing/skills.md (the publish operation, the reads, their
//     permissions and the evidence), docs/research/33-package-formats.md,
//     "Skills" (the documented API and the pinned client's wire shapes,
//     which the answers reconcile), and docs/client-recipes/skills.md (the
//     client recipe).
//   - JFrog Skills repositories:
//     https://docs.jfrog.com/artifactory/docs/skills-repositories.
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules".
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 234 (ecosystem/skills), line 1017 (the mapping row) and lines
//     894 to 897 (thin http slices such as this one, a coverage risk).
//   - Plan: docs/implementation/plan.md#p9b-newly-identified-artifactory-formats
//     and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/688,
//     https://github.com/weaveplatform/artifactweave/issues/689 and
//     https://github.com/weaveplatform/artifactweave/issues/1051
//
// # Status
//
// Implemented P9b (newly identified Artifactory formats). Moved here in P9d.
//
// # Testing
//
//   - skills_test.go (external, skillshttp_test): a published bundle
//     indexed canonically and rebuilt, denied and malformed publishes, the
//     index following delete, restore and copy, and direct bundle deploys
//     admitted by the same format rules.
//   - reads_test.go (external): reads that respect version permissions,
//     pagination, single files and the lifecycle, version and search
//     answers, stored-bundle faults on a single-file read, and the download
//     policy on every read.
//   - publish_fault_test.go: multipart faults, malformed file
//     dispositions, and the file count and total size caps.
//
// The @skills acceptance scenario drives the JFrog CLI's agent skills
// commands to publish, list, search, install and delete a skill.
package skillshttp
