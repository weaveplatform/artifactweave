// Package maven is the format of maven, gradle, ivy and sbt repositories
// (decision 0036): Maven's naming predicates, the coordinates a repository's
// layout reads from a path, and the package index of decision 0018, kept by
// the hooks on every write path and rebuilt by Reindex.
//
// A package is groupId:artifactId, from the layout's organisation and
// module; a version is the name of its version folder, so every unique
// build of 1.0-SNAPSHOT is a file of that one version; and versions order
// by types.CompareMercury, as Artifactory's metadata and searches do.
//
// # Role
//
// artifacts module, ecosystem layer: the format rules of the Maven slice,
// one format with four layouts (package types maven, gradle, ivy and sbt).
// Its Handler plugs into the item service through the format hooks
// (decision 0032), so every write, download and delete in a repository of
// the Maven group reaches it whichever API carried it. The Maven protocol
// is still the root file internal/server/maven.go, which serves the
// calculateMetadata and generatePom operations with this package.
//
// # Inputs
//
//   - Driving: the format registry (artifacts/ecosystem), which hands the
//     item service's hooks for the Maven group to Handler; the work queue,
//     which runs the metadata rebuilds (KindMetadata, KindPlugins) and the
//     maxUniqueSnapshots cleanup (KindSnapshots) through Handler.Work, the
//     cleanup on the timer the application schedules; the server's Maven
//     operations (CalculateMetadata, GeneratePOM) and upstream version
//     search (ReadSourceMetadata); and the remote policy, which reads an
//     upstream's metadata for version tokens and snapshots (SourceMetadata)
//     and checks an upstream file before it is cached (ValidateRemote).
//   - Driven: the format hooks' environment (format.Env), through which
//     Handler reads stored POMs, keeps the package index, writes the
//     maven-metadata.xml files it derives as Deriver, enqueues its work and
//     removes the snapshot builds the cleanup drops; and the repository's
//     layout (artifacts/core/layout), which reads coordinates from a path.
//
// # Outputs
//
//   - Naming: MetadataFile, Snapshot and SnapshotSuffix, the Is* path
//     predicates (IsPom, IsMetadata, IsSnapshot, IsNonUniqueSnapshot and
//     the rest), IsMavenLayout, UniqueBuild and ParseUniqueBuild.
//   - Coordinates and Read, a file's place in the package index, and
//     Versions, a package's versions in Mercury order with their files.
//   - Handler: the hooks that inspect and admit a deploy, index it, resolve
//     a download's version tokens and non-unique snapshot name, classify
//     paths for replication, mark plugin POMs (PluginProperty) and derive
//     maven-metadata.xml at the snapshot, artifact and plugin levels.
//   - CalculateMetadata and GeneratePOM, the work of the calculateMetadata
//     and generatePom operations.
//   - The upstream rules' building blocks: SourceMetadata with
//     SourceVersioning, SourceSnapshot and SourceSnapshotVersion,
//     ReadSourceMetadata, ValidateRemote and ErrRemoteContent.
//
// # Invariants
//
//   - Admit applies Artifactory's checks in its order: a blacked-out
//     repository, the release and snapshot policy, the include and exclude
//     patterns, then what each kind of write adds, a snapshot's name under
//     the repository's snapshotVersionBehavior included.
//   - maven-metadata.xml is derived from the package index through the
//     work queue, never on the write's path, and a read of it waits for the
//     work it is owed (Owed); under deployer a deploy leaves the client's
//     file, and only a delete has it rewritten.
//   - Snapshot renaming, checksum sidecar mapping and -SNAPSHOT resolution
//     apply only under a layout equal to maven-2-default (IsMavenLayout).
//   - Upstream metadata and POMs are bounded and checked before they are
//     cached, and an invalid one is refused with ErrRemoteContent, which
//     never echoes upstream XML.
//
// # References
//
//   - Decision 0036: docs/research/decisions/0036-maven.md, and research 27
//     (docs/research/27-maven.md).
//   - Decision 0032: docs/research/decisions/0032-package-format-handlers.md
//     (format hooks and optional interfaces); decision 0018:
//     docs/research/decisions/0018-package-index.md; decision 0033:
//     docs/research/decisions/0033-persistent-work-queue.md.
//   - docs/operations/maven-remote-repositories.md (Maven, Gradle, Ivy and
//     SBT remote repositories).
//   - JFrog's statements decision 0036 cites: metadata recalculated after a
//     delete under deployer, and its fix,
//     https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-26651 and
//     https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-26126; plugin
//     metadata, https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-20623.
//   - The Maven repository metadata model:
//     https://maven.apache.org/ref/3-LATEST/maven-repository-metadata/repository-metadata.html.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     line 228 (ecosystem/maven, also gradle, ivy and sbt), line 381 (one
//     format, four layouts), line 401 (the protocol's later home,
//     ecosystem/maven/http), line 664 (the P10 virtual.go) and line 997
//     (the mapping row).
//   - Plan: docs/implementation/plan.md#p7-npm-go-maven-locals,
//     #p9-remote-repositories and #p9d-modular-monolith-migration, epic ECO
//     (https://github.com/weaveplatform/artifactweave/issues/943).
//   - https://github.com/weaveplatform/artifactweave/issues/279 (metadata),
//     https://github.com/weaveplatform/artifactweave/issues/282
//     (calculateMetadata and generatePom),
//     https://github.com/weaveplatform/artifactweave/issues/283
//     (maxUniqueSnapshots), https://github.com/weaveplatform/artifactweave/issues/625
//     (remote retrieval) and
//     https://github.com/weaveplatform/artifactweave/issues/1019
//
// # Status
//
// Implemented P7, with remote retrieval in P9. Staged in P9d. Roadmap:
// virtual.go (P10), the Merger that merges maven-metadata.xml for virtual
// repositories.
//
// # Testing
//
//   - naming_test.go and coordinates_test.go: Maven's naming table, unique
//     builds, the maven-2-default comparison and coordinates.
//   - admit_test.go and snapshot_test.go: POM checks and encodings, the
//     release and snapshot policy, uploaded metadata and checksums, and
//     snapshot naming under each snapshotVersionBehavior.
//   - handler_test.go and contract_test.go: indexing, classification and
//     the layouts, and the format contract suite
//     (artifacts/core/formattest) for maven, gradle, ivy and sbt.
//   - metadata_test.go and metadata_fault_test.go: the three metadata
//     levels against the golden XML in testdata, deployer, concurrent
//     deploys, work and queue failures.
//   - resolve_test.go: version tokens and snapshot resolution across
//     layouts and repositories.
//   - cleanup_test.go: the maxUniqueSnapshots cleanup.
//   - rest_test.go: CalculateMetadata and GeneratePOM.
//   - remote_test.go: upstream metadata validation and the POM and JAR
//     admission of a remote file.
package maven
