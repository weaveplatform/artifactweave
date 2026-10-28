# Nix binary caches

ArtifactWeave serves the Nix HTTP binary-cache protocol at
`/artifactory/api/nix/{repository}/` for local, remote, virtual and one-node
federated repositories. This implements the binary-cache scope of #788; Nix
channel publication is outside that inventory operation. Cross-peer federation
remains P13.

The contract was checked on 2026-10-28 against the
[JFrog Nix guide](https://docs.jfrog.com/artifactory/docs/nix-repositories),
[Nix 2.35 narinfo protocol](https://nix.dev/manual/nix/2.35/protocols/binary-cache/narinfo.html)
and [HTTP binary-cache store](https://nix.dev/manual/nix/2.35/store/types/http-binary-cache-store.html).
The native proof uses Nix **2.35.4**, pinned to its official image's immutable OCI
index digest in [nix.Dockerfile](../../test/acceptance/clients/nix.Dockerfile).

## Publish and substitute

Create an `Ed25519` keypair through `/artifactory/api/security/keypair` with an
alias, a PKCS8 PEM private key and its matching PKIX PEM public key. Private
material and passphrases use the existing sealed key store. Set a hosted
repository's `primaryKeyPairRef` to that keypair's name. The keypair alias must
contain only ASCII letters, digits, periods, underscores or hyphens (1–200).

```sh
nix --extra-experimental-features nix-command copy \
  --to 'https://registry.example/artifactory/api/nix/nix-local?compression=xz' \
  /nix/store/<hash>-package

nix --extra-experimental-features nix-command \
  --option require-sigs true \
  --option trusted-public-keys 'cache-1:<base64-raw-Ed25519-public-key>' \
  copy --from https://registry.example/artifactory/api/nix/nix-local \
  /nix/store/<hash>-package
```

Use Nix's protected netrc configuration for authenticated caches. The native
publication needs Write on both the archive and narinfo paths; replacing
metadata additionally requires Delete. Metadata and archive reads require Read
on both objects. Hosted responses add the configured Ed25519 signature over the
Nix store path, NAR SHA256, NAR size and sorted references. Without a hosted key,
existing publisher signatures are retained. An unavailable configured key fails
closed. Publisher `CA` assertions are omitted because the adapter does not
independently prove their content-address claims.

The cache advertises `/nix/store`. It accepts narinfo metadata and immutable
`nar/<SHA256>.nar[.xz|.bz2|.gz|.zst]` archives; compressed and expanded SHA256 and
sizes must agree. It validates the NAR grammar, zero padding, ordered directory
entries, bounded recursion and safe entry names. Limits are 1 MiB narinfo,
512 MiB compressed NAR, 2 GiB expanded NAR, 100,000 nodes, depth 128, 4,096
references and 32 incoming signatures. Decoder dictionaries are bounded to
64 MiB. Responses rewrite archive URLs to associate every download with its
narinfo and permission checks. Query parameters in HTTP requests are rejected;
Nix store URL options such as `compression=xz` are consumed by the client.

## Remote, virtual and lifecycle

A remote URL is an upstream binary-cache root. Configure its primary and/or
secondary Ed25519 key references to verify upstream signatures against trusted
aliases, including during key rotation. With no server trust root, upstream
signatures are preserved for the native client's `trusted-public-keys` check.
Archive validation still verifies both hashes, sizes and NAR structure. Metadata
and archive redirects must stay beneath the configured origin and path; caller
credentials are never forwarded. The common source policy owns TTL, refresh/zap,
transient results, offline mode, upstream credentials and cache generations.
Generic remote item downloads are refused so they cannot bypass these checks.

Virtual caches search readable concrete members in order. Missing members and
missing store paths fall through; upstream faults fail closed. Both virtual and
member permissions and patterns apply to narinfo and its archive. Configuration
and permissions are rechecked before delivery. Nested virtual members are
rejected by the shared router contract.

Native PUT and DELETE, ordinary hosted item writes, copy/move, deletion and trash
restore use the format hooks. Invalid content cannot enter through the generic
item APIs. The package index stores each store-name and store-hash version only
when narinfo and its referenced archive are complete. Deleting the archive removes
that version; restoring the complete pair restores it. Reindex validates stored
pairs and rebuilds the index. `POST /artifactory/api/nix/{repo}/reindex` requires
Manage; virtual reindex is rejected. Normal item reindex APIs also apply.

## Evidence

[Native scenarios](../../test/acceptance/features/nix.feature) use actual
`nix copy` to publish L/F content, then substitute into a fresh home and separate
store. They verify signatures, reject an incorrect trusted key, resolve an
ordered V, and substitute R again after the origin stops with offline enabled.
The committed NAR fixture was produced by the same pinned native client; its
[provenance](../../internal/format/nix/testdata/README.md) records the command.
Unit and package acceptance tests exercise malformed metadata and NARs, hashes,
signing failures, source failures, authority changes, permission denials,
virtual routing, generic admission and lifecycle contracts across store backends.

```sh
docker build -t artifactweave-accept-nix \
  -f test/acceptance/clients/nix.Dockerfile test/acceptance/clients
ARTIFACTWEAVE_NIX_IMAGE=artifactweave-accept-nix \
ARTIFACTWEAVE_REQUIRE_DOCKER=1 ARTIFACTWEAVE_ACCEPT_TAGS=@nix \
  go test ./test/acceptance -run '^TestAccFeatures$' -count=1
```

On macOS, add `ARTIFACTWEAVE_ACCEPT_LOCAL=1` and
`ARTIFACTWEAVE_NIX_DESKTOP=1` for Docker Desktop host access.
