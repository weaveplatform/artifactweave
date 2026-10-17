# Maven, Gradle, Ivy and SBT

Research baseline: **2026-10-16**.

Phase P7 serves Maven local repositories, and Gradle, Ivy and SBT repositories through the
same handler with their own layouts. [Research 03](03-repository-configuration-model.md)
holds the repository fields and the layout patterns, and
[research 04](04-package-protocols.md#maven-gradle-ivy-and-sbt) sketched the protocol. This
report collects what Artifactory does where those leave a point open: when and how it
writes `maven-metadata.xml`, how it names snapshots, which uploads it checks or discards, and
what its two Maven REST operations do. Decision [0036](decisions/0036-maven.md) records the
choices; decision [0032](decisions/0032-package-format-handlers.md) sets the hooks they use.

Each claim is labelled:

| Label | Means |
|---|---|
| **[D]** | documented at the cited place, read on 2026-10-16 |
| **[S]** | seen in source code at the cited commit |
| **[I]** | inferred from the documented facts; not stated by any source |

Sources:
- **Artifactory OSS 6.20.0**, as mirrored at `github.com/knottnhupfer/artifactory-oss`
  `b99e5649bcad` (the mirror research 22 and 24 use). Paths below are relative to its root;
  the classes are:
  - upload: `backend/core/src/main/java/org/artifactory/engine/UploadServiceImpl.java` and
    `backend/core/src/main/java/org/artifactory/util/UploadServiceUtils.java`;
  - metadata: `backend/core/src/main/java/org/artifactory/maven/` (`MavenMetadataCalculator`,
    `MavenPluginsMetadataCalculator`, `MavenMetadataServiceImpl`, `MavenMetaDataInfo`,
    `versioning/MavenVersionComparator`, `snapshot/BuildNumberSnapshotComparator`) and
    `base/api/src/main/java/org/artifactory/api/maven/MavenMetadataService.java`;
  - interceptors: `backend/core/src/main/java/org/artifactory/repo/interceptor/`
    (`MavenMetadataCalculationInterceptor`, `MavenPomInterceptor`,
    `IntegrationCleanerInterceptor`);
  - snapshots: `backend/core/src/main/java/org/artifactory/repo/snapshot/`
    (`SnapshotVersionAdapterBase`, `UniqueSnapshotVersionAdapter`,
    `NonUniqueSnapshotVersionAdapter`, `DeployerSnapshotVersionAdapter`,
    `SnapshotVersionsRetriever`, `LatestVersionResolver`, `LocalLatestVersionResolver`) and
    `backend/core/src/main/java/org/artifactory/repo/cleanup/IntegrationCleanupServiceImpl.java`;
  - policy: `backend/core/src/main/java/org/artifactory/repo/RealRepoBase.java`,
    `base/api/src/main/java/org/artifactory/api/repo/exception/SnapshotPolicyException.java`,
    `base/common/src/main/java/org/artifactory/maven/PomTargetPathValidator.java` and
    `storage/common/src/main/java/org/artifactory/io/checksum/policy/LocalRepoChecksumPolicy.java`;
  - naming, layouts and models: `base/config/src/main/java/org/artifactory/mime/MavenNaming.java`,
    `base/config/src/main/java/org/artifactory/mime/NamingUtils.java`,
    `base/config/src/main/java/org/artifactory/util/RepoLayoutUtils.java`,
    `base/common/src/main/java/org/artifactory/maven/MavenModelUtils.java` and
    `base/common/src/main/java/org/artifactory/maven/MavenPomBuilder.java`;
  - configuration: `base/config/src/main/java/org/artifactory/common/ConstantValues.java`,
    `base/config/src/main/java/org/artifactory/descriptor/repo/` (`RepoType`,
    `RepoBaseDescriptor`, `LocalRepoDescriptor`, `RealRepoDescriptor`),
    `base/common/src/main/java/org/artifactory/repo/config/RepoConfigDefaultValues.java` and
    `base/common/src/main/java/org/artifactory/repo/LocalRepositoryConfigurationImpl.java`;
  - REST: `web/rest/src/main/java/org/artifactory/rest/resource/maven/MavenResource.java`.
- **JFrog's documentation** under `https://docs.jfrog.com/artifactory/docs/` (read with `.md`
  appended): `local-repositories` (the Maven settings), `maven-repositories` and
  `repository-layouts`; and the cached definitions
  `.specoracle/artifactory-package-type-specific-apis/{calculatemavenmetadata,generatemavenpomfile}.json`.
