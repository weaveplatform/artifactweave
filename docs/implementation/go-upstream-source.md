# Go upstream source and P10 handoff

Issue #633 implements `remote.GoSource` below HTTP routing. Construct it with the
application's existing authority-fenced `source.Service`, then pass the resolved
remote repository, current subject/access, project and visited source incarnations.
The wrapper rebuilds a trusted resource from the requested Go path; callers cannot
supply an external target or broaden permissions through resource fields.

| GOPROXY endpoint | Source classification | Validation |
|---|---|---|
| `module/@v/list` | Authoritative versions; metadata TTL | Canonical versions, optional RFC3339 timestamps, 4 MiB bound |
| `module/@latest` | Authoritative metadata; metadata TTL | Canonical version and `.info` schema |
| `module/@v/version.info` | Authoritative metadata; metadata TTL | Matching version, time and bounded JSON |
| `module/@v/version.mod` | Immutable artifact | Parsed module identity, Go's go.mod size bound |
| `module/@v/version.zip` | Immutable artifact | Go module ZIP rules, identity, paths, collisions, size and entry CRCs |

Case-decoded module identities are used for permissions and cache paths; the upstream
request uses Go's case-escaped module/version syntax. `.info` extension fields and
all accepted upstream bytes are preserved. Lists are never generated from the subset
of downloaded modules. ZIP validation stages a bounded private copy outside SQL and
extracts no files. Rejected or interrupted bodies cannot populate the cache, including
when persistence is disabled. Private credentials, origin isolation, checksum policy,
TTL, negative caching, offline/no-store, publication fencing and maintenance use the
shared source/transport/cache implementation.

## Errors and delivery obligations

The [Go GOPROXY protocol](https://go.dev/ref/mod#goproxy-protocol) treats upstream
404 and 410 as absence. The trusted `Resource.GoneIsMissing` opt-in maps both to
`store.ErrNotFound` and the ordinary negative-cache clock. It changes no other
protocol's 410 behavior. Authorization refusal, 401/403, throttling, transport faults,
invalid bodies and 5xx remain typed failures; they cannot become a missing member.

A P10 consumer must retain `Result.Revalidate` and run it immediately before delivery.
It calls `Result.Used` only after successful content delivery, closes every returned
stream, and preserves the original project and visited incarnation chain. A cache-only
read never enables upstream traffic. Any future virtual fallback policy must explicitly
distinguish absence from failures and cannot silently contact a VCS or public proxy.

## Evidence and remaining work

`internal/remote/golang*_test.go` fetches every endpoint through the real source and
cache policy against controlled HTTP upstreams. It covers escaped identities, exact
bytes/digests, pseudo-versions, mutable metadata, immutable files, negative TTL,
404/410 versus failure, private credentials, repository isolation, permission and
credential fencing, cancellation, interrupted/invalid content and warm/offline reads.
The existing `internal/server/golang` class-gate test continues to require 501 for a
direct native remote or virtual route. P9 source completion does not open those routes.

P10 must wire this source into a Go virtual repository over local and remote members,
then prove native `go mod download` and `go build` with an empty client cache. The proof
must include mixed local/remote dependencies, case escaping and pseudo-versions,
private credentials, warm/offline resolution and failure handling. Configure GOPROXY
only to artifactweave and show there is no direct/public fallback or VCS contact.
Publishing through the virtual's default local repository remains a separate P10 exit.

Sumdb proxying and VCS-backed Go remotes remain P28 capabilities, with VCS machinery
shared with P33. Disabling checksum verification in an isolated private-module fixture
cannot qualify sumdb. Native virtual-client evidence remains pending until P10.

These tests prove artifactweave source behavior. Exact Artifactory response envelopes,
`.info` freshness and cache/permission behavior remain inferred comparison cases for
#216/P19; ADR0035 retains its existing status.
