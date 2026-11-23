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
retained per source client.

A proxy with `domain` or `ntHost` selects NTLMv2. `domain` qualifies its configured
username; `ntHost` is the client workstation name in both NEGOTIATE and AUTHENTICATE.
The 7.161.26 `ProxyDescriptor` and `artifactory.xsd` describe these fields as the
proxy domain/realm and this machine's computer name, respectively. The resolver
requires a username, validates names and refuses an ambiguous domain-qualified
username combined with a separate domain. Credentials still come from the sealed
proxy password, independently of repository Basic/Bearer credentials.

For tunnel routes the adapter dials and verifies the proxy, then uses
`platform/outbound.NTLMProxy.Connect` for an unauthenticated CONNECT, its 407 NTLM
advertisement, NEGOTIATE, the 407 CHALLENGE, and AUTHENTICATE on that same socket.
The helper uses [Azure/go-ntlmssp v0.1.1](https://github.com/Azure/go-ntlmssp/tree/v0.1.1)
(MIT, Go 1.24, no transitive modules) for NTLMv2 message generation and cryptography;
it does not use the library's origin-authentication RoundTripper.
[MS-NTHT's proxy exchange](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-ntht/f8064832-931f-4516-9557-01630745732f)
defines the 407/Proxy-Authenticate/Proxy-Authorization sequence. Connection ownership,
checked-IP CONNECT and the following bounds are local engineering choices, not
observed Artifactory responses.

DNS-named HTTP origins and all HTTPS origins use CONNECT to the pinned IP, with
logical Host/SNI and origin mTLS unchanged. Literal-IP HTTP origins retain normal
absolute-form forwarding through HTTP or HTTPS proxies. The forward decorator uses
the same NTLM messages and existing HTTP transport, with one socket per authority;
a closed negotiation cannot redial and send its proof on another socket. GET/HEAD
use the standard 407 request replay. Body-bearing requests authenticate using a HEAD
probe, then send the original body once with automatic replay disabled; this retains
Smart Remote's non-replayable POST/PUT contract as well as admitted metadata POSTs.
The authenticated probe may reach the origin and return any origin status.

Proxy-Authorization is confined to proxy requests and stripped by the proxy before
forwarding. Origin Authorization remains the independently configured repository
identity; a trusted HTTP forward proxy necessarily sees that header, as with Basic
forwarding. CONNECT keeps origin credentials inside the tunnel. Redirect and
metadata destination policy still check the pinned address before each exchange.
Reused connections retain authentication; new connections negotiate anew. The source HMAC revision
includes domain, workstation and sealed password; rotation closes old idle tunnels
and replaces the pool. Active requests keep the existing publication fences.

Negotiation admits three authentication exchanges, bounds each response header and drains
at most 64 KiB per challenge body. A refusal, malformed or duplicate challenge,
closed socket, wrong password or interrupted negotiation fails without raw challenge
or credential data. NTLMv1, signing/sealing/key exchange, required MIC and channel
binding are explicitly refused because the pinned library does not implement them;
there is no Basic downgrade. Optional MIC/channel binding are not generated by the
library. [MS-NLMP recommends MIC with a server timestamp](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-nlmp/c0250a97-2940-40c7-82fb-20d208c71e96);
this implementation therefore does not claim NTLM message-tampering protection or
Extended Protection for Authentication. TLS still authenticates HTTPS proxies and origins.
`redirectedToHosts` grants described below apply to NTLM proxies too: they answer a
target Basic challenge, never proxy authentication. Single-replication push uses the
same `NTLMProxy.Connect` seam under its own destination rules, described in
[Single-replication push proxies](#single-replication-push-proxies-p12-1848).

Proxy `services` reads accept descriptor/provider strings split on newline, space,
comma or semicolon, as Artifactory 7.161.26's `ProxyDescriptor.getServicesList`
does, and historical string lists. Absent, null and empty services permit an
enabled platform-default proxy; a nonempty selection must contain exact `jfrt`.
Explicit references retain priority over service filtering, and `disableProxy`
bypasses selection. Invalid service shapes fail with a redacted configuration
error. Both the artifact resolver and federation push factory share this decoder;
reads preserve stored JSON and sealed passwords.

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
With an NTLM proxy `domain`, the builder registers its `NTCredentials` for the listed
hosts too, and Apache's [BasicScheme](https://github.com/apache/httpcomponents-client/blob/4.5.x/httpclient/src/main/java/org/apache/http/impl/auth/BasicScheme.java)
sends the [NTUserPrincipal](https://github.com/apache/httpcomponents-client/blob/4.5.x/httpclient/src/main/java/org/apache/http/auth/NTUserPrincipal.java)
name: the domain in root-locale upper case, a backslash, then the username (`CORP\user`).
An empty domain sends the bare username. The NTLMv2 proxy handshake is unchanged.
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
Basic/Bearer/mTLS/HTTP(S)-proxy and NTLMv2 tests prove this implementation, including sealed-store
reads, denied redirects, malicious token realms, mixed DNS answers, rotation and redaction.
The NTLM fixture independently verifies the NTLMv2 proof and domain/workstation, and
records proxy and origin requests separately without retaining proof material.
`@ntlm_proxy` in the required core native lane downloads exact cold bytes with the
pinned JFrog CLI and requires its binary rather than skipping that scenario.

## Single-replication push proxies (P12, #1848)

The federation push factory (`internal/federation/adapters/pushhttp`) selects the
explicit or platform-default proxy, applies `services` and `disableProxy` as above,
and reads the sealed password inside the metadata transaction that builds the
connection. A `domain` or `ntHost` selects NTLMv2 with the same identity rules as
remote sources: the configured username, `domain` qualifying it, `ntHost` as the
workstation, and invalid or ambiguous names refused with a redacted configuration
error. No DNS, socket or NTLM exchange happens in that transaction.

`platform/outbound.Client` already tunnels every push exchange, HTTP or HTTPS, with
CONNECT to the checked IP, so the push client calls `NTLMProxy.Connect` for each new
tunnel after dialing and verifying the proxy. Proxy TLS uses the proxy's logical
name; the target keeps its logical Host/SNI, trust and repository Basic credentials
inside the tunnel. The NTLM identity never becomes `Proxy-Authorization: Basic` and
never the target's `Authorization`. Authentication happens before the first request
byte, so a refused, malformed, interrupted, cancelled or timed-out negotiation fails
before any PUT, PATCH or DELETE reaches the target; the existing write rules still
forbid replaying a request body. Wrong credentials and refused destinations are
security failures; cancellation and socket inactivity keep their context errors.

The private connection revision adds the NTLM domain and workstation; the proxy URL
digest already holds the username and sealed password. Changing any of them, or
switching between Basic, NTLM, another proxy and direct, retires the pooled client
and its authenticated tunnels. A run bound to the old transport revision is fenced
and publishes nothing. Basic and direct revisions are unchanged by this addition.

`redirectedToHosts` is validated with the remote resolver's parser
(`outbound.ProxyRedirectHosts`): a malformed list fails truthfully, and a valid one
no longer disables proxy use. It has no other push effect. The outbound client
follows only same-origin GET/HEAD redirects, which keep the target's own credentials,
and refuses cross-origin and write redirects, so a listed host is never contacted
and proxy credentials never substitute for target credentials. The list is therefore
not part of the transport revision. **[I]** Artifactory's push replication client is
closed source; applying the descriptor's NTLM fields to it follows the shared
`ProxyDescriptor` builder that remote repositories use (research 06) and remains a
P19/#216 comparison item.

Evidence: `pushhttp` effect tests over HTTP and HTTPS proxies and origins, explicit and
default selection, Put/Inspect/Delete bytes and failure/cancellation/timeout/
reconfiguration cases; `test/server/federation` SQLite/PostgreSQL push through a
descriptor-configured NTLM proxy to a separate target process; and the two-process
`@replication_ntlm_proxy` acceptance on SQLite and PostgreSQL. An independent fixture
verifies NTLMv2 proof, domain and workstation in every case.

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
