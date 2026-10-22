# Threat model

Status: P0–P8 boundaries and implemented P9 transport, cache and native-adapter
controls; the [#639 sign-off](../testing/phase9-signoff.md) records the combined-tree
qualification required before P9 closes.
P18 owns the first release-wide security review. The expanded identity, governance,
integration and HA scope needs reviews in its implementation phases and the P41
qualification gate; see the [capability roadmap](../implementation/capability-roadmap.md).

## Assets

- Stored artifacts and their integrity (checksums, signatures, provenance files).
- Credentials: user passwords, access and reference tokens, remote repository
  and proxy passwords, webhook secrets, keypair private keys, OIDC configuration.
- Repository and permission configuration, which decides who can read and publish.
- Terraform state, which can contain infrastructure credentials and other secrets.
- Availability of the registry for builds that depend on it.

## Trust boundaries

| Boundary | Notes |
|---|---|
| Package clients → registry | Basic, Bearer and `X-JFrog-Art-Api` credentials, plus `X-NuGet-ApiKey` on NuGet routes, over TLS terminated at the reverse proxy; anonymous access when enabled and granted by repository permissions |
| Registry → upstream registries (P9) | Shared transport resolves sealed credentials, verifies TLS, isolates cookies and constrains destinations. Native adapters validate untrusted metadata and artifact bytes before atomic cache admission; final combined-tree evidence is tracked in the [P9 sign-off ledger](../testing/phase9-signoff.md) |
| Registry → webhook receivers (planned P11) | Outbound HTTP with HMAC signing; receivers are untrusted. Delivery is not implemented |
| Registry → database and blob store | Trusted infrastructure; sealed columns protect secrets at rest. Terraform backend state blobs are sealed when the repository's `encryptStates` is on; the master key is stored separately |
| Signed URLs → object stores and CDNs | A redirected download hands the client a time-limited URL to the object store or a CDN, signed after the permission check. Anyone holding the URL can fetch the blob until it expires; the registry cannot revoke it. Front Door validates nothing and serves its cached copy for the exact URL (decision 0029) |
| CI providers → token exchange | `POST /access/api/v1/oidc/token` needs no credentials: a provider's signed ID token is exchanged for an access token when an identity mapping's claims match. Only administrators register providers, so the keys fetched from `issuer_url` are trusted as far as the administrator trusts that issuer; a mapping with loose claims (or none) hands a token to every workload that provider signs for (decision 0005, research 21) |
| Operators → configuration | Strict YAML, descriptor PATCH, Terraform with admin tokens |
| Human identity providers and provisioning clients → registry (planned P12/P21/P22) | LDAP, SAML, human OIDC/OAuth, Crowd and trusted-proxy HTTP-SSO must bind the intended identity; SCIM changes accounts and group grants without a full web console. Workload token exchange does not cover these flows |
| Registry → evidence, policy and security services (planned P23–P27) | Signing keys, provenance, approval decisions, vulnerability/license intelligence and quarantine results affect release or download authorization; failures must not silently grant access |
| Registry nodes → shared state (planned P35) | Failover, leases, upload staging, configuration and authentication throttling must remain consistent across nodes; mirrored blobs alone do not provide this guarantee |
| Operators → migration, external secrets and hosted services (planned P36/P38/P40) | Imports, key rotation, KMS/Vault/IAM, diagnostics and account integrations cross new trust boundaries; unavailable provider credentials do not justify simulating success |

Terraform's local signed URLs are a separate boundary from object-store/CDN redirects.
Module/provider download URLs expire after 90 seconds, bind the path and checksum, and
recheck the named user's current read access. Modern backend state-upload URLs bind the
workspace incarnation, state serial and MD5, expire within ten minutes or the issuing
token's earlier expiry, and preserve the issuing token's scope. Uploads recheck the
identity's current Deploy permission and the workspace lock; completed uploads cannot be
replayed. Treat both kinds of URL as credentials in logs and proxy configuration
(decisions 0040 and 0041).

Package archives remain untrusted input. In addition to the shared bounded archive
reader, NuGet symbol extraction limits actual decompressed PDB bytes to 64 MiB per file
and 256 MiB per package. Symbol downloads require Read permission on the canonical
`.symbols/<file>/<key>/<file>` path in their repository. The original `.snupkg` is
authoritative and reindex rebuilds the derived symbols (decision 0038).

NuGet remote reindex reads only retained cached packages. The queued worker restores
the caller's current credential reference and Manage grant, validates stored hashes,
ZIP entries and nuspecs, and checks source identity and package snapshots again before
publishing repaired index rows. The queue payload stores neither a password nor a
bearer token. Revoked authority or an obsolete source parks the job; concurrent cache
changes force a retry. See the [NuGet operations guide](../operations/nuget-remote.md#reindexing-cached-packages).

## Implemented P9 source controls and residual limits

The [source security contract](../implementation/remote-http-transport.md#resolved-source-security-618)
removes downstream auth/cookies before every request, scopes configured auth to its
origin unless HTTPS cross-origin auth is explicitly enabled, rejects TLS downgrades,
and applies the same DNS/IP policy to origin, redirect, metadata URL and token-realm
requests. Explicit private origins are supported; metadata cannot grant itself an
internal destination. Known metadata services and link-local addresses remain denied.
Validated addresses are pinned across direct/proxy connections. Proxy credentials and
TLS identity are separate from repository credentials and client certificates.

Credentials and sensitive custom headers come from sealed store reads, never masked
API output. Pool/cookie replacement follows source-setting and referenced-secret HMAC
revisions. Errors expose classifications rather than raw URLs, signed queries, headers,
proxy passwords or certificate material. The cache publication layer must recheck the
revision and current policy/authorization after I/O; replacing a client alone does not
revoke an already running request or prevent stale publication.

Operators intentionally choosing a private source/proxy grant access to that authority;
application checks do not replace network egress controls. A malicious configured proxy
can disregard CONNECT targets, and unlisted provider-specific metadata services still
require network policy. Certificate-chain/hostname verification is implemented; online
CRL/OCSP revocation checking is not. P18 must review revocation requirements and these
residual boundaries. DNS-named plain HTTP sources require proxy CONNECT support; P12
owns NTLM and proxy-credential redirect settings, which currently fail closed. Cookie
domain/path/secure semantics permit eligible cookies across ports within one source;
jars are isolated between repositories and source revisions. Response-body integrity,
authenticated cache partitioning and format metadata rewriting have separate adapter
and cache regressions. The transport tests alone do not establish native-adapter
completion; the [P9 sign-off ledger](../testing/phase9-signoff.md) tracks their combined
client, security and coverage evidence.

## Threats to analyse (P18)

- Credential theft: token leakage in logs, in error bodies, in `GET /api/repositories`
  responses (masking rules in decision 0017), in Terraform state.
- Supply-chain substitution: wrong-checksum uploads, checksum-deploy of a blob the caller
  cannot read, cache poisoning through a compromised upstream, path traversal in layouts
  and archive explode, zip bombs.
- Authorization bypass: include/exclude pattern edge cases, `ANY` targets, token scope
  widening, project key prefix confusion, anonymous reads of `-cache` entries.
- Denial of service: unbounded uploads, AQL cost, metadata merge storms on virtual
  repositories, webhook retry amplification.
- Multi-tenancy: project isolation, shared repositories, storage quotas.
- Signed URL and CDN leakage: URLs in client logs, proxies and CI output; the Front Door
  exposure window (URL window plus TTL plus edge cache, and stale copies served while the
  origin errors); a CDN cache policy that keys on the wrong parameters; a Cloud CDN bucket
  left public; credentials forwarded to a CDN host that is a subdomain of the registry, or
  in headers Go does not strip (`X-JFrog-Art-Api`) (decision 0029).
- Planned workers sandbox (P17): scripts will run in helper processes with a 64 MiB per-execution memory cap,
  a 1 s CPU budget, wall-clock limits, no filesystem and an outbound allow-list (decision
  0026). Analyse escape from goja into the helper, abuse of platform calls made on the
  worker's behalf, and denial of service by saturating the helper pool.

## Mitigations already decided

See decisions [0005](../research/decisions/0005-authentication-and-tokens.md),
[0006](../research/decisions/0006-authorization-native-permission-model.md),
[0017](../research/decisions/0017-sealed-columns-and-master-key.md),
[0029](../research/decisions/0029-cdn-delivery-and-download-redirect.md) and the body and
upload limits in [0004](../research/decisions/0004-storage-sql-metadata-and-blobs.md).
