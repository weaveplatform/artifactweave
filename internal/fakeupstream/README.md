# Controlled upstreams

`fakeupstream` is a chi HTTP/HTTPS test server for remote transport, cache and adapter
contracts. It uses loopback listeners and no public registry. `New` registers cleanup;
`Close` also interrupts blocked handlers. Store ecosystem documents alongside their
adapter tests rather than adding npm, Maven or other schemas to this package.

```go
clock := fakeupstream.NewClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
upstream := fakeupstream.New(t, fakeupstream.Options{Now: clock.Now})
upstream.Set("/artifact.bin", fakeupstream.Resource{Body: []byte("version one")})
upstream.Set("/index.json", fakeupstream.Resource{
    Body: []byte(`{"versions":["1.0.0"]}`),
    Header: http.Header{"Content-Type": {"application/json"},
        "Cache-Control": {"max-age=60"}},
})
// Configure the application repository with upstream.URL. Give the same clock.Now
// to the cache. Configure the native client only with the application endpoint.
// After each client operation, assert upstream.Count("GET", "/index.json").
clock.Advance(61 * time.Second)
upstream.Set("/artifact.bin", fakeupstream.Resource{Body: []byte("version two")})
```

`Resource` snapshots copy their body and headers. A request already using an older
snapshot completes with those bytes even if `Set` or `Delete` runs concurrently.
`Modified` defaults to the injected time and `ETag` to the quoted SHA-256. Normal
responses use `http.ServeContent` for HEAD, conditional requests and byte ranges.
`Header` supplies MIME, cache controls, cookies, checksum headers or deliberately bad
values; malformed metadata is simply a malformed `Body`. Explicit `Status` returns
that status and body, including 401/403/404/429/5xx. `Redirect` points to another
fixture and defaults to 302; set `Status` for another redirect code.

`Fault.BeforeHeaders` and `Fault.BeforeBody` take a zero-value `Barrier`. Wait for
`Reached()` before inspecting counts, replacing a resource or cancelling a request;
`Release()` releases all waiters. A body barrier flushes headers before waiting.
Barriers observe cancellation, and server cleanup releases blocked handlers through
their contexts. No network timing depends on sleeps. `Clock` controls timestamps,
not timers. `DropConnection`, `TruncateAfter` and `LengthDelta` exercise dropped,
partial and incorrectly sized transfers. HTTP clients may retry a dropped request on
an existing connection; use a fresh transport when an exact single attempt matters.

`Options.Auth` supports Basic or Bearer challenges. `Options.TLS` enables HTTPS;
`Client()` trusts the server certificate, and `Certificate()` exposes its public
certificate for the transport under test. `ClientIdentity(t)` returns a client
certificate and CA pool for `tls.RequireAndVerifyClientCert`. Test missing and
untrusted certificates as well as success. `NewProxy(t, auth, upstream...)` supports
HTTP forwarding and HTTPS CONNECT, records requests, strips proxy credentials from
forwarded HTTP requests and refuses destinations outside the supplied fixtures.
Use an `http.CookieJar` to exercise the returned `Set-Cookie` headers.

`Requests()` returns independent redacted snapshots. Query values, credentials,
cookies and unknown header values are redacted; bodies and URL userinfo are never
recorded. `HeaderMatches(name, value)` checks a known candidate using a keyed hash
without putting its plaintext in assertion output. Register fixture secrets embedded
in paths or ordinarily visible headers in `Options.Secrets`. Counts include denied,
missing and interrupted requests. Empty method/path arguments mean all requests;
counts never reset implicitly. Closing a server twice is safe, and closing a proxy
closes its live tunnels.

For native-client acceptance, use a fresh client cache and only the application URL.
Run a first fetch, a cache hit and a forced expiry/mutation; assert upstream counts
and downloaded hashes at each step. A direct registry fallback or a populated client
cache must fail those assertions. `TestApplicationEndpointOnly` checks this fixture
wiring; `TestMutableArtifactAndMetadata` checks expiry, mutation, interruption and
retry for both fixture kinds. Actual ecosystem/native remote acceptance belongs to
the adapter tickets, after their production handlers exist. This harness is not a
claim about a live Artifactory response.
