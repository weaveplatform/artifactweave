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
`github.com/org/repo` shorthand map to GitHub's archive endpoint. Additional shorthand
path segments select a subdirectory; an explicit `//` selector is appended to them.
`ref` selects the
snapshot; positive `depth` does not change archive bytes. This is snapshot retrieval,
not a general Git clone, submodule or LFS implementation. The precise snapshot/ref
matrix and full qualification remain owned by
[P28 #668](https://github.com/weaveplatform/artifactweave/issues/668).

Qualified HTTP(S) vanity sources receive a GET with exactly one `terraform-get=1`.
A complete 200 response selects one nonempty `X-Terraform-Get` field, or, without
that field, the first nonempty HTML `meta name="terraform-get"` content before the
body/end of head. Header selection wins. HTML requires UTF-8/ASCII `text/html` or
`application/xhtml+xml`; scripts, HTML base elements and other resources are never
executed or fetched. Header source text is bounded to 8 KiB and each complete
document to 16 MiB. Chains permit ten documents; requested/effective URL identities
detect cycles, including during cached/offline resolution. Relative children use
the sanitized final response base and their own queries. These bounded server rules
are explicit local choices, not a claim that all native getter quirks match 7.x.

`checksum=file:<absolute-http(s)-URL>` retrieves one bounded checksum document under
the same destination/credential/current-source rules as the archive. It accepts a
complete 200 UTF-8/ASCII `text/plain` or `application/octet-stream` file, at most
16 MiB with 8 KiB lines. GNU text/binary/`?` records, BSD algorithm records and one
standalone digest support MD5, SHA-1, SHA-256 and SHA-512. The selected filename is
matched against the archive URL before its transport redirects; relative record
paths use POSIX URL resolution. Malformed nonblank lines, missing or duplicate matches
and conflicting files in one vanity chain fail. Local/relative checksum locations,
recursive checksum getters, userinfo, fragments and HTTPS downgrade are refused.
Checksum URL, filename and digest remain part of private archive identity; a new
checksum cannot authorize bytes named by an older signed capability.

A `//subdirectory` selector can include directory glob patterns. Admission requires
exactly one real directory, including implicit parent directories. The client gets
that concrete directory, because Terraform's registry installer treats this suffix
literally. The complete package is delivered, preserving relative sibling
module references. Registry module dependencies are rewritten as described below;
archives needing no source edits retain their exact original bytes and format,
except the portable delivery conversions below. Safe internal links are retained; absolute/escaping link targets,
special device entries, duplicate or traversal paths, and entries beneath any link
are rejected regardless of entry order. Link targets are resolved component by component
through the complete archive graph before consuming `..`; escaping chains and cycles are
rejected. Hardlinks may reference ordinary files or other hardlinks, but may not traverse
symlinks because extraction can rebase their relative targets. A selector cannot stand
for a symlink directory.

ZIP entry CRCs and all tar/compression trailers are checked before persistent or
transient admission: an XZ archive must hold every stream's index and footer, so one
cut short after its last block is refused. Checks are bounded by 1,048,576 entries,
16 GiB of expanded data and 64 MiB of ZIP directory data. An XZ block header that
names a dictionary above 1.5 GiB is refused, as XZ for Java refuses it, and a block
is decoded with no larger a dictionary than its header names. Link validation also caps
retained link names/targets at 64 MiB, each target at 8 KiB, expansion depth at 40,
and total parent/target traversal at 4,194,304 components. Metadata is bounded by 16 MiB;
a version response permits at most 256 location lookups to establish exact readable
archive paths. Larger discovery fails explicitly rather than returning a partial list.

## Authorization and delivery

Versions disclose only versions whose archive the caller may read at
`namespace/name/system/1.0.0.tar.gz`. Permission checks run again immediately before
metadata or bytes leave the server. Archive URLs remain under the public artifactweave
base; the upstream `X-Terraform-Get` value never becomes a client redirect.

An archive is cached, and checked against the include and exclude patterns and Read
permission, where Artifactory 7.x keeps it (#1260). A GitHub source is fetched as a
tag and cached at the VCS tag path,
`<owner>/<repo>/tags/<ref>/<repo>-<ref>.tar.gz`, following the trace in RTFACT-30340
and the `vcs-default` layout. An HTTP archive whose URL is below the repository's
`url` is cached at the rest of its URL path, and any other at `external/<host>/<path>`,
the rule provider files follow. A version's `/download` and the version list are
checked at `.terraform/modules/<namespace>/<name>/<system>/...`, so an include list
needs `.terraform/**` as well (RTFACT-30802). Archives cached before #1260 at
`namespace/name/system/<version>.<ext>` are not moved: the next download fetches the
archive again under its new path, and the `<key>-cache` storage listing then shows
that path.

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

Raw discovery/version documents and private validated projections of upstream 204
locations, 200 vanity responses and checksum files use metadata TTLs independently
from immutable source archives. Signed URLs
and caller-specific lists are generated per request. `offline`, no-store, missing-entry
cache periods, hard failure, metadata zap and source-generation fences follow the
[shared retrieval contract](../implementation/remote-retrieval-contract.md).

Missing versions return 404; denied or expired signed authorization returns 403;
invalid metadata, unsafe source grammar, corrupt archives and ambiguous selectors
return 502 without replacing valid cached content. A corrected malformed upstream
response can succeed immediately without waiting for a negative-cache TTL. An archive
acquisition/integrity failure mapped to 502 also rejects its exact stored checksum-file
decision, so a corrected checksum file can be fetched immediately. This rejection
rechecks current authority transactionally and uses the existing cache-generation CAS;
it cannot evict a newer replacement or act from an offline/cache-only read. No-store
bytes are validated in temporary storage and removed after delivery; offline requests
cannot invent a cache entry for content that was never stored.

`terraform_remote.feature` uses pinned Terraform 1.17.1 to initialize and apply two
selected versions from cold ZIP and redirected tar sources, including a wildcard
subdirectory and relative sibling module, then repeats with a fresh client module
cache during upstream outage. This controlled fixture proves artifactweave behavior;
Artifactory parity observations remain #216 / P19. Local module/provider and state
backend behavior is unchanged. Provider remotes retain #632; virtual modules are
implemented by #1550, while virtual providers/network mirrors are implemented by #1564. Remote `terraformbackend` and remote execution are not enabled. Broader registry
and execution contracts remain owned by P28; browser login remains P21/#262.

Every initial metadata/archive URL and redirect hop is checked against the same
configured origin/path and external pattern policy. The transport's DNS and
credential-origin rules still apply independently. For configured GitHub storage,
GitHub archive URLs may redirect to `https://codeload.github.com` only for the
same selected owner/repository and a recognized archive path. This does not
forward GitHub credentials to the CDN. GitHub documents archive downloads as
redirects in its [repository contents API](https://docs.github.com/en/rest/repos/contents#download-a-repository-archive-tar);
a public `HEAD` of the pinned Terraform tag on 2026-10-22 returned the corresponding
`codeload.github.com/hashicorp/terraform/legacy.tar.gz/refs/tags/v1.16.4` location.


## Virtual modules and registry dependency sources

A virtual serves MODULE local/federated members and remote registries under
`<public-host>/<virtual>__<namespace>/<name>/<system>`. Its version list is the
eligible version union; first eligible member owns a duplicate. Virtual/nested and
member patterns filter archives before disclosure, and unreadable members count as
missing. Local PROVIDER and untyped repositories contribute no modules. The signed
90-second download URL stays under the virtual key, names its real member internally,
and rechecks current effective membership, signer Read and policy at delivery.
Publishing modules to a virtual uses its `defaultDeploymentRepo`, including ordinary
module indexing; no target answers 405 with `Allow: GET`.

JFrog documents source override enabled by default since 7.39.4. Both direct remote
and virtual delivery rewrite static module registry sources in `.tf` and `.tf.json`
to this request's public host and repository namespace. Bare registry coordinates
and explicit registry hosts are supported, preserving `//modules/...` selectors.
Safe link entries are retained with their validated targets; Terraform 1.17.1
materializes these entries as ordinary files rather than following them. Relative paths, Git/URL getters, dynamic expressions and other blocks'
`source` attributes retain their values. Each configuration file is bounded to
16 MiB for parsing, in addition to the existing complete archive limits.

Cached source bytes and checksums remain the validated upstream originals. Delivery
uses a separate derived archive when a source changes: ZIP remains ZIP and TAR
variants become tar.gz. Plain TAR and single gzip/bzip2/XZ files always use a derived
tar.gz package because the pinned clients cannot install their original representations
as module directories. A single compressed file becomes one regular entry with its
decoded URL filename minus the compression suffix; it is not interpreted as a TAR
container. Getter format,
public rewrite target and derived digest are signed. Delivered hashes, length,
ETag, conditional responses and ranges describe the delivered bytes. Unchanged
ZIP and compressed-TAR archives retain their byte content and compression without an
output temporary copy when no rewrite is needed.
Exact inferred syntax and representation choices are recorded for #216 in
[the P10 research](../research/p10-terraform-modules.md).

The [shared virtual rules](virtual-repositories.md) cover common order, hiding,
patterns and member policy. Native Terraform publication is not implied by module
or provider resolution; the delivered generic item/DDR path remains separate.

`terraform_getters.feature` adds pinned Terraform 1.17.1 and OpenTofu 1.13.9 module
acceptance through standalone SQLite/PostgreSQL servers and both direct/virtual
registry coordinates. Each run checks native version selection, identifiable module
outputs, relative siblings where applicable, exact cached original bytes and an
upstream outage with a fresh client home/cache. Linux CI requires HTTPS discovery;
macOS uses the established loopback service override because the pinned clients do
not trust the fixture through `SSL_CERT_FILE`. Local results do not qualify Linux
HTTPS discovery, other OpenTofu provider/backend paths or live Artifactory parity.
