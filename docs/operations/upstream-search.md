# Upstream version and GAVC searches

`GET /artifactory/api/search/versions` and `latestVersion` include upstream-only
versions when `remote=1`. The default, `remote=0`, and explicit `{remote}-cache`
repository names search stored artifacts only. A virtual key expands its members
for this search; native virtual package resolution remains P10.

```sh
curl --fail --user reader "$REGISTRY/artifactory/api/search/versions?g=org.acme&a=lib&repos=central&remote=1"
curl --fail --user reader "$REGISTRY/artifactory/api/search/latestVersion?g=org.acme&a=lib&repos=central&remote=1"
```

For the default Maven-2 layout, exact group and artifact coordinates read
`maven-metadata.xml`; integration queries also read the version's snapshot
metadata. Missing or wildcard coordinates discover matching module metadata
through bounded directory listings. Other layouts, including Ivy, Gradle and
custom layouts, read files from bounded listings only when
`listRemoteFolderItems=true`. Maven metadata searches do not require that flag.
This flag distinction follows the current [JFrog version-search
documentation](https://docs.jfrog.com/artifactory/reference/searchVersions), checked
2026-10-22, and corrects the earlier RC-8 assumption that all searches bypass it.
Snapshot document interpretation follows the [Maven repository metadata
model](https://maven.apache.org/ref/3-LATEST/maven-repository-metadata/repository-metadata.html).

Search never fetches a listed package binary. Metadata and listings retain their
own cache entries; they do not create artifact rows for discovered paths. Results
merge with local and cached versions, deduplicate by version, and retain the
existing Mercury comparator and `v` filters. A concrete `v=1.0-SNAPSHOT` returns
integration file versions; a wildcard filters the final versions. An empty
unfiltered version set remains 404.

Version-only Maven results require current Read authority for each version
folder, repository include/exclude patterns and validated authoritative metadata.
This does not invent a POM or require access to one. Snapshot records naming an
extension/classifier and every GAVC result require current Read authority for that
actual file. Permissions, source identity and credentials are checked again before
delivery. Raw Maven metadata downloads retain their separate exact-file permission
contract; a search is a filtered projection, not a raw XML response.

`specific=true` enables upstream GAVC for exact `g` and `a`, returning proxy
`downloadUri`, `ext`, `version` and optional `classifier` values for listed files.
`remote=0` explicitly suppresses discovery. The compatibility override `remote=1`
requires `specific=true` and exact coordinates; invalid combinations return 400.
Uncached artifacts never receive invented Storage API records or foreign download
URLs. The established `downloadUri` field follows the existing schema and recorded
JFrog example; the current [JFrog GAVC page](https://docs.jfrog.com/artifactory/reference/searchGavc)
prose says `downloadUrl` and does not list a `remote` parameter. The override semantics
and this documentation discrepancy remain **[I] RC-8** capture obligations under
[#216](https://github.com/weaveplatform/artifactweave/issues/216), not proven vendor
parity.

One search permits at most 100 listing pages / 10,000 listing entries across all
repositories, 32 path levels, and 100 Maven metadata documents / 10,000 metadata
version records. Exhaustion, malformed metadata, unavailable listing capabilities,
authentication and security refusals are explicit errors. Ordinary upstream
availability failures may leave a partial local/cached answer when `hardFail=false`;
`hardFail=true` propagates them. Explicit offline and assumed-offline use the common
cache/circuit policy, without extra probes or background artifact downloads.

Different canonical and upstream layouts currently return 501 before network I/O
unless their definitions are equivalent. Mapping directory permissions safely is
a P10 mapped-discovery continuation; coordinate translation cannot authorize the upstream's
lexical path as if it were the canonical path. This restriction matches the existing
Maven metadata translation boundary. RC-8/P19 must compare the refusal, partial
failure status, bounds and permission-filtered metadata against a live target.

The `@remote_search` acceptance feature runs native curl against the application
and deterministic upstream fixtures. Unit/race tests exercise the real retrieval
policy, private upstream authentication, no-store admission, cache aliases and
current permission/source fences. These fixtures establish our behavior; they do
not substitute for the live comparisons tracked by #216.
