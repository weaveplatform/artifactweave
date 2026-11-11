// Package upstream is the outbound HTTP of remote repositories: the client
// and its pool, credential, cookie, client certificate and proxy resolution,
// destination and TLS security, and the Docker registry client with its
// challenge and token exchange. It sends what core remote decides to fetch
// and never decides whether to fetch or what to cache.
//
// # Role
//
// artifacts module, adapter layer: outbound HTTP to remote origins. It
// implements core remote's ports, remote.Upstream (Pool) and
// remote.Registries (RegistryClients), which the application binds; core
// remote and Smart Remote reach origins only through those ports and never
// import this package (decision 0044, dependency rule 8).
//
// # Inputs
//
//   - Driving: the shared remote policy, Smart Remote and the format slices'
//     upstream rules, through remote.Upstream and remote.Connection
//     (Fetch, Do, and for Smart's own requests Guarded, CheckURL, Prepare
//     and Send); the Docker rules through remote.Registries; and the
//     application, which builds the pools and the resolver.
//   - The repository configuration, the resolved remote.Options and each
//     remote.Request, and the request-scoped remote.Outbound hooks, which Do
//     applies to the first request and its redirects.
//   - Driven: the resolver's store, ResolverStore (repository
//     configurations and their sealed credentials, headers and cookies, and
//     the proxy configuration documents), the keyring that
//     unseals them and derives the credential revision, and the access API's
//     Keys, which hold the client certificates a remote names.
//
// # Outputs
//
//   - Pool, the remote.Upstream of ordinary remotes, with Client and
//     NewClient; Resolver, NewResolver and ResolverOptions, which turn a
//     repository into its remote.Options and credential revision, and
//     ResolverStore, the store a Resolver reads.
//   - RegistryPool, Registry, NewRegistry and RegistryOptions, the Docker
//     registry clients, and RegistryClients, the remote.Registries over
//     them; ErrRegistryChallenge.
//
// # Invariants
//
//   - DNS answers are validated together and pinned: the transport dials
//     the addresses it checked, so a second lookup cannot swap a public
//     address for a private one.
//   - Private networks are reachable only for authorities the operator
//     configured, and known cloud metadata endpoints are always denied.
//   - Credentials go to another origin only with allowAnyHostAuth and
//     HTTPS; a caller's own Authorization, API keys and cookies are never
//     forwarded.
//   - The caller owns every response body that a successful Fetch, Do or
//     Send returns, and closes it.
//   - Registry tokens are isolated by source identity, credential revision,
//     image, service and scope, and never follow a redirect to a CDN or to
//     another image.
//   - The remote: sentinel texts, ErrRegistryChallenge's text and the
//     redirect, proxy and destination behaviour stay byte-identical.
//
// # References
//
//   - Decision 0007: docs/research/decisions/0007-remote-repositories.md
//     (transport #617, source security #618, registry authentication #623:
//     https://github.com/weaveplatform/artifactweave/issues/617,
//     https://github.com/weaveplatform/artifactweave/issues/618 and
//     https://github.com/weaveplatform/artifactweave/issues/623).
//   - docs/implementation/remote-http-transport.md and
//     docs/implementation/remote-registry-authentication.md.
//   - Decision 0017: docs/research/decisions/0017-sealed-columns-and-master-key.md
//     (sealed remote passwords, headers and proxy passwords).
//   - Decision 0044: docs/research/decisions/0044-modular-monolith-migration.md,
//     "Remote is split fully" (the ports live in core remote; url.go and
//     destination.go are split; the registry client is here).
//   - JFrog remote repository and proxy documentation:
//     https://docs.jfrog.com/artifactory/docs/remote-repositories and
//     https://docs.jfrog.com/administration/docs/manage-proxy-servers.
//   - Target layout: docs/research/architecture-options/5-modular-monolith.md,
//     lines 254 to 256 (adapters/upstream), lines 647 to 652 (its file list)
//     and line 1013 (the mapping row).
//   - Plan: docs/implementation/plan.md#p9-remote-repositories and
//     #p9d-modular-monolith-migration, epic RMT
//     (https://github.com/weaveplatform/artifactweave/issues/942).
//   - https://github.com/weaveplatform/artifactweave/issues/1026 and
//     https://github.com/weaveplatform/artifactweave/issues/1091
//
// # Status
//
// Implemented P9. Moved here in P9d, which narrowed the resolver's store to
// ResolverStore (https://github.com/weaveplatform/artifactweave/issues/1091).
//
// # Testing
//
//   - client_test.go and client_guard_test.go: fetches and validators, HEAD
//     fallback, URL preservation, proxies, TLS and binding, redirects,
//     cancellation and deadlines, the pool, the Connection contract and the
//     request destination guard.
//   - credentials_test.go: the resolver's sealed credentials and rotation,
//     redirect credentials, cookies, client certificates, proxies and DNS
//     pinning, over the in-memory and SQLite stores.
//   - destination_test.go: the destination policy, mixed DNS answers,
//     rebinding and the security transport's pools.
//   - outbound_test.go: the outbound hooks on requests and redirects.
//   - p2_child_auth_test.go: P2 child repositories and credentials.
//   - registry_test.go and registry_challenge_test.go: the registry
//     exchange, its coalescing, denial, redirects and token faults, and the
//     challenge grammar.
//   - registry_feature_test.go: the godog scenarios of
//     testdata/registry_auth.feature.
//   - Core remote and Smart Remote tests build a pool and a resolver through
//     upstreamtest.
package upstream
