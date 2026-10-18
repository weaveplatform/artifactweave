# 0007: Remote repositories: proxy and cache

Status: Proposed

Checked against: **Artifactory 7.161.32** on 2026-10-17 ([research 03](../03-repository-configuration-model.md#artifactory-7x-check), [#341](https://github.com/weaveplatform/artifactweave/issues/341)).

## Context

A remote repository proxies an upstream registry and caches what it fetches. Artifactory
exposes the cache as `<repo>-cache` in storage and search responses, lets clients delete
and zap cache entries, and drives behaviour from a long list of per-repository settings:
`retrievalCachePeriodSecs`, `missedRetrievalCachePeriodSecs`, `assumedOfflinePeriodSecs`,
`storeArtifactsLocally`, `hardFail`, `offline`, `bypassHeadRequests`, `socketTimeoutMillis`,
`blockMismatchingMimeTypes`, `enableTokenAuthentication`, `clientTlsCertificate`,
`customHttpHeaders`, proxies and credentials. Each package format rewrites upstream
metadata so that clients keep talking to the proxy.

## Decision

`internal/remote` owns upstream fetching with one HTTP client per repository built from
the settings above (proxy, credentials, headers, client certificate, timeouts). Behaviour
follows a decision table keyed by item kind and setting:

| Item kind | Cached | Expires | Notes |
|---|---|---|---|
| Unique binary (a versioned artifact, a blob, a tarball) | yes | never | re-fetched only after delete or zap |
| Non-unique metadata (`maven-metadata.xml`, packuments, `index.yaml`, `@v/list`, `tags/list`, simple index pages, NuGet registration) | yes | `retrievalCachePeriodSecs` | served stale while a refresh runs |
| Miss (upstream 404) | negative entry | `missedRetrievalCachePeriodSecs` | no upstream call until expiry |
| Any, `storeArtifactsLocally=false` | no | n/a | streamed through |
| Any, `offline=true` | serve cache only | n/a | miss answers 404 |
| Any, after a connect failure | serve cache only | `assumedOfflinePeriodSecs` | one probe per period |
| Any, `hardFail=true` | | | upstream error becomes the client's error instead of a cache fallback |

A HEAD precedes GET unless `bypassHeadRequests`. Concurrent misses for one path are
collapsed with singleflight within the process. Upstream content type is checked against
the path extension when `blockMismatchingMimeTypes` is on.

**Cache naming.** Cache entries are items with `cached = true` under the real repository
key. One function, `repokey.Resolve`, maps `<repo>-cache` to (`<repo>`, cached) on the way
in and renders `<repo>-cache` on the way out, so:

- AQL, storage info, file lists and search results show `<repo>-cache`;
- `GET`, `DELETE` and `zap` on `/<repo>-cache/<path>` work;
- repository listings, configuration CRUD and permission targets never see `-cache`
  (`ANY REMOTE` and a remote's own grants cover its cache);
- deleting the remote deletes its cache;
- properties can be set on cache items.

**Format adapters.** Each format handler implements the upstream rewrite for its metadata
(npm tarball URLs, PyPI hrefs, Helm `urls`, NuGet `@id` values, Terraform download URLs,
Docker Hub token authentication and foreign layers) through the base-URL resolver.

## Rationale

A decision table makes the cache semantics testable one row at a time and gives the
acceptance features their structure. Storing cache entries as flagged items rather than as
a second repository keeps one item model while reproducing every place Artifactory shows
or hides the `-cache` name.

**Alternatives considered:**
- *A real `<repo>-cache` repository row*: leaks into listings and lets clients create or
  mutate it.
- *A separate cache store*: duplicates the item model and breaks AQL over cached items.

## Artifactory 7.x check

The decision stands against 7.161.32 as a plan for P9. The 7.x notes add inputs for P9:
- **Zap.** Zapping a remote cache needs Manage or Delete ([7.104.12](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-710412-self-managed)), as artifactweave's
  `api/zap` already checks.
- **Remote folder listing** is off by default ([7.98.7](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-7987-self-managed)).
- **Go and npm.** Go remotes serve only through a virtual repository (go-modules), and npm
  audit is on by default for npm remotes that support it (7.124.0).
- **Offline detection.** The "assumed offline" state was fixed repeatedly
  ([7.117.5](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71175-self-managed), SaaS 7.174.0).
- **Version tokens** resolve in remote caches ([research 27 section 13](../27-maven.md#13-artifactory-7x-check)).

## Constraints

- Go remote repositories resolve only through a virtual repository, as in Artifactory.
- Smart remote (Artifactory-to-Artifactory `contentSynchronisation`) is accepted in
  configuration and ignored in v1.
- Singleflight is per process; two processes may fetch the same miss twice.

## Verification

- A test per table row against `internal/fakeupstream` with fault modes (5xx, slow, wrong
  content type, dropped connection).
- `repokey.Resolve` tests over every endpoint family that may see a cache key.
- Acceptance features resolve each format through a remote against the fakes; nightly
  runs hit the real registries.

## References

- [03-repository-configuration-model](../03-repository-configuration-model.md), remote fields
- [04-package-protocols](../04-package-protocols.md), per-format upstream notes
- <https://docs.jfrog.com/artifactory/docs/remote-repositories>
