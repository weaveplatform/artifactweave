# Remote HTTP transport

The shared HTTP client for P9 lives in `internal/remote`. It implements the transport
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
rejected. Repeated separators are normalized unless `disableUrlNormalization` is set.
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
retained per source client. NTLM/domain/ntHost and `redirectedToHosts` currently fail
closed with a redacted configuration error; P12 owns these proxy/global-configuration
behaviors and broader proxy compatibility, as recorded in the
[capability ledger](capability-roadmap.md#work-already-owned-by-p9p19).

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
