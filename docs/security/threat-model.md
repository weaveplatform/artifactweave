# Threat model

Status: skeleton (P0). Completed in P18 before the first release candidate.

## Assets

- Stored artifacts and their integrity (checksums, signatures, provenance files).
- Credentials: user passwords, access and reference tokens, API keys, remote repository
  and proxy passwords, webhook secrets, keypair private keys, OIDC configuration.
- Repository and permission configuration, which decides who can read and publish.
- Availability of the registry for builds that depend on it.

## Trust boundaries

| Boundary | Notes |
|---|---|
| Package clients → registry | Basic, Bearer and `X-JFrog-Art-Api` credentials over TLS terminated at the reverse proxy; anonymous access when enabled |
| Registry → upstream registries | Remote repository credentials, proxies, client TLS certificates; upstream content is untrusted until checksummed |
| Registry → webhook receivers | Outbound HTTP with HMAC signing; receivers are untrusted |
| Registry → database and blob store | Trusted infrastructure; sealed columns protect secrets at rest |
| Signed URLs → object stores and CDNs | A redirected download hands the client a time-limited URL to the object store or a CDN, signed after the permission check. Anyone holding the URL can fetch the blob until it expires; the registry cannot revoke it. Front Door validates nothing and serves its cached copy for the exact URL (decision 0029) |
| CI providers → token exchange | `POST /access/api/v1/oidc/token` needs no credentials: a provider's signed ID token is exchanged for an access token when an identity mapping's claims match. Only administrators register providers, so the keys fetched from `issuer_url` are trusted as far as the administrator trusts that issuer; a mapping with loose claims (or none) hands a token to every workload that provider signs for (decision 0005, research 21) |
| Operators → configuration | Strict YAML, descriptor PATCH, Terraform with admin tokens |

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
- Workers sandbox: scripts run in helper processes with a 64 MiB per-execution memory cap,
  a 1 s CPU budget, wall-clock limits, no filesystem and an outbound allow-list (decision
  0026). Analyse escape from goja into the helper, abuse of platform calls made on the
  worker's behalf, and denial of service by saturating the helper pool.

## Mitigations already decided

See decisions [0005](../research/decisions/0005-authentication-and-tokens.md),
[0006](../research/decisions/0006-authorization-native-permission-model.md),
[0017](../research/decisions/0017-sealed-columns-and-master-key.md),
[0029](../research/decisions/0029-cdn-delivery-and-download-redirect.md) and the body and
upload limits in [0004](../research/decisions/0004-storage-sql-metadata-and-blobs.md).
