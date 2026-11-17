# Go upstream source and P10 handoff

Issue #633 implements `golang.GoSource` below HTTP routing. Construct it with the
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
`kernel.ErrNotFound` and the ordinary negative-cache clock. It changes no other
protocol's 410 behavior. Authorization refusal, 401/403, throttling, transport faults,
invalid bodies and 5xx remain typed failures; they cannot become a missing member.

A P10 consumer must retain `Result.Revalidate` and run it immediately before delivery.
It calls `Result.Used` only after successful content delivery, closes every returned
stream, and preserves the original project and visited incarnation chain. A cache-only
read never enables upstream traffic. Any future virtual fallback policy must explicitly
distinguish absence from failures and cannot silently contact a VCS or public proxy.

## Evidence and remaining work

`internal/artifacts/ecosystem/golang/remote*_test.go` fetches every endpoint
through the real source and cache policy against controlled HTTP upstreams. It covers
escaped identities, exact bytes/digests, pseudo-versions, mutable metadata, immutable
files, negative TTL, 404/410 versus failure, private credentials, repository
isolation, permission and credential fencing, cancellation, interrupted/invalid
content and warm/offline reads. The existing
`internal/artifacts/ecosystem/golang/http` class gate retains 501 for direct remote
reads and virtual PUT. GOV-01 registers the remote source only for virtual member
reads and unions local/federated lists with authoritative upstream lists. Duplicate
versions keep the first member's line; list ordering and cross-member latest selection
are inferred cases for #216/P19. Member faults that the shared package walk passes over
are logged with their type and produce the virtual's 404 when no member serves it.

`test/acceptance/features/go.feature` now proves native `go mod download` and
`go build` through a virtual with an empty client cache, mixed local and private remote
dependencies, case escaping and a pseudo-version. After stopping the upstream and
clearing the client's module cache, the build succeeds from the remote cache. GOPROXY
names only artifactweave; no direct/public proxy or VCS fallback is configured.
`test/server/artifacts/golang/virtual_test.go` covers merged versions, latest, duplicate
precedence, missing modules/versions and the direct-remote, virtual-PUT and sumdb guards.
Publishing through the virtual's default local repository remains GOV-02.

Sumdb proxying and VCS-backed Go remotes remain P28 capabilities, with VCS machinery
shared with P33. Disabling checksum verification in an isolated private-module fixture
cannot qualify sumdb. The virtual-client evidence above qualifies GOV-01 reads.

These tests prove artifactweave source behavior. Exact Artifactory response envelopes,
`.info` freshness and cache/permission behavior remain inferred comparison cases for
#216/P19; ADR0035 retains its existing status.

## GOV-01 before/after evidence

The same router probes ran on main `dcd46bf91` and the GOV-01 branch against
controlled members (no Artifactory instance). Native commands used the isolated
scenario's `GONOSUMDB=*`, `GOTOOLCHAIN=local`, `GOFLAGS=-modcacherw` and fresh caches.

| Request through the Go API | Main | GOV-01 |
|---|---|---|
| Virtual list, latest, version info/mod/zip | 501 | 200 |
| Virtual missing module / missing version | 501 | 404 with module/version text |
| Direct remote read | 501 | 501, same body |
| Virtual PUT | 501 | 501, same body |
| Virtual sumdb probe | 404 | 404 |

`@go_virtual` passes `go mod download`, `go build -mod=mod .`, then
`go clean -modcache` and another build with the private upstream stopped. Its ZIP
request count remains one across both builds. This establishes native virtual read
resolution; exact 7.x comparisons remain the inferred cases above.
