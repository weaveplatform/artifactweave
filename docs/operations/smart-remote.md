# Smart Remote synchronization

Smart Remote repositories now discover their configured peer, copy user properties,
refresh them on expiry, report successful downloads and record confirmed source
absence. These behaviors use the existing remote source, credentials, transport and
cache controls. The controlled-peer tests prove artifactweave behavior; they do not
establish interoperability with a deployed Artifactory version. Exact contemporary
wire details remain [I] under [#216](https://github.com/weaveplatform/artifactweave/issues/216),
and P37 owns broader external-peer qualification.

Configure a repository URL that identifies one upstream repository, such as
`https://peer.example/artifactory/libs-release` or its `api/<package>/<repository>`
form, and configure that peer's credentials on the remote. The strict NuGet v3
`api/nuget/v3/<repository>/index.json` and PyPI `api/pypi/<repository>/simple`
forms are also recognized. Query-bearing or ambiguous
management roots fail explicitly. Caller Authorization is never forwarded to the peer.
Discovery reads the peer version and repository configuration using the configured
credentials; a denied configuration read may use the authenticated shared-configuration
endpoint. Unsupported versions/classes or failed negotiation fail the read; they do
not silently disable synchronization. Go requires a virtual source, and remains bound
to its P10 virtual-routing handoff.

| Setting | Behavior |
|---|---|
| `synchronizeProperties: true` | Copy initial user properties atomically with the first stored artifact. Later HTTP representations of the same artifact do not turn this into refresh. |
| `contentSynchronisation.enabled: true` | Enable authenticated capability discovery and the individually selected capabilities below. |
| `contentSynchronisation.properties.enabled: true` | Refresh properties on a normal artifact GET after `retrievalCachePeriodSecs` expires. Default is 7200 seconds; a configured zero checks on every normal GET. |
| `contentSynchronisation.statistics.enabled: true` | Record successful ordinary GET delivery, including warm reads, then submit a durable bounded count to the configured peer. HEAD and lookup-only operations do not add counts. |
| `contentSynchronisation.source.originAbsenceDetection: true` | Refresh the exact upstream item's existence on the same TTL. A confirmed miss is distinct from an outage or denied request and never deletes cached bytes. Generic repositories do not perform absence detection. |
| `shareConfiguration: true` | Permit authenticated non-admin users to read the existing sanitized remote configuration view. Anonymous callers receive 401; passwords, sensitive headers and other secret settings are omitted. |

The property snapshot is validated before artifact publication. Invalid documents,
authorization failures and property responses marked `no-store` cannot publish a
partial artifact/property update. Initial synchronization and refresh copy user
properties, excluding reserved artifactweave/Artifactory and package metadata prefixes.
A refresh replaces or removes only values still matching the previous synchronized
snapshot. A local edit or deletion takes ownership of that key for this source lifetime;
subsequent peer updates cannot overwrite or resurrect it. Ownership is coherent across
HTTP representations of one current item, and an older in-flight observation cannot
replace a newer published one. Docker/OCI/Helm OCI digest pulls and blob reads do not
synchronize tag properties.

Normal reads wait for due property/absence work; a failed refresh preserves the last
validated state and fails that read. `offline: true` and `-cache` reads serve retained
bytes without discovery, property refresh, statistics submission or any other peer I/O.
The absence timestamp remains the time of the last successful observation during an
outage. An unknown state is never reported as confirmed absent. Metadata zap expires
Smart observations while retaining binary bytes, local annotations and statistics;
a subsequent normal read may refresh them. Full deletion/restore and source or
credential changes fence old work. Artifact `no-store` and disabled local storage
create no artifact synchronization jobs.

The queue persists on SQLite and PostgreSQL. Each controller admits at most 256
pending, leased or parked keys, with two workers, shared upstream concurrency limits,
one-minute operations and at most three attempts. Repeated work for the same source,
path and representation coalesces. Exhausted refresh work stays parked; ordinary reads
do not reset its retry budget. Explicit metadata zap or a replacement source/content
allows a new attempt. Workers revalidate current user/token authority, registry grant
and realm, repository source, credentials, epoch, item identity and digest before I/O
and before publication. `X-Artifactory-Originated` chains are bounded to eight hops;
a repeated local source identity fails before another fetch.

Statistics use an at-most-once submission claim because the researched receiver has
no demonstrated idempotency contract. A durable claim is committed before the PUT.
An ambiguous reply, connection failure, crash or revoked publication authority leaves
that count uncertain and it is never replayed automatically. Later downloads can add
new counts; they cannot resend an old uncertain count. This avoids multiplying remote
statistics, at the cost of possible undercounting requiring operator reconciliation.

Administrators can inspect one exact canonical path without network access:

```sh
curl --fail --user admin \
  "$REGISTRY/artifactory/api/artifactweave/remote-status/vendor?path=org/example/tool/1.0/tool-1.0.jar"
```

This is an artifactweave diagnostic extension; standard Storage API responses are
unchanged. It returns at most 16 current representations, a `truncated` indicator,
`properties_checked_at`, `origin_checked_at`, optional `origin_absent`, exact integer
`downloads`, `claimed`, `uncertain`, and bounded work operation/state/attempt counts.
The counters are per representation; sum them only if `truncated` is false. Zero origin
time means unknown and omits `origin_absent`. JSON consumers must preserve 64-bit
integers. The endpoint rejects cache aliases, wildcard paths and extra query keys;
anonymous callers receive 401 and non-admin users 403. It returns neither credentials,
upstream URLs, raw work payloads nor local/remote property values. Source or credential
changes suppress obsolete state rather than presenting it as current.

The Linux `@smart_remote` acceptance matrix exercises real curl and pinned JFrog CLI
against a controlled peer: initial/changed properties, cached-download counts,
confirmed source removal, retained offline cache, legacy-only and disabled flags, and
an actual artifactweave self-loop. Race tests additionally cover revoked authority,
changed source credentials, no-store, malformed documents, throttling, bounded retries,
uncertain statistics and concurrent ownership publication. The [implementation
contract](../implementation/smart-remote.md) records the evidence and open wire captures.