- **The JFrog Terraform provider** `jfrog/terraform-provider-artifactory` at `990e8dfdb64e`:
  `pkg/artifactory/resource/repository/local/java.go`,
  `pkg/artifactory/provider/framework.go` and
  `pkg/artifactory/resource/repository/{repository,default_repo_layout_map}.go`.
- **The JFrog CLI and its Maven extractor:** jfrog-cli-core `b47892ded3a0` (the commit
  `jf` 2.126.0 pins; `common/build/buildinfoproperties.go`, `common/build/buildutils.go`) and
  `github.com/jfrog/build-info` `4f0d5407279e`
  (`build-info-extractor-maven3/.../BuildInfoRecorder.java`,
  `build-info-extractor/.../clientConfiguration/ClientProperties.java`).
- **Maven's documentation:** the repository metadata model
  (`https://maven.apache.org/ref/3-LATEST/maven-repository-metadata/repository-metadata.html`,
  Maven 3.10.0) and Maven Resolver's configuration
  (`https://maven.apache.org/resolver/configuration.html`).
- **Gradle's documentation:** the 6.0 release notes
  (`https://docs.gradle.org/6.0/release-notes.html`).

Artifactory 6.20 is the last open-source release. Where 7.x may differ, and where no source
shows the behaviour, the claim is **[I]** and is on the live-comparison list for P19
([#216](https://github.com/weaveplatform/artifactweave/issues/216)).

## 1. Layouts and coordinates

### 1.1 Layouts

- **[D]** A layout is five values: `artifactPathPattern`, `distinctiveDescriptorPathPattern`,
  `descriptorPathPattern`, `folderIntegrationRevisionRegExp` and
  `fileIntegrationRevisionRegExp` (`repository-layouts`). Research 03 lists the Maven 2
  patterns ([research 03](03-repository-configuration-model.md#other-enumerations)).
- **[S]** The built-in layouts (`RepoLayoutUtils`):

  | Layout | Artifact pattern | Descriptor | Integration revisions (folder, file) |
  |---|---|---|---|
  | `maven-2-default` | `[orgPath]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]` | same with `.pom` | `SNAPSHOT`; `SNAPSHOT\|(?:(?:[0-9]{8}.[0-9]{6})-(?:[0-9]+))` |
  | `ivy-default` | `[org]/[module]/[baseRev](-[folderItegRev])/[type]s/[module](-[classifier])-[baseRev](-[fileItegRev]).[ext]` | `.../[type]s/ivy-[baseRev](-[fileItegRev]).xml` | `\d{14}`; `\d{14}` |
  | `gradle-default` | `[org]/[module]/[baseRev](-[folderItegRev])/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]` | `[org]/[module]/ivy-[baseRev](-[fileItegRev]).xml` | `\d{14}`; `\d{14}` |
  | `maven-1-default` | `[org]/[type]s/[module]-[baseRev](-[fileItegRev])(-[classifier]).[ext]` | `.pom` | `.+`; `.+` |

  `sbt-default` is not defined in that class but in the default descriptor
  (`artifactory.config.xml:172-179`), which #277 checked artifactweave's copy against
  ([research 03](03-repository-configuration-model.md#bundled-definitions)).
- **[S]** The Terraform provider gives each type a default `repoLayoutRef`: `maven-2-default`
  for Maven and Gradle, `ivy-default` for Ivy and `sbt-default` for SBT
  (`default_repo_layout_map.go`). Artifactory's REST API defaults every type to
  `maven-2-default` (research 24, fact 2.13).

### 1.2 Tokens

- **[S]** The token names are `org`, `orgPath`, `module`, `baseRev`, `folderItegRev`,
  `fileItegRev`, `classifier`, `ext`, `type` and `RELEASE` (`RepoLayoutUtils.TOKENS`).
  `[orgPath]` has a filter that turns the dots of the organisation into slashes
  (`TOKEN_FILTERS`); a custom token is written `[name<regex>]`; a parenthesised part is
  optional.
- **[D]** `[folderItegRev]` and `[fileItegRev]` match the layout's two integration-revision
  expressions; a path that matches either is an integration (snapshot) version
  (`repository-layouts`).

### 1.3 Which repositories are "Maven"

Artifactory uses two different tests:
- **[S] The Maven group:** `RepoType.isMavenGroup()` is true for `maven`, `gradle`, `ivy`,
  `sbt` and `p2`. It gates metadata calculation, POM checks and `calculateMetadata`
  (`MavenMetadataCalculationInterceptor`, `MavenPomInterceptor`, `MavenResource`).
  Metadata calculation also runs on a repository of any other type whose layout is
  `maven-2-default` (`MavenMetadataServiceImpl.calculateMavenMetadata`).
- **[S] The Maven layout:** `RepoBaseDescriptor.isMavenRepoLayout()` compares the
  repository's layout with `maven-2-default` by value (`RepoLayoutUtils.isDefaultM2`). Only
  then does the upload rewrite snapshot names and sidecar paths
  (`UploadServiceUtils.isMavenRepo`), and only then does a download of a `-SNAPSHOT` name
  resolve to a unique build (`LatestVersionResolver`).

### 1.4 Coordinates and version order

- **[S]** In a version-container folder, the version of each child is the name of the folder
  that holds its POM (`MavenMetaDataInfo.getVersion`).
- **[S]** Versions in `maven-metadata.xml`, and the `[RELEASE]` and `-SNAPSHOT` download
  resolution, are ordered by `MavenVersionComparator`, which wraps versions-maven-plugin's
  `mercury` comparator (`VersionComparators.getVersionComparator("mercury")`).
  `mvn.metadataVersionsComparatorFqn` and `mvn.metadataSnapshotComparatorFqn` can replace the
  version and snapshot comparators (`ConstantValues`).
- **[S]** artifactweave has the Mercury order as `types.CompareMercury` (`pkg/types/version.go`)
  and uses it for the P5 version searches, but `types.ComparatorFor` gives the Maven family
  `CompareMaven`, Maven's `ComparableVersion` (research 24, fact 2.9).

## 2. Snapshots

### 2.1 The three behaviours

- **[D]** `snapshotVersionBehavior` is `unique` ("a unique, time-based version number"),
  `non-unique` ("the default self-overriding naming pattern:
  `artifactID-version-SNAPSHOT.type`") or `deployer` ("the format sent by the deployer as
  is"). "Metadata will not be generated when selecting the Deployer option." Maven 3 dropped
  non-unique snapshots (`local-repositories`).
- **[S]** The behaviour picks an adapter (`SnapshotVersionAdapterBase.getByType`). An adapter
  never touches metadata paths, files whose module info is not an integration version, or
  paths that are not valid in the layout (`isApplicableOn`), and the upload applies it only
  under the Maven layout (1.3). The deployer adapter is never applicable.

### 2.2 Unique names

- **[S]** A unique name replaces `SNAPSHOT` in the file name with `<timestamp>-<buildNumber>`,
  where the timestamp is `yyyyMMdd.HHmmss` in UTC (`MavenModelUtils.UNIQUE_SNAPSHOT_FORMATTER`)
  and the whole name matches `^((.+)-(.+))-(([0-9]{8}.[0-9]{6})-([0-9]+))[\.-].+$`
  (`MavenNaming`). A name that is already unique is kept as sent.
- **[S] The build number** (`UniqueSnapshotVersionAdapter.adjustUsingServerData`) starts from
  the `buildNumber` in the snapshot folder's own `maven-metadata.xml`, or 0 without one:
  - a main artifact takes the next number;
  - a classifier artifact, or a POM's checksum, takes the current one, "always deployed after
    the pom (which triggers the maven-metadata.xml calculation)";
  - a POM takes the current number when no file has the next one yet and no POM has the
    current one, which covers a main artifact deployed before its POM;
  - the timestamp is the one already used by a file of that build, or now.

  Because the number is read from the metadata file, Artifactory recalculates snapshot-level
  metadata synchronously when a unique file or a snapshot POM arrives (3.2).
- **[S] `build.timestamp`.** When the upload carries a `build.timestamp` matrix parameter
  (milliseconds since the epoch), that time names the build: a file with the same timestamp
  supplies its build number, otherwise the next number is taken
  (`UploadServiceUtils.adjustMavenSnapshotPath`, `adjustUsingClientTimestamp`). A value that
  is not a number is logged and ignored (`MavenSnapshotVersionAdapterContext.setTimestamp`).
- **[S] Sidecars.** A `-SNAPSHOT` checksum upload is mapped to the newest unique file of the
  folder, by build number, whose non-unique form matches the sidecar's target
  (`adjustChecksum`). With no match it builds a name that cannot exist, so the upload fails
  with 404 (section 4).

### 2.3 Non-unique and deployer

- **[S]** Non-unique renames a unique name back to `-SNAPSHOT` when its base version matches
  the folder (`NonUniqueSnapshotVersionAdapter`), so every build overwrites the last.
- **[S]** Deployer stores what is sent (`DeployerSnapshotVersionAdapter`) and skips the
  metadata calculation for snapshot files (`MavenMetadataCalculationInterceptor`).

### 2.4 What the clients send

- **[D] Maven 3** deploys unique snapshots only (`local-repositories`). Maven Resolver
  generates and uploads SHA-1 and MD5 sidecars by default
  (`aether.checksums.algorithms`, default `SHA-1,MD5`) and also supports SHA-256 and SHA-512
  (Resolver configuration).
- **[I] Maven 3's deploy order** is: the artifact, the POM and attached artifacts with their
  sidecars, then the snapshot-level and artifact-level `maven-metadata.xml` with their
  sidecars, each computed by the client from metadata it downloaded first.
- **[S] The JFrog Maven extractor** (`jf mvn`) deploys the module's artifacts and POM itself,
  at `-SNAPSHOT` paths from `Artifact.getVersion()`, and no `maven-metadata.xml`
  (`BuildInfoRecorder.addDeployableArtifact`, `getDeploymentPath`). It adds the publisher's
  matrix parameters (`deploy.` prefix, `ClientProperties.PROP_DEPLOY_PARAM_PROP_PREFIX`);
  the CLI maps its `build.timestamp` to `deploy.build.timestamp`
  (`buildinfoproperties.go`), and `jf rt u` with a build name sends
  `build.name=...;build.number=...;build.timestamp=<ms>` (`buildutils.go`
  `CreateBuildProperties`). So one `jf mvn` build's files share one unique timestamp.
- **[D] Gradle** 6.0 and later upload SHA-256 and SHA-512 files beside MD5 and SHA-1 with
  `maven-publish` and `ivy-publish` (6.0 release notes).

## 3. maven-metadata.xml

### 3.1 What Maven defines

- **[D]** A `maven-metadata.xml` describes a directory that is a `groupId` (plugins), a
  `groupId/artifactId` (versions) or a `groupId/artifactId/version` (a snapshot)
  (repository metadata model). In particular:
  - `version`: "The base version (ie. ending in -SNAPSHOT)" of a snapshot directory;
  - `latest`: "the last version added to the directory ... including both releases and
    snapshots"; `release`: the same "for the releases only";
  - `lastUpdated`: UTC `yyyyMMddHHmmss`; a snapshot's `timestamp`: UTC `yyyyMMdd.HHmmss`;
  - `snapshotVersions`: "only the most recent SNAPSHOT for each unique extension/classifier
    combination", with `classifier`, `extension`, `value` and `updated`;
  - `plugins`: `name`, `prefix` and `artifactId` for each plugin in a group.

### 3.2 When Artifactory recalculates

- **[S] After a file is created** in a local repository of the Maven group, or one with the
  Maven layout, whose path is a valid Maven artifact path, unless it is a snapshot under
  `deployer` (`MavenMetadataCalculationInterceptor.afterCreate`):
  - a unique snapshot file, or a POM in a snapshot folder, recalculates the parent folder
    **synchronously**, because "unique snapshots require instant metadata calculation since
    it is used to calculate future snapshots";
  - any POM queues the grandparent (`groupId/artifactId`) **asynchronously**.
- **[S] After a delete,** the parent is queued, and for a POM the grandparent too, unless
  `mvn.metadata.calculation.skip.delete.event` is set (`afterDelete`).
- **[S] The queue.** `calculateMavenMetadataAsync` and `calculateMavenPluginsMetadataAsync`
  are `@Async(delayUntilAfterCommit = true, workQueue = true)`: they run after the commit, on
  work queues of 8 and 2 workers (`mvn.metadata.calculation.workers`,
  `mvn.metadata.plugin.calculation.workers`). Every calculation, synchronous or not, then
  queues the plugin metadata of the whole repository (`MavenMetadataServiceImpl`).
- **[S] Calculations** are not recursive unless asked; `calculateMetadata` asks by default
  (section 9).

### 3.3 Snapshot level

For a folder whose path is a snapshot version and that holds at least one POM
(`createSnapshotsMetadata`):
- **[S]** `groupId`, `artifactId` and `modelVersion` from the first POM's path.
- **[S]** `version` is `substringBefore(version, "-") + "-SNAPSHOT"`: the part of the
  version before its first hyphen. `1.0-SNAPSHOT` gives `1.0-SNAPSHOT`, but
  `1.0-beta-SNAPSHOT` gives `1.0-SNAPSHOT`, which is not the folder's base version (3.1).
- **[S]** `lastUpdated` is the time of the calculation.
- **[S]** `snapshot`: under `non-unique`, or `deployer` with no unique POM, `buildNumber` 1
  and no timestamp. Under `unique`, the `buildNumber` and `timestamp` of the newest unique
  POM by build number (`BuildNumberSnapshotComparator`).
- **[S]** `snapshotVersions`, under `unique` and with `mvn.metadata.version3.enabled` (default
  true): one entry per (extension, classifier) for the newest unique file of that pair, with
  `value` `<baseRev>-<timestamp>-<buildNumber>` and `updated` the timestamp without its dot.

### 3.4 Artifact level

For a folder with POMs exactly two levels below it, found by an AQL query on depth
(`getSubFoldersContainingPoms`, `createVersionsMetadata`):
- **[S]** `versions`: the distinct version folders, in the version order of 1.4.
- **[S]** `latest`: the last of them, snapshot or release; `version` is set to the same
  value.
- **[S]** `release`: the last version that is not a snapshot.
- **[S]** `lastUpdated`: the time of the calculation.

Maven defines `latest` and `release` as the last version *added* (3.1); Artifactory takes the
highest in version order, whatever the upload order.

### 3.5 Plugin level

- **[S]** A POM with `<packaging>maven-plugin</packaging>` gets the property
  `artifactory.maven.mavenPlugin=true` when it is stored (`MavenPomInterceptor`,
  `PropertiesService.MAVEN_PLUGIN_PROPERTY_NAME`).
- **[S]** The plugin calculation searches the repository for that property, groups the POMs
  by their great-grandparent folder (the `groupId`), and writes one `plugin` per artifactId:
  `artifactId` from the POM, `prefix` from `PluginDescriptor.getGoalPrefixFromArtifactId`,
  and `name` from the POM, or `Unnamed - <id>`. It saves only when the set of plugins
  changed (`MavenPluginsMetadataCalculator`). It does not read the goal prefix from the
  plugin's `plugin.xml`.

### 3.6 Removal

- **[S]** A calculated folder that qualifies for neither level loses its
  `maven-metadata.xml` (`removeMetadataIfExist`). Two consequences:
  - a group folder's plugin metadata is removed by a calculation of that folder, as the
    source's own comment notes ("this will also remove plugins metadata"), until the plugin
    calculation that every calculation queues (3.2) writes it again;
  - metadata that has `snapshotVersions` is kept outside a snapshot folder, for a Maven 2
    client bug (RTFACT-6242).

### 3.7 Metadata the client uploads

- **[S] Snapshot level.** Under `unique` and `non-unique`, a PUT of `maven-metadata.xml` in a
  snapshot folder is read and dropped with 202 Accepted, "use the metadata calculated after
  the pom is deployed" (`UploadServiceImpl.uploadFile`, `consumeContentAndRespondAccepted`).
  Under `deployer` it is stored.
- **[S] Artifact level.** A PUT of `groupId/artifactId/maven-metadata.xml` is not snapshot
  metadata, so it is stored as an ordinary file (`uploadFile`).
- **[I]** It triggers no calculation, since its path is not a Maven artifact path (3.2).
  The asynchronous calculation that the POM queued runs after its commit, so whether
  the stored file is the client's or Artifactory's depends on which lands last; the next
  POM, delete or `calculateMetadata` replaces the client's copy.
- **[S] Metadata checksums.** A checksum PUT for `maven-metadata.xml` is read and answered
  200 with no body, "we maintain our self-calculated checksums for metadata"
  (`validateAndUploadChecksum`). A metadata file's checksum is always the actual one,
  whatever the policy (`LocalRepoChecksumPolicy.getChecksum`).

## 4. Checksums

- **[D] The policies** (`local-repositories`): under "Verify against client checksums" (the
  default, `client-checksums`) a missing client checksum answers 404 to a client asking for
  it, and one that conflicts with the server's answers 409 "until a valid checksum is
  deployed"; under "Trust server generated checksums" client checksums are not verified.
- **[S] Checksum uploads** (`UploadServiceImpl.validateAndUploadChecksum`,
  `validatePathAndUploadChecksum`):
  - a body over 1024 bytes answers 409 "Suspicious checksum file, content length of ...
    bytes is bigger than allowed.";
  - under the Maven layout the target path is adapted as for its artifact (2.2);
  - the value is recorded as the target's client checksum; a match answers 201 with a
    `Location` of the target file;
  - a mismatch answers 409 "Checksum error for '<path>': received '<value>' but actual is
    '<actual>'" under `client-checksums`, and 201 under `server-generated-checksums`;
  - a missing target answers 404 "Target file to set checksum on doesn't exist: ..."; a
    folder answers 409.
- **[S]** Checksum headers sent with the file (`X-Checksum-Sha1`, `-Sha256`, `-Md5`) are
  recorded as client checksums; a mismatch fails the upload only under `client-checksums`
  (`UploadServiceUtils.setFileInfoChecksums`, `LocalRepoChecksumPolicy.verify`).
- **[S]** 6.20 knows SHA-1, MD5 and SHA-256 only (`ChecksumType`); a `.sha512` upload is an
  ordinary file there. Research 04 records that Artifactory 7 serves `.sha512`; artifactweave
  stores SHA-512 for all content and checks `.sha512` uploads like the others (#296, decision
  0032).

## 5. Release and snapshot policy

- **[D]** `handleReleases` and `handleSnapshots` allow deploying releases and snapshots
  (`local-repositories`); both default to true (research 03).
- **[S]** `RealRepoBase.handlesReleaseSnapshot` decides by the layout's module info: a path
  is a snapshot when it is an integration version or contains `[INTEGRATION]`. Metadata,
  checksum and system paths, and paths that are not valid in the layout, are exempt.
- **[S]** A refusal is `SnapshotPolicyException`, 409 "The repository '<key>' rejected the
  resolution of an artifact '<path>' due to conflict in the snapshot release handling
  policy." `assertValidPath` applies it to deploys and downloads alike; include and exclude
  patterns answer 404 on download and 409 on deploy.

## 6. POM consistency checks

- **[D]** Artifactory checks that a POM's `groupId:artifactId:version` matches its deployed
  path and rejects a mismatch unless "Suppress POM Consistency" is set
  (`local-repositories`); the Terraform provider's description names the status, 409
  Conflict (`java.go`, `suppress_pom_consistency_checks`).
- **[S] The check** (`PomTargetPathValidator.validate`), for a POM stored in a repository of
  the Maven group (`MavenPomInterceptor.afterCreate`):
  - `groupId` and `version` may come from `<parent>`; a POM with no `groupId` is not checked;
  - a missing version is refused: "The Pom version of '<path>' does not exists...";
  - the expected prefix is `groupId/as/dirs/artifactId/version`, or
    `groupId/as/dirs/artifactId/` when the path is a snapshot but the POM's version is not a
    `-SNAPSHOT` version; it is compared case-insensitively, and a prefix containing `${` is
    not checked;
  - an unparseable POM is refused: "Failed to read POM for '<path>': ...";
  - with suppression on, each failure is logged and the POM is kept.
- **[S] Copy and move** into a Maven-group repository run the same check on a POM, unless the
  target suppresses it, and answer 400 "Failed to validate target path of pom: ..."
  (`isCopyOrMoveAllowed`).
- **[S] Defaults differ by surface:**

  | Surface | `suppressPomConsistencyChecks` |
  |---|---|
  | 6.20 descriptor (`RealRepoDescriptor`) | `true` |
  | 6.20 UI defaults (`RepoConfigDefaultValues`) | `true`, and `false` for Maven (`DEFAULT_SUPPRESS_POM_CHECKS_MAVEN`) |
  | 6.20 REST model (`LocalRepositoryConfigurationImpl`) | `false` |
  | Terraform provider | `false` for `maven`; `true` for `gradle`, `ivy` and `sbt` (`framework.go`, `PackageTypesLikeGradle` with `NewJavaLocalRepositoryResource(packageType, true)`) |

  At the research baseline artifactweave set `false` for every type (`pkg/repo/defaults.go`,
  `localDefaults` and `remoteDefaults`); #278 sets the provider's per-type defaults.

## 7. maxUniqueSnapshots

- **[D]** The maximum number of unique snapshots of an artifact to keep; when it is
  exceeded the oldest are removed. 0, the default, keeps all (`local-repositories`).
- **[S] Trigger.** After any file is created in a local repository with a limit above 0,
  whose module info is a valid integration version, its folder is noted with the time
  (`IntegrationCleanerInterceptor`). The notes are kept in memory.
- **[S] Schedule.** A job every 300 s (`integrationCleanup.intervalSecs`) cleans each noted
  folder that has been quiet for 60 s (`integrationCleanup.quietPeriodSecs`)
  (`IntegrationCleanupServiceImpl`).
- **[S] What it removes.** For the folder's `org`, `module` and `baseRev`, the integration
  files are grouped by their file integration revision (one unique build), each group dated
  by its earliest-created file, and then split by classifier. While a classifier has more
  builds than the limit, its oldest build's files are undeployed, and their folders pruned
  (`conditionalCleanup`, `performCleanup`, `SnapshotVersionsRetriever`). Non-unique
  `-SNAPSHOT` files are not candidates under the Maven layout, whose folder and file
  revision expressions differ.
- **[S]** The cleanup applies to any layout, not only Maven's, and nothing in it sets a
  floor: a limit of 1 keeps one build. Research 04 reports "the effective minimum is 2"
  from JFrog's knowledge base, which a plain fetch cannot read; that is on #216.

## 8. Version tokens

- **[S]** Download paths are rewritten before resolution, in local repositories, unless
  `request.disableVersionTokens` is set (`LatestVersionResolver.getDynamicVersionContext`,
  `LocalLatestVersionResolver`):
  - **`[RELEASE]`** resolves to the highest release with the path's `org`, `module`,
    `classifier` and `ext`, in the Mercury order, among files whose properties match the
    request's matrix parameters; with `request.searchLatestReleaseByDateCreated` it is the
    newest by creation date instead.
  - **`[INTEGRATION]`** resolves to the newest integration file by creation date with the same
    `org`, `module`, `classifier`, `ext` and `baseRev` (`areModuleInfosTheSame`). Under the
    Maven layout it is not processed: only `-SNAPSHOT` names and `[RELEASE]` are.
  - **A `-SNAPSHOT` name** under the Maven layout resolves to the newest unique file in its
    folder by `<baseRev>-<fileItegRev>` in Mercury order, or by last modified under
    `deployer`. Under `non-unique` it is served as it is.
  - When several candidates have the same version, the one whose path contains the
    translated request path wins; otherwise the first.

## 9. calculateMetadata and generatePom

- **[D] calculateMetadata:** `POST /api/maven/calculateMetadata/{repoKey}/{folderPath}`
  "Calculates Maven metadata on the specified path (for local repositories only). A POM file
  must exist in the repository for the calculation to succeed." `noneRecursive` is optional,
  default false; 200 is `text/plain` (`calculatemavenmetadata`).
- **[S] calculateMetadata in 6.20** (`MavenResource.calculateMetadata`):
  - the class allows the `admin` and `user` roles; the method checks no repository
    permission;
  - a missing path answers 404 "Could not find path '<path>'";
  - a repository that is not local answers 404 "Unable to find local repository '<key>'.";
  - a repository outside the Maven group answers 400 "Repository '<key>' is not a maven
    repository.";
  - otherwise it queues a calculation, recursive unless `noneRecursive=true`, and answers
    200 with no body.
- **[D] generatePom:** `POST /api/maven/generatePom/{repoKey}/{jarFilePath}` "Generates a
  Maven POM file from an internal Jar or a default POM file." 200, no body
  (`generatemavenpomfile`).
- **[S]** 6.20 has no generatePom endpoint. Its building blocks are the first
  `META-INF/maven/**/pom.xml` entry of a jar (`MavenModelUtils.getPomFileAsStringFromJar`)
  and a default POM with `modelVersion` 4.0.0, the coordinates, the packaging and the
  description "Artifactory auto generated POM" (`MavenPomBuilder`).
- **[S] artifactweave's specification** describes `calculateMavenMetadata`'s 200 as "Plain-text
  acknowledgement naming the folder", and `generateMavenPomFile` as writing a POM "deriving
  the coordinates from the repository layout" without the jar's own POM
  (`api/src/artifactory/paths-packagetypes.yaml`); both operations answer 501 today.

## 10. Corrections to research 03 and 04

- **`snapshotVersionBehavior`.** Research 03 gives `unique` "since 7.41". 6.20 is consistent
  with that: its descriptor defaults to `unique` (`LocalRepoDescriptor`,
  `RepoConfigDefaultValues.DEFAULT_SNAPSHOT_BEHAVIOR`), its REST model to `non-unique`
  (`LocalRepositoryConfigurationImpl`), and the Terraform provider sends `unique`. Research 03
  now notes this.
- **`suppressPomConsistencyChecks`.** Research 03's `false` holds for Maven only (section 6).
  Research 03 now gives the default per type.
- **Research 04's Maven section** said nothing in it was unverified. Its claims about the
  minimum of `maxUniqueSnapshots` and the cleanup interval are covered by section 7, and its
  unverified points now point here.

## 11. Inferred and undocumented

These go to the live-comparison list ([#216](https://github.com/weaveplatform/artifactweave/issues/216)):

1. Whether Artifactory 7 still writes a snapshot `<version>` from the part before the first
   hyphen (`1.0-beta-SNAPSHOT` as `1.0-SNAPSHOT`) (3.3).
2. What a GET of `groupId/artifactId/maven-metadata.xml` returns straight after `mvn deploy`:
   the client's upload or Artifactory's calculation (3.7).
3. The status of a snapshot-level `maven-metadata.xml` PUT (202 in 6.20) and of a metadata
   checksum PUT (200 in 6.20) on 7.x (3.7).
4. Whether 7.x checks `.sha512` and `.sha256` uploads under `client-checksums`, and with which
   statuses (section 4).
5. The `maxUniqueSnapshots` floor: whether a limit of 1 keeps one build or two, and whether
   the 300 s interval and 60 s quiet period still apply (section 7).
6. The default `suppressPomConsistencyChecks` of a Gradle, Ivy or SBT repository created over
   REST without the field (section 6).
7. The status 7.x answers for a POM consistency failure on deploy (409 per the provider's
   description).
8. Who may call `calculateMetadata` on 7.x: any authenticated user, as in 6.20, or a
   permission on the path (section 9).
9. `generatePom` on 7.x: the permission it needs, its answer when the POM exists, when the jar
   is missing or has no embedded POM, and for a repository that is not Maven (section 9).
10. Whether 7.x writes plugin-level metadata with the 6.20 rules, and whether a calculation of
    a group folder still removes it (3.5, 3.6).
11. The status `calculateMetadata` answers for a remote or virtual repository on 7.x (404 in
    6.20).
12. The `[INTEGRATION]` token in a repository with the Maven layout: ignored, as in 6.20
    (section 8).

## 12. Versions at the research baseline

| Component | Version |
|---|---|
| Artifactory open source edition (mirror) | 6.20.0, `b99e5649bcad` |
| `jfrog/terraform-provider-artifactory` | `990e8dfdb64e` |
| jfrog-cli-core | `b47892ded3a0` (pinned by `jf` 2.126.0) |
| jfrog/build-info | `4f0d5407279e` |
| Maven repository metadata model | Maven 3.10.0 reference, 2026-10-16 |
| Maven Resolver configuration | read 2026-10-16 |
| Gradle | 6.0 release notes |
