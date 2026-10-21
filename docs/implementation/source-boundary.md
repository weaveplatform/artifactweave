# Remote source read boundary

[#616](https://github.com/weaveplatform/artifactweave/issues/616) implements the
shared read seam from the [project-source design](project-remote-sources.md) and
[#613](https://github.com/weaveplatform/artifactweave/issues/613)'s retrieval contract.
The application installs this boundary for direct generic downloads through
[#621](https://github.com/weaveplatform/artifactweave/issues/621), composing transport,
credentials, ingestion and policy. Native protocols retain their existing class gates
until their P9 tickets. Operational behavior is documented in
[generic remote repositories](../operations/remote-repositories.md).

## Contracts and ownership

`internal/source` imports no server package and does not receive a transaction or
format write-hook environment. `pkg/types/source.go` defines durable, secret-free
`RepositoryIncarnation`, `SourceGeneration` and tagged `SourceIdentity` records;
[#619](https://github.com/weaveplatform/artifactweave/issues/619) owns allocation,
persistence and atomic publication fences. Reusing a repository key or changing a
configuration away and back must not revive old source work.

`Service.Read(context.Context, Request)` applies path validation, exact Read/token
permission, blackout and include/exclude rules before identity lookup or backend work.
The route has already resolved repository existence. Requests carry the decoded logical
path and a separate escaped upstream-relative path; the two must name the same resource.
Only selected representation headers are admitted. Downstream conditional headers are
not upstream validators. `Resource.Mutable` distinguishes refreshable binary names
from metadata so TTL refresh does not imply metadata-only zap. Locally derived
content remains a format-classifier responsibility. Original project and visited repository incarnations are
explicit fields for later composition, never caller-controlled HTTP headers.

The service derives `Permit{Identity, Intent, UpstreamNetworkAllowed}`. Normal external
reads use `MayFetch`, independent of client Deploy permission. `CacheOnly` selects the
separate cache backend and cannot fall back to HTTP. The only registered kind is
`ExternalHTTP`; `ProjectRepository` and unknown kinds fail explicitly before cache or
network access. Project share authorization and network-restricted traversal remain
P11, with their concrete boundary in the [design](project-remote-sources.md).

A `Backend.Read(context.Context, Request, Permit)` implements retrieval policy. The
HTTP backend in [#620](https://github.com/weaveplatform/artifactweave/issues/620)
composes the transport and cache; the cache backend never performs upstream work.
`Service.Resolve` must return the durable identity corresponding to the supplied
configuration snapshot, failing on a changed snapshot. `Service.Revalidate` rechecks
current policy before lookup and after a wait/fetch. It is optional for isolated tests;
production wiring supplies it. `Result.Revalidate` additionally fences the backend
credential revision immediately before delivery. The service rechecks identity before handing content
to its caller; the backend independently checks identity and path epoch inside its
short publication transaction. Nothing holds a SQL transaction across network or body I/O.

## Results, errors and delivery

A `Result` contains exactly one of:

- `Item`: a committed, non-folder item in the consumer repository's cache at the
  requested logical path. Existing blob delivery, checksum, range and redirect handling
  remain available.
- `Stream`: a transient representation with its own body, status and safe representation
  metadata. No fake item or durable blob reference is needed for no-store reads.

The successful caller owns the body. The service closes rejected, canceled and
superseded bodies, and arranges closure when an active request is canceled. The HTTP
adapter closes successful streams on every path, including HEAD/304. A short or broken
body aborts the HTTP response so an unknown-length partial artifact cannot appear to
finish successfully. Rewritten metadata must supply validators for its emitted bytes.
`Result.Used` optionally records successful delivery through an opaque backend token;
[#621](https://github.com/weaveplatform/artifactweave/issues/621) wires that feedback to
the implemented [cache maintenance](remote-cache-maintenance.md) activity clock (#622).
A lookup, HEAD, 304 or failed stream must not call it; no-store results leave it nil.

A confirmed missing resource is `store.ErrNotFound`. Other failures cannot become
negative entries. `HTTPError` retains only an approved 401/403/429/5xx status, failure
classification and a Retry-After duration capped at 300 seconds. Unknown failures are
sanitized; URLs, credentials, cookies, upstream bodies and challenges are not exposed.
The router renders its existing protocol envelope. An upstream 401 does not challenge
the caller to authenticate to artifactweave. These exact upstream status choices remain
[#613](https://github.com/weaveplatform/artifactweave/issues/613)'s inferred RC-2/P19
capture obligation, not new observations of a vendor instance.

## Adapter gates and remaining work

Direct generic item GET/HEAD can use an injected service. Other package types do
not gain HTTP fetching from this injection; their adapters must opt in separately. Explicit `-cache` aliases, storage
queries and recursive virtual member lookup retain their existing cache-only paths;
installing the service cannot implicitly enable P10 fetching. Local and federated reads
and all client refusals to deploy to a remote/cache are unchanged. Matrix filters apply
to stored properties; transient content has no properties and cannot satisfy a mandatory
property filter. `Resource.ChecksumOnly` authorizes the underlying artifact while
requesting a bounded, transient cold SHA-256 sidecar. Upstream folder browsing remains #635.

`pkgroute.Remote` extends the existing per-class `Source` map with stream-aware
`Kit.Read` and `ServeItem`. Legacy `Item` cannot return a transient body and explicitly
refuses that operation. A native metadata adapter supplies a `Metadata` or `Versions`
resource to `Read`; it must not synthesize authoritative upstream lists from cached
artifacts. No built-in protocol registers `Remote` here. Docker retains its separate
routing seam; P10 must compose source permissions and intent deliberately, and P11
adds project graph and sharing checks without administrator loopback HTTP.

Tests in `internal/source/source_test.go`, `internal/server/source_test.go` and
`internal/server/pkgroute/source_test.go` exercise the service and router through
injected backends: permission/pattern/path refusals before work, explicit intent,
representation validation, generation/revocation races, safe protocol errors, stream
ownership, local/federated preservation, cache/virtual isolation and an unrelated store
write completing while a fetch is blocked. These prove the boundary, not a production
fetcher by themselves. `internal/app/remote_test.go` and the generic remote acceptance
feature additionally prove production wiring, native curl/JFrog CLI downloads, restart
identity, signed delivery, cache visibility and revocation before publication.
