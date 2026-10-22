# Maven-family remote repositories

Maven, Gradle, Ivy and SBT remotes fetch repository content through
`/artifactory/{repo}/{path}` (#625). The shared [remote operations guide](remote-repositories.md)
explains transport security, source authorization, cache aliases, no-store delivery and
cache maintenance. Virtual merging remains P10.

```sh
curl --fail --user admin "$REGISTRY/artifactory/api/repositories/maven-central" \
  --request PUT --header 'Content-Type: application/json' \
  --data '{"rclass":"remote","packageType":"maven","url":"https://repo.maven.apache.org/maven2/","rejectInvalidJars":true}'
```

Configure the native client repository URL as `$REGISTRY/artifactory/maven-central/`,
with its ordinary server credentials. Read permission is sufficient to populate the
cache; client deployment to a remote remains refused.

## Resolution and metadata

Artifacts and POMs become cached items and enter the Maven-family package index.
Reindexing rebuilds that index from fetched content. Upstream `maven-metadata.xml`
remains a separate raw representation at both artifact and version levels: fetching
one version never replaces the upstream version list with the cached subset.
`remote.MavenMetadata` exposes this authoritative version data to the #213 source
consumer without downloading every listed binary; the version-search API integration
remains owned by #213.

A metadata-file Read grant permits the complete original XML. It does not grant any
artifact listed in it. Resolving `-SNAPSHOT`, `[RELEASE]` or `[LATEST]` checks the
original requested path before metadata I/O, checks the metadata file, and independently
checks the selected artifact. Both modern `snapshotVersions` and legacy snapshot
`timestamp`/`buildNumber` metadata are understood. Non-unique snapshot payloads refresh
on the mutable-content clock; unique snapshots and releases are immutable cache items.
Metadata-only zap preserves these binary items.

`repoLayoutRef` determines the canonical permission/cache/index path;
`remoteRepoLayoutRef` translates the upstream request through the configured layouts.
Equivalent layouts preserve metadata paths. A translation that cannot preserve all
coordinates, including metadata with no representable target layout, fails with 400
before upstream I/O. Native Gradle, Ivy and SBT qualification has the explicit owners
listed below; the server layout tests cover direct Gradle/Ivy/SBT paths and a Gradle to
Maven-2 translation.

The `{repo}-cache` view performs no upstream I/O. Raw XML is available there after
fetching, independently of the requesting client's Accept header. The normal shared
freshness/stale policy governs a remote while its upstream is unavailable.

## Admission, checksums and retention

`handleReleases` and `handleSnapshots` reject disallowed requests before a fetch.
POM coordinates must agree with the canonical path unless
`suppressPomConsistencyChecks` is enabled. `rejectInvalidJars=true` checks the complete
ZIP structure and entry CRCs before publication. These checks also apply to transient
`storeArtifactsLocally=false` responses. XML/POM parsing is bounded to 16 MiB; JAR
validation permits at most 10,000 entries and 512 MiB total expanded content. Rejected
bytes never become a successful cached representation.

| `remoteRepoChecksumPolicyType` | Behavior |
|---|---|
| `generate-if-absent` | Read available upstream checksum headers or SHA-1/MD5 sidecars, reject mismatches, compute missing digests |
| `fail` | Also reject an artifact with no upstream checksum |
| `ignore-and-generate` | Compute digests without sidecar lookup |
| `pass-thru` | Preserve available SHA-1, MD5, SHA-256 and SHA-512 claims for checksum delivery, even when they differ; generate any missing digest |

Checksums are bounded and syntax checked. Actual byte digests always identify blobs;
pass-thru claims remain separate, including for raw metadata and no-store responses.
A cold Maven checksum request resolves and validates its owner artifact, then returns
the appropriate checksum. A successfully delivered sidecar marks its owner used;
HEAD and failed delivery do not.

`maxUniqueSnapshots` applies to fetched unique builds through the existing persistent
Maven cleanup worker, with the configured quiet period and cleanup interval. Zero
means unlimited. Deletions use the cache item path and its normal invalidation,
index and trash hooks; cleanup never generates replacement upstream XML.

## Eager companions

`fetchJarsEagerly` schedules the matching JAR after a POM read;
`fetchSourcesEagerly` schedules the sources JAR after a binary JAR read. Background
reads do not recursively spawn companions. Disabled flags, HEAD, cache-only and
no-store retrieval do not schedule new work. Eager failures do not fail an otherwise
valid foreground response.

The persistent `maven.remote-companion` work kind deduplicates by source, credential
revision, original principal reference and target. It uses two workers, a maximum of
256 pending/leased/parked operations, and a 60-second deadline per attempt. New
companions are skipped when this bound is full. Transient failures get at most three
attempts with the work queue's backoff; permanent authority/configuration refusals
park immediately. Parked rows retain their sanitized failure and emit the normal work
queue error log. Repeated foreground reads do not silently reactivate a parked job.

Jobs survive restart. Each execution reloads current repository policy, source identity,
upstream credential revision, user/token state and permissions. Persisted references
contain no password, token value, group grants or cached administrator status. Revoked
or expired credentials cannot resume an old fetch. A source or credential change
produces a different operation identity. Back up the queue with the metadata database.

## Evidence and owned client gaps

| Workflow | Proof and owner |
|---|---|
| Native Maven 3.10 | `maven_remote.feature`: release, parent/transitive dependencies, unique and non-unique snapshots from a cold remote; a second empty client cache succeeds after upstream shutdown; raw snapshot metadata is readable through the cache alias |
| Native Gradle remote resolution | Server Gradle-layout and translation tests pass; required native-client qualification remains P28, broadened by P41 |
| Native Ivy remote resolution | Server Ivy-layout tests pass; required native-client qualification remains P28, broadened by P41 |
| Native SBT remote resolution | Server SBT-layout tests pass; required native-client qualification remains P28, broadened by P41 |
| JFrog Maven extractor | #523/P28 remains open for Maven 3.10; native Maven is not pinned backward to hide that extractor limitation |

Runtime tests cover admission failures, all checksum policies, source metadata versus
reindex, permission/pattern denial, bounded durable companions, restart/revocation,
layout translation and cached snapshot retention. These fake-upstream observations
prove artifactweave behavior, not a captured Artifactory response. The [research
addendum](../research/27-maven.md#12-p9-remote-implementation-evidence-625) records
current sources and the remaining #216/P19 comparison cases.
