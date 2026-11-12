# Nix binary caches and channels

ArtifactWeave serves the Nix HTTP binary-cache protocol and Nix channels at
`/artifactory/api/nix/{repository}/` for local, remote, virtual and one-node
federated repositories. The binary cache is #788's scope and channels are
#1263's. Cross-peer federation remains P13.

The contract was checked on 2026-10-28 against the
[JFrog Nix guide](https://docs.jfrog.com/artifactory/docs/nix-repositories),
[Nix 2.35 narinfo protocol](https://nix.dev/manual/nix/2.35/protocols/binary-cache/narinfo.html)
and [HTTP binary-cache store](https://nix.dev/manual/nix/2.35/store/types/http-binary-cache-store.html).
The native proof uses the [current pinned Nix client](ci-client-images.md),
with its official image's immutable OCI index digest in
[nix.Dockerfile](../../test/acceptance/clients/nix.Dockerfile).

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
publication needs Write on the paths below, where the NAR and the narinfo are
stored; replacing metadata additionally requires Delete. Metadata and archive
reads require Read on both objects. Hosted responses add the configured Ed25519 signature over the
Nix store path, NAR SHA256, NAR size and sorted references. Without a hosted key,
existing publisher signatures are retained. An unavailable configured key fails
closed. Publisher `CA` assertions are omitted because the adapter does not
independently prove their content-address claims.

A hosted repository stores each store path in its own folder,
`binary-cache/<store hash>/`, as Artifactory 7.x does and `jf nix` queries it
([jfrog-cli-artifactory](https://github.com/jfrog/jfrog-cli-artifactory),
`artifactory/commands/nix/command.go`): the narinfo as
`binary-cache/<store hash>/<store hash>.narinfo` and the NAR under the file
name the client gave it, `binary-cache/<store hash>/<file>`. The client keeps
the URLs of a plain binary cache. `nix copy` uploads the NAR first, to
`nar/<file>`, which names no store path, so the repository holds it there
until the narinfo arrives at `<store hash>.narinfo`; it then stores the NAR in
the store path's folder, from that upload or from the same file stored for
another store path, stores the narinfo beside it and removes the held upload,
past the trash can. The publisher needs Write on both paths in the folder. `DELETE <store hash>.narinfo` and
`DELETE nar/<store hash>/<file>`, the URL the narinfo serves, remove the stored
files; `DELETE nar/<file>` removes only an upload still held. The item API
stores what it is sent where it is sent: a narinfo at the root of a hosted
repository is refused, and one deployed to its folder needs the NAR there. A
remote repository's cache keeps the paths the client asks for,
`<store hash>.narinfo` and `nar/<file>`.

The cache advertises `/nix/store`. It accepts narinfo metadata and immutable
`<SHA256>.nar[.xz|.bz2|.gz|.zst]` archives; compressed and expanded SHA256 and
sizes must agree. An xz archive must be whole: every stream must end in an
index that lists its blocks and a footer that matches it, as `xz -t` and Nix's
decompressor require, so an upload cut short after its last block is refused
with 400 "invalid Nix archive", and a channel tarball with 400 "invalid Nix
channel archive". A published store path keeps its signed fingerprint (path,
NAR hash/size and references); signatures, deriver and transport encoding can
refresh without changing that identity. Concurrent publications serialize on the
store hash. It validates the NAR grammar, zero padding, ordered directory
entries, bounded recursion and safe entry names. Limits are 1 MiB narinfo,
512 MiB compressed NAR, 2 GiB expanded NAR, 100,000 nodes, depth 128, 4,096
references and 32 incoming signatures. The zstd decoder holds at most 64 MiB
of history. An xz block header that names a dictionary above 1.5 GiB is refused
with the same 400, as XZ for Java, which Artifactory decodes xz with, refuses
it. Responses rewrite archive URLs to associate every download with its
narinfo and permission checks. Query parameters in HTTP requests are rejected;
Nix store URL options such as `compression=xz` are consumed by the client.

## Channels

A channel is followed with `nix-channel`, as the JFrog guide shows, and a
snapshot of it is uploaded with `curl`:

```sh
curl -X PUT -u <user>:<token> -T nixexprs.tar.xz \
  https://registry.example/artifactory/api/nix/nix-local/channels/nixpkgs-25.11/nixpkgs/nixpkgs-25.11pre893393
nix-channel --add \
  https://registry.example/artifactory/api/nix/nix-local/channels/nixpkgs-25.11 nixpkgs
nix-channel --update
```

The JFrog guide puts the credentials in the channel URL. `nix-channel`
downloads that URL, then downloads `nixexprs.tar.xz` below the final URL
libcurl reports, and libcurl 8.23.0 and later leave the credentials out of
that URL. The pinned Nix 2.35.5 image has libcurl 8.23.0, so the tarball
download carries no credentials, whatever the server answers. For a
repository that needs a login, give Nix the credentials in its netrc file
(`netrc-file`, `/etc/nix/netrc` by default), as for the binary cache.

The flavor is `nixpkgs` or `nixos`. The upload must be an xz-compressed tar
archive with at least one entry; anything else is refused with 400 "invalid
Nix channel archive". It is stored as
`channels/<channel>/<flavor>/<snapshot>/nixexprs.tar.xz`, and a PUT to that
path stores it there too. Publication needs a local or federated repository.

`GET channels/<channel>` answers 302 to the newest snapshot the caller may
read, with a relative `Location` (`<channel>/<flavor>/<snapshot>`), so the
client keeps the URL's host and path. The newest snapshot is the one
uploaded last, so uploading an older snapshot again rolls the channel back to
it. The snapshot's folder answers a small HTML page linking the tarball, as
`releases.nixos.org` does, and `nix-channel` then downloads
`nixexprs.tar.xz` below it. Nix caches the channel for `tarball-ttl` seconds
(an hour by default), so an `--update` within that time keeps the snapshot it
has.

Read permission comes first: a caller who may not read the channel or the
tarball gets 403, or 401 when anonymous. The channel and a snapshot's page are
computed, so a blacked-out repository refuses them with 404 at the path asked
for and include and exclude patterns do not; patterns refuse the tarball at its
own path, while the channel still names that snapshot.

A remote repository resolves the channel name at the NixOS Channel Registry,
`https://channels.nixos.org`, whatever its own URL (the binary cache), as the
JFrog guide requires an official channel name. It redirects to the snapshot
the registry names and caches that release's tarball from
`https://releases.nixos.org`. Its cache period, offline mode and proxy apply;
its credentials are never sent there. Only the snapshot the registry names now
is served. A virtual repository redirects to the first member's newest
snapshot in order, and serves a snapshot's page and tarball from the first
member that holds it, passing over a member that is blacked out or refuses
it.

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
member permissions and patterns apply to narinfo and its archive. Permissions
and the download policy are checked where each repository stores the file: a
hosted repository's `binary-cache/<store hash>/` folder, so a pattern written
for 7.x such as `binary-cache/**/*.nar.xz` refuses the NAR and leaves the
narinfo served, and a remote's `<store hash>.narinfo` and `nar/<file>`. A
virtual repository checks its own at a hosted repository's paths. Configuration
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
A channel scenario uploads a snapshot with `curl`, follows the channel with
`nix-channel --add` and `--update`, then uploads a newer snapshot and updates
to it.
[A layout scenario](../../test/acceptance/features/nix_binary_cache.feature)
publishes with `nix copy` and finds both files in the store path's
`binary-cache` folder through the storage API and `jf nix`'s AQL query.
The committed NAR fixture was produced by the same pinned native client; its
[provenance](../../internal/artifacts/ecosystem/nix/testdata/README.md) records the command.
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
