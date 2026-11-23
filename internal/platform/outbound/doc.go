// Package outbound supplies fixed-origin HTTP transport and shared destination
// address policy without importing a business module.
//
// # Role
//
// Platform network infrastructure. Business adapters retain endpoint, credential,
// request-body and response semantics; this package owns DNS/IP pinning, proxy
// isolation, TLS, socket inactivity and redirect boundaries.
//
// # Inputs
//
// Immutable trusted destination/proxy origins, an optional DNS resolver and
// root certificate pool, socket timeout, an optional NTLMProxy identity, and
// caller-owned request contexts. NewPolicy, NewClient, NewNTLMProxy and
// ProxyRedirectHosts do no network I/O. Operators explicitly configure
// private origins; metadata/link-local destinations remain forbidden.
//
// # Outputs
//
// Policy.Addresses returns only checked physical addresses. Client.Do transfers
// response-body ownership on success; Close stops new requests and releases idle
// connections. NTLMProxy.Connect negotiates an already dialed, checked proxy
// connection and transfers the authenticated tunnel to its caller; a Client
// with an NTLM identity calls it for every new CONNECT tunnel. ProxyRedirectHosts
// returns canonical redirectedToHosts keys, which grant no reachability.
// SocketTimeoutConn and IdleConnTimeout carry the socket inactivity timeout
// for Client and for the remote repository client, which builds its own
// transport.
// Errors retain safe classifications and context cancellation,
// never URLs, headers, bodies or credentials.
//
// # Invariants
//
// Every exchange checks all DNS answers before using a pinned address. A proxy
// cannot perform a second unchecked destination resolution; its authentication
// and TLS identity stay separate from the destination's. Mutating requests never
// follow redirects or replay a body after a failed exchange; GET/HEAD may follow
// only same-origin redirects. A request on a reused connection is timed from
// its own write, never from when the connection went idle, so net/http never
// sees a stale timeout it would replay (#1866). Business adapters must build scoped request URLs.
//
// # References
//
//   - docs/research/decisions/0044-modular-monolith-migration.md.
//   - https://github.com/weaveplatform/artifactweave/issues/1761.
//   - https://pkg.go.dev/net/http#Client.Do.
//
// # Status
//
// Shared address rules and fixed-origin transport for P11 single push, plus
// connection-scoped NTLMv2 CONNECT negotiation consumed by the P12 remote adapter
// and, through ClientOptions.NTLM, by single push (#1848).
// Remote-cache adapters retain their broader protocol-specific request policy.
package outbound
