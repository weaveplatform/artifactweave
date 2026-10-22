# Remote Terraform modules

Remote `terraform` repositories serve module discovery, versions and signed archive
retrieval through the existing encoded namespace and repository-scoped routes (#631).
The repository API accepts `url` for source storage and `terraformRegistryUrl` for the
registry. `terraformType` is a local/federated setting and remains invalid on a remote.
For example:

```json
{
  "rclass": "remote",
  "packageType": "terraform",
  "url": "https://api.github.com",
  "terraformRegistryUrl": "https://registry.terraform.io"
}
```

The client module source is `<public-host>/<repo>__<namespace>/<name>/<system>`.
A host-root registry configuration uses `/.well-known/terraform.json` and its
`modules.v1` service; absent discovery falls back to `/v1/modules/`. A configured API
prefix, including `/artifactory/api/terraform/<upstream-repo>`, appends `v1/modules/`.
Registry metadata is authoritative even when no archive is cached. Local package
reindexing does not replace it with a list of cached versions.

## Source forms and integrity

The adapter admits HTTP(S) ZIP, tar, tar.gz/tgz, tar.bz2/tbz2, tar.xz/txz, gzip, bzip2
and XZ sources. `archive=` selects an explicit format. Relative locations resolve
against the final upstream response URL, including redirects. Inline `checksum=`
values support MD5, SHA-1, SHA-256 and SHA-512, with or without an algorithm prefix.
They are enforced independently of optional upstream checksum-header policy.

GitHub HTTPS Git locations (`git::https://github.com/org/repo.git?ref=...`) and
`github.com/org/repo` shorthand map to GitHub's archive endpoint. `ref` selects the
snapshot; positive `depth` does not change archive bytes. This is snapshot retrieval,
not a general Git clone, submodule or LFS implementation. Additional source getters,
HTTP vanity documents, checksum-file URLs and those Git semantics remain explicitly
owned by [P28 #668](https://github.com/weaveplatform/artifactweave/issues/668).

A `//subdirectory` selector can include directory glob patterns. Admission requires
exactly one real directory, including implicit parent directories. The client gets
that concrete directory, because Terraform's registry installer treats this suffix
literally. The complete original archive is served, preserving relative sibling
module references. Safe internal links are retained; absolute/escaping link targets,
special device entries, duplicate or traversal paths, and entries beneath any link
are rejected regardless of entry order. A selector cannot stand for a symlink directory.

ZIP entry CRCs and all tar/compression trailers are checked before persistent or
transient admission. Checks are bounded by 1,048,576 entries, 16 GiB of expanded data,
64 MiB of ZIP directory data and a 64 MiB XZ dictionary. Metadata is bounded by 16 MiB;
a version response permits at most 256 location lookups to establish exact readable
archive paths. Larger discovery fails explicitly rather than returning a partial list.

## Authorization and delivery

Versions disclose only readable actual archive paths, such as
`namespace/name/system/1.0.0.tar.gz`. Permission checks run again immediately before
metadata or bytes leave the server. Archive URLs remain under the public artifactweave
base; the upstream `X-Terraform-Get` value never becomes a client redirect.

A signed URL lasts 90 seconds and binds the repository, path, archive SHA-256, source
identity and original credential reference. Access-token identity, scope, expiry and
revocation are re-read from the token store. Basic authentication retains a password
revision, and anonymous access must still be enabled. User/permission changes and
source/credential/epoch changes fence publication and delivery. No password or bearer
token is copied into the URL or sent upstream.

Source archives must remain inside the configured registry/storage prefixes, the
configured GitHub storage pair, or explicit `externalDependenciesPatterns` with
`externalDependenciesEnabled=true`. Shared DNS, redirect, TLS and credential-origin
rules apply to every fetch; an allow pattern does not bypass private-address policy.
Unrelated origins do not receive configured credentials unless the operator enables
the existing explicit authentication-forwarding policy.

## Cache and failures

Raw discovery/version documents and the private validated projection of upstream 204
locations use metadata TTLs independently from immutable source archives. Signed URLs
and caller-specific lists are generated per request. `offline`, no-store, missing-entry
cache periods, hard failure, metadata zap and source-generation fences follow the
[shared retrieval contract](../implementation/remote-retrieval-contract.md).

Missing versions return 404; denied or expired signed authorization returns 403;
invalid metadata, unsafe source grammar, corrupt archives and ambiguous selectors
return 502 without replacing valid cached content. A corrected malformed upstream
response can succeed immediately without waiting for a negative-cache TTL. No-store
bytes are validated in temporary storage and removed after delivery; offline requests
cannot invent a cache entry for content that was never stored.

`terraform_remote.feature` uses pinned Terraform 1.16.4 to initialize and apply two
selected versions from cold ZIP and redirected tar sources, including a wildcard
subdirectory and relative sibling module, then repeats with a fresh client module
cache during upstream outage. This controlled fixture proves artifactweave behavior;
Artifactory parity observations remain #216 / P19. Local module/provider and state
backend behavior is unchanged. Provider remotes retain #632; virtual resolution is
P10. Remote `terraformbackend` and remote execution are not enabled. Broader registry
and execution contracts remain owned by P28; browser login remains P21/#262.
