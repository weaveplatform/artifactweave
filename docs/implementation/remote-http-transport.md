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
