# Remote HTTP transport

The shared HTTP client for P9 lives in `internal/artifacts/adapters/upstream`, the
transport adapter behind core remote's `Upstream` port. It implements the transport
portion of [#617](https://github.com/weaveplatform/artifactweave/issues/617); source
authorization, durable cache decisions and native adapters remain separate layers.
The [retrieval contract](remote-retrieval-contract.md) defines their ordering.

`Client.Do` sends a GET or HEAD and returns an owned response, including error statuses.
The caller closes every response body. `Fetch` performs HEAD before a GET unless
`bypassHeadRequests` is enabled. A HEAD 405/501 falls back to GET; a 304 or another error
status is returned to the cache policy. GET headers and bytes are authoritative after
preflight: a changing upstream cannot make a HEAD length or checksum validate another
body. An explicit caller HEAD remains HEAD and cannot publish a complete artifact.

Paths are escaped relative paths. Encoded package slashes and spaces retain their
spelling. Dot segments, including escaped traversal, absolute paths and userinfo are
rejected. A request's matrix string (`Request.Matrix`) is checked by its own rule: it
may carry `%5C`, `%00`, `%0D` and `%0A`, as Artifactory sends them, but no decoded piece
between `/`, `\` or NUL that reads as a dot segment, alone or before a `;`, `?` or `#`;
the path before it is checked as any path. Repeated separators are normalized unless
`disableUrlNormalization` is set.
Raw query order and escapes are preserved: source query, configured `queryParams`, then
client query when `propagateQueryParams` is enabled. Adapters must not treat metadata
download URLs as ordinary relative paths or append these queries to signed URLs.

The socket timeout bounds dialing, TLS handshake, response headers and inactivity while
reading or writing. HTTP/1 connections keep that inactivity deadline independent of
other requests; this client does not multiplex HTTP/2 streams. Metadata requests also
have a total deadline covering both preflight and response body. Closing a body releases
its timer. Caller cancellation always applies, including when a configured timeout is
zero. Response headers default to a 1 MiB limit. Artifact bodies are streamed; the cache
and parser layers own body-size and integrity limits.

The client uses only its resolved proxy, never ambient proxy environment variables.
`disableProxy` bypasses the resolved proxy. `localAddress` binds a literal source IP.
The credential resolver supplies TLS trust/client identity, proxy configuration,
request preparation and redirect destination checks. Redirects are bounded to ten
requests, reject HTTPS downgrade and strip custom/credential headers on origin changes
before invoking the credential layer. Errors retain cancellation, deadline, connection,
security and redirect classifications without including URLs or signed query values.

`Pool` keys connections by repository and a digest of its configuration plus the
resolver's opaque revision. The revision must change when a referenced proxy, certificate
or credential changes. Replacement closes old idle connections; active work remains
owned by caller contexts and the cache generation fence. Repository delete handlers
remove their pools, and the application closes the pool after draining requests.

Tests use the deterministic loopback upstream/proxy harness. They verify HTTP and HTTPS
proxy routing, local binding, query/escape behavior, conditional and bypass counts,
redirect isolation/loops, untrusted certificates, bounded headers, metadata cancellation,
short bodies, configuration changes and lifecycle ownership. These are implementation
tests, not observed Artifactory responses; the contract's P19 capture cases remain open.

## Resolved source security (#618)

`NewResolver(store, keyring, ResolverOptions)` resolves each request's authoritative
repository configuration, sealed secrets, selected/default proxy and client-certificate
alias. `Options(ctx, config)` accepts an otherwise current masked configuration; stale
non-secret settings fail closed. It performs store reads and local parsing only, so a
publication fence can call it with the cache transaction's context and store. DNS is
resolved when sending requests. The ordinary auth modes are Basic and Bearer (password
as token). The separate [registry authentication client](remote-registry-authentication.md)
implements #623 challenge/token exchange without adding registry branches to this transport.

All caller headers except representation/range/validator headers are discarded. Only
configured repository headers and credentials are applied. Custom headers cannot override
Host, hop-by-hop framing, proxy auth or cookies. An explicitly configured Authorization
header follows the same origin policy; configured Basic/token credentials take precedence. Same-origin redirects retain configured auth. `allowAnyHostAuth=true`
permits repository auth, configured headers and client identity at another permitted
HTTPS authority; it grants neither private-network access nor downgrade permission.
HTTPS-to-HTTP requests fail closed, including metadata URLs. Enabled cookies use a
public-suffix-aware jar per client/source revision with normal domain/path/secure rules;
cookies are not scoped to a port. Caller cookies never seed that jar.

`Request.ExternalURL` is the shared entry point for untrusted metadata downloads and
future token realms. It rejects simultaneous relative `Path`/`Query` and preserves the
external URL's escaped path and raw signed query. Configured origin, explicit repository
endpoint URLs, configured proxy and trusted `AllowedPrivateOrigins` permit private
addresses at those exact authorities. Untrusted destinations require public addresses;
all DNS answers are checked, including IPv4-mapped IPv6. Link-local, unspecified,
multicast and known cloud-metadata addresses/hostnames are denied even when configured.
Checked addresses are used directly, so another DNS lookup cannot change the destination.

HTTP and HTTPS proxies use separately resolved Basic credentials. HTTPS proxy TLS does
not receive the repository's client certificate. Literal-IP HTTP destinations use forward
proxying; HTTPS destinations use CONNECT. DNS-named HTTP destinations also require
CONNECT so a checked IP can be pinned while the origin still receives its logical Host.
Proxies must permit CONNECT to that configured port. At most 32 authority pools are
retained per source client. NTLM/domain/ntHost currently fail closed with a redacted
configuration error; #1835 owns that implementation. Redirected Basic proxy credentials
are described below. P12 retains broader consumer compatibility (#1848), as recorded in the
[capability ledger](capability-roadmap.md#work-already-owned-by-p9p19).

### Redirected proxy credentials (#1836)

`redirectedToHosts` authorizes reuse of the **proxy** username/password at an exact
redirected hostname and the configured proxy port. It does not copy repository
Authorization. The list accepts newline, ASCII space, comma and semicolon separators;
DNS labels are validated after full root-locale lowercasing, preserving Kelvin-sign
to `k` while refusing dotted `İ`, whose lowercase retains a combining dot.
DNS names and IP literals compare as case-insensitive host strings;
a terminal dot or different IP spelling does not grant an alias. URL brackets delimit IPv6 syntax and
are not part of its hostname key. URLs, userinfo, explicit port tokens, wildcard
patterns, invalid labels and other embedded whitespace fail resolution with a redacted
configuration error.
Only edge characters through U+0020 are trimmed; Unicode whitespace remains invalid.
An empty list disables reuse. Default URL ports resolve to 80/443 before matching.

For an actual redirect, a bodyless GET/HEAD receiving a valid `401 WWW-Authenticate:
Basic` challenge gets one retry with proxy Basic credentials in **Authorization**.
`Proxy-Authorization` continues to authenticate only to the configured proxy, including
CONNECT; a 407 does not supply target credentials. A configured repository Authorization
value takes precedence, and `NoCrossOriginAuth` forbids this reuse. Initial requests and
metadata URLs do not acquire the redirect grant. Unsupported or malformed challenges
remain unsuccessful responses, without an authentication retry or alternate mechanism.
HTTP-to-HTTP is supported; HTTPS-to-HTTP remains refused at every redirect hop.

Every attempt rechecks DNS/IP policy and pins the approved address. The host list grants
no private or metadata-address reachability, TLS client identity, custom repository
headers or cookie permission. Cookies retain their ordinary domain/path/secure rules,
including same-host eligibility across ports. Proxy secret/list changes rotate the HMAC
source revision and its client/cookie state. An in-flight old revision can finish its
network exchange, but the existing current-authority publication fence refuses its
artifact and retrieval entry after reconfiguration.

Primary evidence: the official [7.161.26 source archive](https://releases.jfrog.io/artifactory/bintray-artifactory/org/artifactory/oss/jfrog-artifactory-oss/7.161.26/jfrog-artifactory-oss-7.161.26-sources.tar.gz)
contains `ProxyDescriptor`, the XSD and `HttpRepo.createHttpClient`. Its parent chain
resolves [jfrog-http-client 13.21.0](https://releases.jfrog.io/artifactory/jfrog-jars/org/jfrog/common/jfrog-http-client/13.21.0/jfrog-http-client-13.21.0.jar).
The inherited builder registers listed hosts at `proxyPort`, `ANY_REALM`; the deprecated
static server helper instead uses `ANY_PORT` and is not the remote's call path.
[Research 06](../research/06-system-configuration-and-descriptor.md#proxy-redirect-credential-contract)
records the dependency chain and source distinctions. Challenge-only reuse, strict token
validation, actual-redirect scope, repository precedence and bodyless bounded retry are
explicit local choices. Source/bytecode establishes scope registration, not a live vendor
observation. The controlled HTTP/HTTPS, DNS, mTLS and SQLite/PostgreSQL race suites and
required `@jf @generic_remote @proxy_redirects` native scenario prove local effects;
P19/#216 comparative qualification remains separate.

System roots plus explicit PEM trust verify server identities. The opaque revision is
an HMAC under a derived master key over source settings and resolved proxy/certificate/
trust material. Referenced-secret changes rotate the pool and cookie jar even if the
repository row is unchanged. Retrieval TTLs, offline/hard-fail/no-store/access-pattern
policy are excluded: the cache uses a separate policy fence and must retain valid
immutable bytes. Source revision, repository incarnation, source generation and request
representation all belong in cache identity; the resolver alone is not authorization.

JFrog documents [Bearer, lenient host auth, cookies and client certificates](https://docs.jfrog.com/artifactory/docs/remote-repositories)
and [proxy settings](https://docs.jfrog.com/administration/docs/manage-proxy-servers).
Our HTTPS-only cross-origin opt-in, universal metadata-address denial, DNS pinning and
reserved-header rules are deliberate security restrictions, not observed Artifactory
responses. RC-6/#216 retains comparative captures for these differences. The loopback
Basic/Bearer/mTLS/HTTP(S)-proxy tests prove this implementation, including sealed-store
reads, denied redirects, malicious token realms, mixed DNS answers, rotation and redaction.

## Virtual origin requests (P10, #1542)

Every repository upstream request sends `X-Artifactory-Originated` with the incoming
trace followed by this process's host identity, and `Origin-Artifactory` with that
identity. Both names are reserved: configured headers cannot override them. These
markers grant no permissions. Smart Remote adds its own node/repository trace and
retains its separate eight-hop loop guard.

When either marker is present, a virtual whose
`artifactoryRequestsCanRetrieveRemoteArtifacts` is false (the default) searches
remote caches without contacting upstreams, including when cached copies have aged
out. The requested virtual's flag governs the whole walk. Native Swift, Terraform
and Composer routes are exempt; item routes apply the restriction to every type.
An item download with more than one origin value including this host answers 404
`Recursive call detected for '<request URI>'. Returning nothing.` before lookup.

Source: Artifactory 7.161.26 `HttpRepo.addDefaultHeadersAndQueryParams`,
`HttpArtifactoryRequest.isRecursive` and `VirtualRepositoriesResolver.addRemoteRepo`;
decision 0008 R7. **[I], P19/#216:** the host identity is 24 hexadecimal characters
from a SHA-256 hash of process-random identity material. It lasts until process
restart, matching the lifecycle of the source's permitted JVM VMID; no configured
node identity exists yet. The native-route exemptions remain inferred.

Concurrent retrievals with different origin traces use separate in-flight work,
while retaining the same persistent cache keys. This prevents a returning hop
from waiting on its own original fill before the recursion guard can answer.

`TestAccVirtualOriginLoopbackChain` exercises real loopback HTTP requests with
the flag disabled and enabled, then a recursive source. `TestUnitOriginTrace`
checks both markers and reserved-header assembly; the transport and Smart suites
cover hook coexistence. Ordinary requests keep their existing retrieval behavior.
