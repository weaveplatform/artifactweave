# Upstream registry authentication

`internal/remote.Registry` implements the authentication foundation for
[#623](https://github.com/weaveplatform/artifactweave/issues/623). It performs
Distribution v2 discovery and read-only Basic/Bearer challenge exchanges using the
[shared HTTP transport](remote-http-transport.md) and resolved source security.
The local Docker token issuer remains separate: downstream Authorization, API keys
and cookies are removed before contacting either the registry or token service.
Native Docker/OCI/Helm OCI routing, digest validation and cache publication are
implemented by [#624](https://github.com/weaveplatform/artifactweave/issues/624);
see the [remote registry operations guide](../operations/registry-remote.md).

## Adapter interface and ownership

The adapter obtains `Resolver.RegistryOptions(ctx, repo.Masked(config))`, then
`RegistryPool.Client(config, permit.Identity, options)`. Options reload sealed
credentials and bind them to the resolver's opaque revision. A pool entry is scoped
to the repository configuration, credential revision, durable repository incarnation
and source generation. Replacement/removal closes idle connections and starts with
empty token state; application shutdown calls `RegistryPool.Close`. Existing active
requests retain their caller context, so the adapter must use the source publication
and delivery fences before returning any bytes.

`Probe(ctx)` requests the configured base URL plus `v2/`. `Do(ctx, image, request)`
requests `v2/<image>/<request.Path>` using GET or HEAD. Examples are `manifests/latest`
and `blobs/sha256:…`. The adapter passes the image name without a tag or digest and
must not include `dockerProjectId` a second time. `Image(name)` produces the exact
upstream name used by both URL and scope: a configured project prefixes the name,
and bare Docker Hub names acquire `library/`. Invalid, traversing, encoded or oversized
names fail before I/O. Explicit legacy `dockerApiVersion=V1` is rejected. Configured
base path prefixes remain intact.

All returned response bodies, including errors, belong to the caller. The adapter
must close them and must not treat a successful authenticated response as validated
artifact bytes. Authentication neither publishes an item nor changes cache usage.
External blob/CDN redirects use the shared destination policy; `Do` does not accept an
arbitrary absolute initial URL. Foreign-layer selection and its repository settings
remain the adapter's responsibility.

## Challenge, scope and token lifecycle

Configured username/password are Basic credentials in this registry client.
`enableTokenAuthentication=true` additionally enables Bearer challenge negotiation;
it does not send the configured password as a registry Bearer token. When disabled,
Basic or anonymous retrieval remains available, and a Bearer-only challenge fails.
Configured arbitrary Authorization headers are not used by this client.

The parser accepts Basic/Bearer schemes, case-insensitive names, separate challenge
headers, quoted commas and escaped quotes. Duplicate parameters, malformed URLs or
headers, challenges larger than 16 KiB and unsupported authentication schemes fail
with redacted errors. The token request preserves the challenged service and requests
only `repository:<effective-image>:pull`. An omitted scope is filled from the current
image; a same-image `pull,push` challenge is narrowed to pull. Other images, catalog
scopes and push-only grants are rejected. Probe tokens have no image scope and are
never reused for image retrieval.

Token exchanges use GET with `service`, `scope` and a fixed `client_id`. Existing realm
query values cannot override those fields or request offline refresh credentials.
Tokens remain opaque: either `token` or `access_token` is accepted, while conflicting
values, invalid header characters, malformed/oversized JSON and an explicitly denied
scope fail. Omitted expiry defaults to 60 seconds; `issued_at` is honored. Tokens expire
slightly early, are retained for at most 24 hours, and are never persisted or logged.
The same image/realm/service/scope exchange is coalesced; one canceled waiter cannot
cancel other callers. The last departure cancels abandoned exchange I/O. Each registry
retains at most 256 token/challenge entries and admits eight active exchanges, with
256 queued. Exchanges have a one-minute maximum including queueing, further narrowed
by configured transport deadlines.

A request can retry a registry 401 once after exchange. A rejected cached token is
removed before refresh. A second 401/403 is terminal, preventing authentication loops.
Token-service 401/403 return a sanitized denial. Other failures retain safe status
classification; 429 returns a Retry-After bounded to 1–300 seconds without sleeping or
retrying. The consuming cache policy owns durable cooldown/stale decisions. Adapters must map
`ErrRegistryDenied` to authentication/authorization failure and `ErrRegistryChallenge`
to protocol failure; neither can become `MANIFEST_UNKNOWN`. Responses and errors never relay upstream token bodies, challenges or signed query strings.

## Realm and redirect security

Contacting a token realm does not authorize sending credentials there. Every realm
and redirect must pass the shared scheme, TLS, DNS-pinning and private-address policy.
HTTP realms are permitted only at an explicitly configured HTTP registry's own origin;
HTTPS registries cannot downgrade. Untrusted private and metadata destinations fail
before a connection is made.

Basic credentials reach the same registry origin. Cross-origin HTTPS realms are
anonymous unless `allowAnyHostAuth` explicitly enables forwarding. The one narrow
built-in exception is the documented Docker Hub pair
`https://registry-1.docker.io` → `https://auth.docker.io/token`; suffix lookalikes, other
ports and other paths do not inherit that trust. Redirects never expand this grant.
Registry tokens are attached only to the exact registry origin and the requested
image's v2 path, even when `allowAnyHostAuth` is enabled. A cross-image redirect loses
the token, and a CDN challenge cannot trigger another exchange. Registry/token flows
do not share a cookie jar.

## Evidence and remaining qualification

`TestRegistryAuthenticationFeature` executes
[`registry_auth.feature`](../../internal/remote/testdata/registry_auth.feature) against
controlled registry and token endpoints. It proves anonymous/authenticated exchange,
exact service/scope, expiry, reuse and image isolation using the real shared resolver
and HTTP client. Race tests cover concurrent coalescing/cancellation, source and
credential replacement, denial, malformed responses, malicious realms, redirected
credentials and bounded loops. These are implementation proofs; native crane/oras/Helm
pull evidence belongs to #624, and observed Artifactory comparisons remain #216/P19.

The [Distribution token specification](https://distribution.github.io/distribution/spec/auth/token/)
documents the challenge/exchange/retry sequence, Docker Hub realm example, GET token
parameters, token/access_token aliases and expiry fields. Its
[scope specification](https://distribution.github.io/distribution/spec/auth/scope/)
defines audience/resource/action scoping. JFrog's
[remote Docker Terraform resource](https://github.com/jfrog/terraform-provider-artifactory/blob/master/docs/resources/remote_docker_repository.md)
documents token authentication and GCR/GAR project selection. Our strict realm grants,
project-prefix mapping, pull-only narrowing, retry/capacity/expiry bounds and cookie
separation are deliberate implementation choices [I], requiring #216/P19 comparative
captures. They do not change the status of ADR 0007 or complete P9's registry adapter.
