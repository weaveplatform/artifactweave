# Terraform remote providers

The P9 provider adapter uses authoritative upstream metadata and preserves the
publisher's original ZIP, SHA256SUMS, detached signature and GPG public-key
identity. Remote repositories do not need a local signing key. No local private
key signs or replaces an upstream package.

## Configuration and endpoints

Configure a remote `terraform` repository with `terraformRegistryUrl` for the
registry API and `terraformProvidersUrl` for provider storage. `url` remains the
module storage setting. `terraformType` applies to local repositories and is not
set on a remote repository.

A host-root registry URL uses `providers.v1` discovery. Only an absent discovery
document (404) falls back to `/v1/providers/`. An API prefix such as
`https://peer.example/artifactory/api/terraform/upstream` uses the repository-scoped
`v1/providers/` endpoints. Relative package URLs resolve against the actual
metadata response URL after redirects.

The downstream registry routes are:

- `/artifactory/api/terraform/<repo>/v1/providers/<namespace>/<type>/versions`
- `/artifactory/api/terraform/<repo>/v1/providers/<namespace>/<type>/<version>/download/<os>/<arch>`
- The host discovery form uses `<repo>-rt-ns-<namespace>` as for local providers.

A downstream network mirror uses
`https://<server>/artifactory/api/terraform/<repo>/providers/` in Terraform's
`provider_installation.network_mirror` configuration. Its `index.json` and
`<version>.json` identify a provider by registry hostname, namespace and type.
The requested hostname never becomes a network destination.

For a configured host-root origin registry, mirror requests name that registry's
host (the configured service port may be omitted from the provider identity).
Registry metadata supplies the versions/platforms and the server verifies the
upstream signature before producing mirror package URLs and `h1`/`zh` hashes.
For a configured Smart Remote API prefix, the adapter consumes that peer's
`providers/<hostname>/<namespace>/<type>/index.json` and version JSON directly.
This preserves the mirror's per-host identity and original hashes. A raw mirror
has no GPG-signature fields; the adapter does not invent a signer for it.

## Integrity and cache visibility

Discovery, versions and platform metadata are validated as complete bounded JSON
before cache admission. Malformed documents, platform mismatches, duplicate
versions/platforms, unsafe URLs and public keys whose advertised key ID disagrees
with their actual key are rejected. Raw upstream metadata is retained separately
from request-specific rewritten responses and the local package index.

Before a registry package response is emitted, all three components must be
available and agree: the package SHA-256 and its SHA256SUMS entry, a detached
signature verified by an advertised public key, and a completely validated ZIP.
Individual validated cache representations may exist while a companion is
missing, but they are not advertised as a complete installable package. Invalid
bytes are never published; corrected upstream bytes can be retried immediately.
A metadata/key or checksum-document revision changes the companion validation
identity, preventing a mixed old/new bundle from being treated as valid.

Mirror archives are fully validated and checked using the strongest supported
advertised hash (`h1` before `zh`); a weaker match cannot rescue a stronger
mismatch. Multiple valid alternatives for that algorithm are allowed. Unknown-only
hash sets fail. When an upstream mirror omits hashes, the server computes an `h1`
for downstream corruption detection; this does not establish an upstream signer.

Native Terraform verifies GPG for the registry protocol. The mirror protocol
verifies package hashes and may describe an installation as unauthenticated,
because its wire contract has no signer identity. When mirror data is projected
from a configured origin registry, the server still verifies that registry's
original GPG signature before exposing the archive.

Versions are upstream-authoritative, filtered using actual platform metadata and
current permissions. If upstream versions omit optional platform hints, the
adapter probes the pinned client's supported platforms within a bounded lookup
budget. It does not use an invented path as proof that a package is readable.
Package download metadata is emitted only after complete bundle validation.

## Authorization, origins and refresh

Archive and companion URLs are short-lived signed links bound to the original
credential reference, source identity, metadata revision and exact bytes. Their
anonymous HTTP transport does not become anonymous authority: delivery restores
and rechecks the original token scope/expiry/revocation, password revision or
anonymous-access policy, plus current Read permission. Metadata and each disclosed
file are checked again immediately before delivery.

Provider registry cache paths use
`<namespace>/<type>/<version>/terraform-provider-<type>_<version>_<os>_<arch>.zip`
and the adjacent canonical SHA256SUMS/signature paths. Smart Remote mirror
artifacts use `provider-mirrors/<hostname>/` before that path, keeping different
origin identities separate. Permissions must cover the selected archive and,
for registry bundles, both companions.

Configured registry/storage origin and path prefixes constrain initial targets
and every redirect. Other advertised hosts require external-dependency allow
patterns. DNS/private-address rules and credential-origin policy apply
independently; an allowed artifact host does not receive registry credentials
merely because it was named in metadata. Query credentials and raw upstream URLs
are not exposed in rewritten package links.

Repository freshness and negative-cache settings govern metadata. Checksum and
signature artifacts are refreshable, with validation bound to the current package
metadata. Offline mode serves only a complete validated cached bundle; it cannot
manufacture a missing archive or companion. Repository no-store and upstream
`Cache-Control: no-store` retain the same verification and delivery checks using
transient bytes. A no-store component that cannot be fetched during an outage
makes its bundle unavailable.

## Evidence and remaining ownership

The native content qualification uses the pinned Terraform executable and null
provider archive against a controlled publisher. It is separate from JFrog
Terraform-provider repository CRUD acceptance. Controlled upstream tests do not
establish an observed Artifactory response: P19/#216 owns vendor comparison and
ADR0040 retains its existing status. P10 owns virtual resolution; P21/#262 owns
browser login approval. Additional Terraform source-getter qualification remains
P28/#668 and does not change provider signature requirements.

Protocol references: [provider registry](https://developer.hashicorp.com/terraform/internals/provider-registry-protocol),
[network mirror](https://developer.hashicorp.com/terraform/internals/provider-network-mirror-protocol),
and [JFrog repository settings](https://docs.jfrog.com/artifactory/docs/terraform-opentofu-and-terraform-backend-repositories#remote-terraform-repository).
