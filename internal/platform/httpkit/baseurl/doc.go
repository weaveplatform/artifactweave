// Package baseurl resolves the public base URL a response must embed.
//
// npm tarball links, Helm chart URLs, the NuGet service index, Terraform
// download URLs, Docker upload locations and file-info URIs all carry absolute
// URLs. Behind a reverse proxy the request's own host is wrong, so the resolver
// follows Artifactory's precedence (OSS 6.20 HttpUtils.getServletContextUrl,
// research 17): the X-JFrog-Override-Base-Url header, then the configured base
// URL, then the X-Forwarded-* headers, then the request. A configured base URL
// is the operator naming the public URL, so a request header the operator does
// not control never overrides it (#313). Every handler reads the result from
// the context so there is exactly one place that implements the rule
// (decision 0002).
//
// # Role
//
// platform layer, in platform/httpkit: the public base URL of the whole HTTP
// surface, which no module owns. The router runs Middleware on every
// request, each handler that writes an absolute URL reads the result with
// From, and the Docker registry's subdomain method reads the host the client
// addressed with ForwardedHost. It imports only the standard library:
// context, net, net/http and strings.
//
// # Inputs
//
//   - The operator's configured base URL, the base_url setting, which may be
//     empty.
//   - Per request: the X-JFrog-Override-Base-Url header; the
//     X-Forwarded-Host, X-Forwarded-Proto and X-Forwarded-Port headers; and
//     the request's Host and whether it arrived over TLS.
//
// # Outputs
//
//   - Resolve: the base URL without a trailing slash, from the first source
//     that gives one: the override header, the configured base URL, the
//     forwarded headers, then the request's own scheme and host.
//   - Middleware: a handler wrapper that stores Resolve's result in the
//     request context, and From, which reads it back.
//   - ForwardedHost: the first entry of X-Forwarded-Host; OverrideHeader: the
//     name of the override header.
//
// # Invariants
//
//   - No header the operator does not control overrides a configured base
//     URL; only the override header, which JFrog's reverse-proxy guidance has
//     the proxy set, comes first. Otherwise a client that can set
//     X-Forwarded-Host would choose the Docker token realm that other clients
//     send their credentials to (#313).
//   - Each X-Forwarded-* header is a list with one entry per proxy, and only
//     its first entry, the client-facing hop, counts. An empty first entry
//     names no host, so a later, internal entry is never trusted (#249).
//   - Only http and https are taken as a forwarded scheme, and only a number
//     of up to five digits as a forwarded port. The port is added only when
//     the host has none and it is not the scheme's default.
//   - From returns "" when Middleware did not run, and callers treat that as
//     a programming error, never as a fallback.
//
// # References
//
//   - Decision 0002:
//     docs/research/decisions/0002-spec-first-flat-crud-handwritten-item-paths.md
//     ("Base URL resolver").
//   - Decision 0039: docs/research/decisions/0039-helm.md (absolute chart
//     URLs come from the resolver when relative URLs are off).
//   - Research 17: docs/research/17-undocumented-shapes.md, "Base URL behind
//     proxies" (Artifactory's precedence and its sources).
//   - Deviations DV-037 and DV-038: docs/implementation/deviations.md (the
//     legacy X-Artifactory-Override-Base-Url header is not honoured, and the
//     configured base URL is used as written).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Dependency rules": the platform imports no module.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 158 to 160 (platform/httpkit) and line 1019 (the mapping row).
//   - Plan: docs/implementation/plan.md#p0-foundations, task 5, and
//     #p9d-modular-monolith-migration, epic WEB
//     (https://github.com/weaveplatform/artifactweave/issues/944).
//   - https://github.com/weaveplatform/artifactweave/issues/249,
//     https://github.com/weaveplatform/artifactweave/issues/313 and
//     https://github.com/weaveplatform/artifactweave/issues/1000
//
// # Status
//
// Implemented P0: the base URL resolver middleware of plan task 5. Two fixes
// followed: the first entry of each comma-separated X-Forwarded-* header
// (#249), and the configured base URL ahead of the forwarded headers, as in
// Artifactory (#313). Moved here in P9d.
//
// # Testing
//
//   - TestUnitResolvePrecedence: the order of the four sources, trimming,
//     proxy-chain lists, empty first entries, IPv6 hosts, ports already in
//     the host, default ports, and an unknown scheme or a port that is not a
//     number.
//   - TestAccMiddlewareAndFrom: Middleware stores the base URL that From
//     reads, and From on a bare context returns "".
//   - TestAccForwardedHost: the first, trimmed entry of X-Forwarded-Host.
//
// Through the server, TestAccURLsBehindAProxyChain and
// TestAccConfiguredBaseURLWinsOverForwardedHeaders check the URLs that
// responses embed, and the Docker registry's TestAccSubdomainFromForwardedHost
// checks the subdomain method.
package baseurl
