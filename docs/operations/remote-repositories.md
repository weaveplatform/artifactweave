# Generic remote repositories

Generic remote repositories now fetch HTTP(S) artifacts through their ordinary
repository key. [Maven-family remotes](maven-remote-repositories.md) also implement
their native source adapter; other native formats retain their existing gates until their P9
adapter tickets; virtual fetching belongs to P10 and project-scoped sources to P11.
See the [capability roadmap](../implementation/capability-roadmap.md) for those owners.

Create a remote using the existing repository API, for example:

```sh
curl --fail --user admin "$REGISTRY/artifactory/api/repositories/vendor" \
  --request PUT --header 'Content-Type: application/json' \
  --data '{"rclass":"remote","packageType":"generic","url":"https://packages.example/releases/"}'
curl --fail --user reader "$REGISTRY/artifactory/vendor/tool.bin" --output tool.bin
```

A caller needs Read on the remote and requested path; filling its cache does not
require Deploy. Repository blackout and include/exclude rules still apply. Requests
recheck current user/token state, permissions, source identity, configuration and
upstream credential revisions before publication and delivery. Uploads to remotes
and cache aliases remain refused.

The first GET validates a complete upstream representation before publishing it.
A cold range therefore fetches and checks the full artifact, then serves the requested
range. HEAD can obtain upstream headers without creating an item. Any upstream checksum
headers on a cold HEAD are syntax-checked claims; only a completed GET computes
checksums from bytes, which subsequent warm HEAD responses report. Warm GET/HEAD,
Range and conditional responses use the existing artifact delivery path. Configured
signed storage/CDN redirects occur after publication, so the first and subsequent
redirects refer to the same validated bytes. Transient no-store bodies support ranges
and conditions but do not create items, cache entries or blob references.

`vendor-cache` is a read view, never a second repository. Its misses do not make
upstream GET or HEAD requests. Storage information, file lists and AQL report fetched
items under this cache name; repository listings and permission targets still use
`vendor`. AQL-backed `jf rt dl` discovers cached items. Use the supported
`jf rt direct-download vendor/path destination/` command when fetching an exact cold
path with the pinned JFrog CLI. A warm immutable artifact remains readable during an
upstream outage; freshness, stale fallback, negative caching and no-store behavior
follow the [retrieval contract](../implementation/remote-retrieval-contract.md).

Checksums of cached artifacts are served from their recorded digests. A cold
`.sha256` request contacts upstream only when `retrieveSha256FromServer` is enabled;
that response is bounded to 4096 bytes and must contain a valid SHA-256 digest
(optionally followed by a filename). It remains transient, and does not fetch or cache
the artifact itself. Its HEAD response describes the normalized 64-character digest
without fetching a body; GET performs the syntax validation. Other cold checksum
requests return 404. Matrix parameters filter
stored properties; transient content has no properties, so mandatory filters cannot
match. Existing cached folder and archive browsing remains available; upstream folder
browsing is tracked by #635.

`?trace` follows ordinary generic artifact resolution, including a permitted cold
fetch, but serves no artifact bytes, issues no signed URL and records no download.
Trace output omits upstream URLs and credentials. Origin metadata retains only the
final upstream URL's scheme, authority and path, with userinfo, query and fragment
removed. Upstream errors retain approved statuses in the established error envelope;
upstream bodies, cookies and authentication challenges are not relayed.

A successful GET, range or signed URL issuance updates cache usage. HEAD, 304,
lookup-only requests and failed bodies do not. `skipUpdateStats=true` suppresses the
download counter; it does not make successfully delivered content unused. Repository
deletion removes its durable source/cache state. [Cache maintenance](../implementation/remote-cache-maintenance.md)
is implemented: zap expires raw metadata and clears misses while preserving complete
binaries; path deletion and restore fence older pending fetches. A positive
`unusedArtifactsCleanupPeriodHours` enables hourly scheduling of persistent cleanup
pages, with current usage rechecked before deletion. Zero disables it, including queued
work. Native adapters retain their own format-specific maintenance proofs.

With a configured master key, internal cache/credential identity keys are derived from
it. The default unencrypted installation instead generates a private random key in its
metadata database, preserving cache identity across restarts. This internal setting is
not exposed by configuration APIs. Protect and back up that database as described in
[backup and restore](backup-restore.md); it already contains the installation's
unencrypted authentication signing material when no master key is configured.

`test/acceptance/features/generic_remote.feature` runs real curl and pinned JFrog CLI
commands with isolated client/output directories, a Read-only user, cold and warm
paths, an upstream outage, cache-alias search/download and repository deletion. Its
request counts describe artifactweave, not a captured Artifactory response. Inferred
compatibility behavior retains the retrieval contract's RC-2/P19 comparison obligation.
