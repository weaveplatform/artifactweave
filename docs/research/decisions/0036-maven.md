# 0036: Maven, Gradle, Ivy and SBT

Status: Accepted

Checked against: **Artifactory 7.161.32** on 2026-10-17 ([research 27 section 13](../27-maven.md#13-artifactory-7x-check), [#341](https://github.com/weaveplatform/artifactweave/issues/341)); audited against KB, Jira and client sources on 2026-10-18 ([#378](https://github.com/weaveplatform/artifactweave/issues/378)).

## Context

P7 serves Maven local repositories to `mvn` and `jf mvn`, and Gradle, Ivy and SBT
repositories through the same format with their own layouts. Nothing writes
`maven-metadata.xml` today, `calculateMetadata` and `generatePom` answer 501, and the package
index orders the Maven family with `CompareMaven` rather than Artifactory's Mercury order.
[Research 27](../27-maven.md) records what Artifactory 6.20 does, what Maven and the JFrog
CLI send, and what no source confirms. Decision [0032](0032-package-format-handlers.md) gives
formats the hooks this decision uses (`Inspect`, `Admit`, `Resolve`, `Reindexer`, `Derive`
and `Enqueue`), and decision [0033](0033-persistent-work-queue.md) the work queue.

## Decision

**Scope.** The Maven format serves the `maven`, `gradle`, `ivy` and `sbt` package types: the
POM checks, metadata and `calculateMetadata` apply to all four, as Artifactory's Maven group
does without `p2` (research 27 section 1.3). Snapshot renaming, sidecar mapping and
`-SNAPSHOT` download resolution apply only where the repository's layout equals
`maven-2-default` by value, as in Artifactory.

**The package index** (decision [0018](0018-package-index.md)):
- **Package:** `groupId:artifactId` per repository, from the layout's `[org]` or `[orgPath]`
  (with dots) and `[module]`.
- **Version:** the version folder's name: `[baseRev]`, followed by `-[folderItegRev]` when the
  layout has one, such as `1.0-SNAPSHOT`. A unique build is not a version of its own.
- **Files:** every file the layout places in that version: the POM or Ivy descriptor, the
  artifacts and classifiers, and every unique build. Derived `maven-metadata.xml` files are
  not version files.
- **Order:** `types.CompareMercury` for all four types, which `types.ComparatorFor` returns
  instead of `CompareMaven`.
- **Per-version metadata:** what `Inspect` reads from the POM before the write transaction:
  `packaging` and `name`, for plugin metadata.

**Deploys** (`Admit`, inside the upload's transaction):
- **Release and snapshot policy.** `handleReleases` and `handleSnapshots` refuse with 409 and
  Artifactory's message, on deploy and on download (research 27 section 5). A deploy checks
  it between the blacked-out check and the patterns, as Artifactory does; a download checks
  it in `Resolve`, after `generic.CheckDownload`, so a path the patterns also refuse answers
  their 404 rather than 409.
- **Unique snapshots** (amended 2026-10-18, #280). In a local repository, a `-SNAPSHOT` file
  of its folder's version becomes `<baseRev>-<yyyyMMdd.HHmmss>-<buildNumber>` in UTC, on a
  deploy, a checksum deploy and an archive entry alike. The response reports the stored path.
  The build number is assigned at upload from the snapshot folder's unique files, read in the
  upload's own transaction, never from metadata and never by waiting on the queue:
  - with a `build.timestamp` matrix parameter (milliseconds since the epoch), the build that
    already has that timestamp, or else the next number; a value that is not a number is
    ignored;
  - a checksum upload: the newest build of its own file (the sidecars below);
  - otherwise Artifactory's rules (research 27 section 2.2): a main artifact takes the next
    number; a classifier artifact takes the current one; a POM takes the current one unless
    that build already has a POM, as when its main artifact arrived first;
  - "current" is the highest build number of any unique file in the folder (DV-063), and the
    timestamp is that build's, or now for a new build.

  A name that is already unique, a release, metadata and any file the layout does not read
  as a snapshot of its folder's version are stored as sent. Replacing a file at the stored
  path needs Delete there, as at the path sent.
- **Non-unique and deployer.** Non-unique renames a unique build of the folder's version back
  to `-SNAPSHOT`, sidecars included, so each build replaces the last; deployer stores what is
  sent.
- **POM checks.** `Inspect` parses the POM; `Admit` applies Artifactory's rules (research 27
  section 6) and refuses with 409 and its message unless `suppressPomConsistencyChecks` is
  set. A copy or move of a POM is checked against the target and refused with 400. A POM
  with `maven-plugin` packaging gets `artifactory.maven.mavenPlugin=true`.
- **Defaults.** `suppressPomConsistencyChecks` defaults to `false` for `maven` and `true` for
  `gradle`, `ivy` and `sbt`, as the Terraform provider and Artifactory's UI do.
  `pkg/repo/defaults.go` sets them so, locally and remotely (#278).

**Uploaded metadata and checksums:**
- **Snapshot level.** Under `unique` and `non-unique`, in a repository with the Maven
  layout, a PUT of `maven-metadata.xml` in a snapshot folder is read and dropped with 202, as
  in Artifactory (`uploadFile` checks it only under `isMavenRepo`). Under `deployer`, or
  another layout, it is stored, and that folder never gets derived metadata.
- **Artifact level** (the client's `groupId/artifactId/maven-metadata.xml`). The PUT is stored
  with 201 and queues the package's metadata in the same transaction. The run replaces the
  file with derived metadata whenever the package has a version with a POM, so a read always
  returns the server's metadata; with no such version the client's file stays.
- **Deployer** (amended 2026-10-17, #279). A repository whose `snapshotVersionBehavior` is
  `deployer` gets no derived metadata at any level: nothing is queued, written or removed,
  and every `maven-metadata.xml` in it is the client's. Artifactory 7.x documents the deploy
  side so ("Metadata will not be generated when selecting the Deployer option",
  `local-repositories`). [RTFACT-26126](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-26126)
  does not support the delete side: its fix (7.27.3) keeps the metadata of a `deployer`
  repository's other snapshots after a delete, rather than emptying the file, and JFrog staff
  on [RTFACT-26651](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-26651) say that after
  other files are deleted "the re-calculation should be done properly on our end". So 7.x
  recalculates after a delete where this decision does nothing;
  [#418](https://github.com/weaveplatform/artifactweave/issues/418) aligns it. 6.20's source
  skipped only snapshot files under `deployer`; that difference is on #216.
- **Metadata checksums.** A checksum PUT for `maven-metadata.xml` answers 200 and is
  ignored; a metadata file's checksums are always the server's, so the client's later
  `.sha1` never fails under `client-checksums`.
- **Sidecars.** `.md5`, `.sha1`, `.sha256` and `.sha512` uploads are checked under
  `checksumPolicyType` (decision 0032, #296) with Artifactory's answers: 409 for a body over
  1024 bytes, 409 "Checksum error for ..." for a mismatch under `client-checksums`, 201
  otherwise, and 404 when the target file does not exist. A 201 names the file the checksum
  was recorded on in `Location`, in every repository, as Artifactory's
  `sendUploadedChecksumResponse` does. Under `unique`, a `-SNAPSHOT` sidecar applies to the
  newest unique build whose name, with `-SNAPSHOT` for its build, is the sidecar's file; with
  none it answers 404 naming the path sent (DV-064).

**Derived work, through the work queue** (as agreed, decisions 0032 and 0033):
- **Kinds.** Three, each keyed by repository:
  - a package's metadata, keyed by `groupId:artifactId`: its artifact-level file, the file
    of each snapshot folder, and its group's plugin metadata;
  - a group's plugin metadata, keyed by `groupId`;
  - a package's `maxUniqueSnapshots` cleanup, keyed by `groupId:artifactId`.
- **Enqueueing.** The format's hooks call `Env.Enqueue` inside the write's or delete's own
  transaction, so no work is lost and none is queued for a rolled-back write. A deploy,
  delete, copy, move or restore in a package queues its metadata; a POM with
  `maven-plugin` packaging also queues its group's plugin metadata; a unique build in a
  repository with `maxUniqueSnapshots` above 0 queues the cleanup.
- **Runs** read the package index, not the folder tree, and write through `Derive`, so they
  do not re-enter the hooks and the files carry server checksums. They remove through
  `Underive`. A derived file records `_system_` as its creator and modifier, which is how a run
  tells it from a client's artifact-level file (#279).
- **Plugin metadata** is rebuilt by every package's metadata run, and written only when it
  changes, so a run for a package with no plugin leaves it as it was. Artifactory has
  recalculated plugin metadata only when the file deployed or deleted is a Maven plugin since
  7.10.1 and 6.23.0 ([RTFACT-20623](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-20623),
  research 27 section 3.2), so the stored result is the same. The legacy 7.10.1 note,
  "calculated for every deploy or delete actions"
  ([legacy notes](https://releases.jfrog.io/artifactory/legacydocs/Artifactory-Self-Managed-Release-Notes-End-of-Life.pdf)),
  is the weaker source. A group's own item, keyed by `groupId`, is queued when a POM marked
  `artifactory.maven.mavenPlugin` is deployed or deleted, or a POM gains or loses the mark, so
  a read of the group's metadata waits for it (#279).
- **Reads.** A GET or HEAD of a `maven-metadata.xml`, or of its sidecar, whose package or
  group has pending work calls `EnsureFresh` first, so a client never reads stale metadata.
  A parked item answers 500 in the error envelope.

**What the metadata says.** Artifactory's rules (research 27 sections 3.3 to 3.6), with two
departures:
- **Snapshot level,** for each snapshot folder with a POM: `groupId`, `artifactId`,
  `lastUpdated`, `snapshot` and `snapshotVersions` as Artifactory writes them under each
  behaviour. `<version>` is the folder's base version, such as `1.0-beta-SNAPSHOT`, where
  Artifactory writes the part before the first hyphen.
- **Artifact level,** for each package with a version that holds a POM: `versions` in Mercury
  order, `latest` and `version` the highest of them, `release` the highest release, and
  `lastUpdated`. Metadata is removed when no POM is left (#279).
- **Plugin level,** for each group with a plugin POM: one `plugin` per artifactId, with
  `prefix` from the artifactId and `name` from the POM of its highest version, where
  Artifactory takes whichever POM its search returns first. It is removed only when the group
  has no plugin left; Artifactory also removes it whenever that folder is recalculated, until
  its queued plugin calculation writes it back.

**`maxUniqueSnapshots`.** A run applies Artifactory's rule (research 27 section 7) to the
queued package: per classifier, while there are more unique builds than the limit, the
oldest build's files are deleted through the item service and empty folders pruned, which
queues the package's metadata again. The cleanup state lives in the queue, so it survives a
restart, and it runs when the queue reaches it rather than on a five-minute timer.

**Version tokens** (`Resolve`, GET and HEAD, #281; checked against Artifactory 7.161.32):
`[RELEASE]`, `[INTEGRATION]` and `-SNAPSHOT` resolve in local repositories and remote
repositories' caches, and in a virtual repository through its members, the repositories
JFrog's Retrieve Artifact documents (research 27 section 8). A candidate has the path's
coordinates and matches the request's matrix parameters, and resolution reads the files,
never `maven-metadata.xml`.
- **Under the Maven layout,** a `-SNAPSHOT` name resolves to its folder's newest unique build
  and `[RELEASE]` to the highest release in the Mercury order, with Artifactory 6.20's rules,
  which 7.x documents no differently. `[INTEGRATION]` is left as it is.
- **Under any other layout,** `[RELEASE]` and `[INTEGRATION]` resolve to the alphabetically
  last release, or integration revision of the path's base revision, as the 7.x
  documentation says. Artifactory 6.20's code ordered them by the Mercury order and by
  creation date; 7.x wins (research README), and #216 asks which a live 7.x does.

**`calculateMetadata`** (`POST /api/maven/calculateMetadata/{repoKey}/{folderPath}`):
- 404 "Could not find path '...'" for a missing path; 404 "Unable to find local repository
  '...'." for a repository that is not local; 400 "Repository '...' is not a maven
  repository." for a type outside the four.
- **Permission:** Deploy on the folder.
- **Behaviour:** rebuilds the package index under the folder from the item tree
  (`Reindexer`), then queues the metadata of every package and group under it, or of the
  folder alone with `noneRecursive=true`. It answers 200 with an empty body once the work is
  queued, as Artifactory does.

**`generatePom`** (`POST /api/maven/generatePom/{repoKey}/{jarFilePath}`):
- The same 404 and 400 answers; 404 "Could not find path '...'" when the jar is missing.
- **Permission:** Deploy on the POM's path.
- **Behaviour:** the POM goes at the jar's descriptor path in the repository's layout. An
  existing POM is left as it is, with 200. Otherwise the content is the jar's
  `META-INF/maven/<groupId>/<artifactId>/pom.xml` for the jar's own coordinates, or, without
  one, a default POM: `modelVersion` 4.0.0, the coordinates from the path, `packaging` from
  the jar's extension, and the description "Artifactory auto generated POM". It is written
  through the item service, so the POM checks, the index and the metadata queue apply. 200
  with an empty body.

**Gradle, Ivy and SBT for M2.** They are covered by layout tests (#277): coordinates, the
index, the policy checks and metadata through their layouts. No Ivy or SBT client runs in
acceptance, and Gradle's run does not gate M2 (decision
[0015](0015-testing-and-acceptance.md)).

## Rationale

- **Build numbers from the folder's files.** Artifactory reads the current build number from
  the snapshot folder's metadata, the newest unique POM's, which is why it must recalculate
  that metadata synchronously on every unique upload. Reading the folder's files in the
  upload's own transaction lets every metadata level go through the queue, and the highest
  build any file has is never reused: Artifactory's number repeats build 1 in a folder with no
  POM, and sends a classifier deployed before its build's POM to the previous build (DV-063).
- **The sidecar that matches no build** answers 404 for the path the client sent. Artifactory
  names a path built from build 0 and the current time in milliseconds, which the client
  never sent and cannot find (DV-064).
- **The queue and rebuild on read.** Recalculating inside the write transaction contends on
  PostgreSQL and blocks SQLite's single writer (decision 0033). Artifactory calculates
  artifact-level metadata after the commit and serves whatever is stored meanwhile. Rebuilding
  on read keeps the write path just as short and never serves metadata that lags a completed
  deploy (DV-054).
- **Mercury order** is the order Artifactory's metadata, its `[RELEASE]` resolution and the
  P5 version searches already use, so the index, the metadata and the searches agree.
  Maven defines `latest` and `release` as the last added; Artifactory's highest-version rule
  is kept because clients compare against Artifactory.
- **The client's artifact-level metadata** is stored rather than refused because `mvn deploy`
  fails on a refused upload, and Artifactory stores it. Recomputing over it means a read
  never depends on which of the client and the queue wrote last.
- **The snapshot `<version>`.** Maven defines it as the base version ending in `-SNAPSHOT`;
  Artifactory's truncation at the first hyphen is wrong for qualified versions. Maven
  resolves snapshots from `<snapshot>` and `<snapshotVersions>`, so the change affects only
  tools that read `<version>` (DV-050).
- **Plugin metadata** is not wiped, even briefly, by recalculating its group folder;
  Artifactory's own source questions that removal (DV-051).
- **`calculateMetadata` needs Deploy.** It rewrites files every Maven client trusts. In 6.20
  any authenticated user can recalculate any folder; Deploy is what writing those files would
  take anyway (DV-052).
- **`generatePom`** picks the embedded POM for the jar's own coordinates, where 6.20's helper
  takes the first `pom.xml` it meets: a shaded jar carries its dependencies' POMs too.
- **`maxUniqueSnapshots`** keeps its pending folders in memory in Artifactory, so a restart
  forgets them until the next deploy to that folder. The queue persists them, and its
  coalescing replaces the 60-second quiet period (DV-053).
- **POM reading.** The check reads only the coordinates it compares, where Artifactory's
  strict Maven reader also refuses elements outside the POM model; a POM a newer model or a
  tool extends is still a POM to every client (DV-060).
- **`suppressPomConsistencyChecks` per type.** The Terraform provider and Artifactory's UI
  both default Gradle, Ivy and SBT to suppressed and Maven to checked, so a repository
  created either way matches. What a REST create without the field gets on 7.x is on #216.

## Artifactory 7.x check

The decision stands against 7.161.32. The 7.x documentation and release notes add four points
([research 27 section 13](../27-maven.md#13-artifactory-7x-check)):
- **POM parsing** accepts a POM that starts with an empty line, holds UTF-8 non-breaking
  spaces or declares ISO-8859-1 (7.125.4, SaaS 7.110.5). A failed check answers 409 ([#278](https://github.com/weaveplatform/artifactweave/issues/278)).
- **Version tokens** resolve on 7.x in remote caches and virtual repositories as well as local
  ones, and a path cannot mix `[RELEASE]` with `[INTEGRATION]`. P7 resolves in local
  repositories ([#281](https://github.com/weaveplatform/artifactweave/issues/281)); P9 and P10 extend it.
- **`calculateMetadata`** answers 403 on 7.x to a caller without the permission it needs, so
  DV-052 now differs from 7.x only in which permission that is.
- **`maxUniqueSnapshots` of 0** turns the cleanup off (SaaS 7.164.0).

The audit against KB, Jira and client sources ([#378](https://github.com/weaveplatform/artifactweave/issues/378)) adds the following:
- **Plugin metadata** is calculated only for plugin files since 7.10.1 ([J] RTFACT-20623);
  the derived-work rule above stores the same result.
- 7.x differs: an ISO-8859-1 POM is accepted on upload and on copy ([D] SaaS 7.110.5,
  RTDEV-56287); fixed by [#418](https://github.com/weaveplatform/artifactweave/issues/418).
- 7.x differs: version tokens resolve only under a layout that defines both
  `[folderItegRev]` and `[fileItegRev]` ([D] retrieveartifact); fixed by
  [#418](https://github.com/weaveplatform/artifactweave/issues/418).
- 7.x differs: under `deployer`, a delete of other files recalculates the metadata ([J]
  RTFACT-26126, fixed in 7.27.3; JFrog staff on RTFACT-26651); fixed by
  [#418](https://github.com/weaveplatform/artifactweave/issues/418).
- 7.x differs: under `client-checksums`, a GET of a sidecar the client never sent answers 404
  ([D] local-repositories); fixed by [#399](https://github.com/weaveplatform/artifactweave/issues/399).
- 7.x differs: an sbt repository created without `repoLayoutRef` gets `sbt-default` ([D]
  sbt-repositories); fixed by [#396](https://github.com/weaveplatform/artifactweave/issues/396).
- `maxUniqueSnapshots`: a KB article gives a floor of 2 unique snapshots and a cleanup every 5
  minutes ([KB], research 27 section 13), where this decision accepts a limit of 1 and runs
  the cleanup when the queue reaches it; [#283](https://github.com/weaveplatform/artifactweave/issues/283)
  settles both.
- `calculateMetadata`: JFrog's REST reference required Manage, Delete/Overwrite, Deploy/Cache,
  Annotate and Read from 4.8 ([D], an Internet Archive copy, research 27 section 13), so
  DV-052's "open to any authenticated user" no longer describes Artifactory;
  [#282](https://github.com/weaveplatform/artifactweave/issues/282) settles the permission.

## Constraints

- The points in research 27 section 11 are unconfirmed until the live comparison (P19,
  #216). The snapshot `<version>`, the client's artifact-level PUT, the metadata upload
  statuses, the `maxUniqueSnapshots` floor, `generatePom` and the `calculateMetadata`
  permission are the ones that would change this decision.
- The deploy rules (#278) were checked against Artifactory 7.161.32: the current local
  repository docs and the 7.x release notes confirm the policy on deploys, the POM check's
  409 and the checksum policy's 409, and change none of them. The download side of the
  policy, the messages, the metadata and checksum upload answers and the strict POM reader
  come from 6.20 only and are on #216.
- Without the 60-second quiet period, a limit of 1 removes the previous build as soon as the
  first file of the next one is stored.
- Remote and virtual Maven repositories, including metadata merging, are P9 and P10
  (decisions [0007](0007-remote-repositories.md) and [0008](0008-virtual-repositories.md)).

## Verification

- Index, deploy-rule and metadata tests in `internal/format/maven` against the
  `formattest` contract on every store (#276, #278, #279), with golden XML for each level.
- Snapshot naming with Maven's and the JFrog extractor's upload orders: two snapshot deploys
  give `buildNumber` 2 and correct `snapshotVersions` (#280).
- Token resolution tables and download scenarios (#281); `calculateMetadata` and
  `generatePom` status and permission rows (#282); `maxUniqueSnapshots` with a limit of 2
  (#283).
- Layout tables from real Gradle, Ivy and SBT publication paths (#277).
- `mvn deploy` and resolution, snapshots included, in acceptance (#270, #284).

## References

- [27-maven](../27-maven.md), [24-package-format-handlers](../24-package-format-handlers.md),
  [03-repository-configuration-model](../03-repository-configuration-model.md)
- [0018-package-index](0018-package-index.md), [0032-package-format-handlers](0032-package-format-handlers.md),
  [0033-persistent-work-queue](0033-persistent-work-queue.md)
- Artifactory OSS 6.20, `knottnhupfer/artifactory-oss` at `b99e5649bcad`
- Maven repository metadata model,
  <https://maven.apache.org/ref/3-LATEST/maven-repository-metadata/repository-metadata.html>
- [Deviations register](../../implementation/deviations.md): DV-050 to DV-054, DV-060, DV-063, DV-064
