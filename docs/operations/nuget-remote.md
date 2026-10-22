# NuGet remote repositories

Configure a `nuget` remote with its upstream `url` and `v3FeedUrl`, then point .NET at
`/artifactory/api/nuget/v3/<repository>/index.json`. Upstream credentials belong to the
repository configuration. A client uses its own artifactweave credentials and needs
Read permission; cache population does not require Deploy. `forceNugetAuthentication`
challenges anonymous clients before discovery, so NuGet can supply its credentials.

The service index discovers the upstream flat container, registration hives, search,
autocomplete and repository-signature resources. Responses advertise only supported
resources actually present upstream. Package publication and vulnerability resources
are not invented. Proxying an upstream `VulnerabilityInfo` resource remains P28 protocol
integration with P27 security data; an upstream advisory resource is not advertised by
this adapter. Remote v2/v3 writes answer 405. V2 feeds and upstream symbol reads are
implemented by P9 [#629](https://github.com/weaveplatform/artifactweave/issues/629).
Local v2/v3 publication and restore are unchanged; virtual resolution belongs to P10.

Package identities are case-insensitive and versions use NuGet normalization. A cached
archive's canonical permission path is `<lower-id>/<normalized-version>/<lower-id>.<normalized-version>.nupkg`.
Versions, registration pages/leaves and search/autocomplete responses disclose only
readable archives. Every disclosed path is checked again immediately before delivery.
Dependency ranges, listed/unlisted state, SemVer2 rules and compatible catalog metadata
are retained. Search drops an entry whose selected version is forbidden and reports
only its visible result count; it does not disclose the upstream total or aggregate
download count. Autocomplete package IDs require at least one readable version.

Only the registration index path is constructed. Page links are discovered from the
index, and package targets come from the selected registration entry. Flat-container-only
feeds also work. Public URLs are built per request from the configured/forwarded base
URL; the raw cache never contains a user's filtered metadata or public URL. Resource
URLs used for network requests must be absolute HTTP(S), without credentials,
fragments or traversal. An inline registration page may identify itself with
`index.json#page/...` when all its entries are embedded. Its base URL still passes
the same security checks; that identity is never fetched, and response page URLs
are rewritten locally. Linked pages and package targets retain strict fragment
rejection. HTTPS
resources cannot downgrade to HTTP. DNS/address and redirect checks run at the shared
transport boundary, and credentials remain bound to their configured origin.

Admission validates complete JSON and each catalog entry before publishing raw metadata,
including entries the first reader cannot access. Gzipped registration responses are
validated with bounded decompression; the original upstream bytes remain cached. Each
request permits at most 128 metadata reads, 100 registration pages, 10,000 versions,
16 MiB per raw/decompressed document and 64 MiB of decoded metadata in total. Search
and autocomplete request at most 100 results. Exceeding a bound or receiving malformed
metadata fails with 502; a missing upstream resource/document returns 404.

Archives retain their exact upstream bytes, including `.signature.p7s`. Before cache or
no-store publication, the adapter bounds the ZIP central directory before allocation (32 MiB and 100,000 entries),
checks URI-decoded entry paths and regular-file/directory types, every entry's CRC, nuspec identity and
any declared registration SHA-512 pin. Pins apply even with `ignore-and-generate`
upstream checksum-header policy. Repository-signature documents retain their
`allRepositorySigned` value and certificate fingerprints; certificate URLs are proxied,
validated as DER and pinned to the declared SHA-256. Signature trust, expiry, revocation
and signing-policy decisions remain with NuGet's native cryptographic verifier. The
server neither signs packages nor converts a valid ZIP into a claim of signature trust.
Use an HTTPS public base URL when exposing repository-signature resources, as NuGet
requires HTTPS for those URLs.

The shared [retrieval contract](../implementation/remote-retrieval-contract.md) governs
metadata expiry, negative caching, transient no-store results, outage fallback and
current source/credential/permission fences. Existing Chocolatey URL validation retains
its minimum retrieval interval. Warm offline restore requires the needed service index,
version/registration documents and archives to have been fetched; no local package-index
subset replaces upstream version metadata.

`nuget_remote.feature` creates a private upstream with real author-signed packages and
a dependency, requires a trusted signer during .NET restore, compares the signed bytes,
and runs `dotnet nuget verify`. Changing signed content while retaining the real
signature and valid ZIP structure fails native verification with NU3008. It repeats restore/verification with an empty client
package cache and no HTTP cache after the upstream stops. NuGet's lock hash excludes
the signature, so that assertion uses the original unsigned content separately from
signed-archive byte comparisons. This controlled fixture proves artifactweave behavior;
Artifactory comparison remains P19/#216 work.

## V2 feeds and custom context paths

Point a client at `/artifactory/api/nuget/<repository>` with `protocolVersion="2"`.
`feedContextPath` is appended to the configured upstream `url` for service/schema and
feed queries; `downloadContextPath` supplies the package download prefix. Their defaults
are `api/v2` and `api/v2/package`. An explicit empty string uses the URL root, including
smart remotes whose URL already names `/api/nuget/<upstream-repository>`; omission or
`null` restores the default. Configured download context takes precedence over the Atom
content URL after that URL passes safety validation. This precedence remains an [I]
comparison under N9-5/#216. Upstream Basic credentials and force-auth remain shared policy.

The retained local query grammar also applies remotely: `Packages`, `Search()`,
`FindPackagesById()`, exact package keys, `$count`, bounded filters/order and pagination.
`GetUpdates()` is superseded; unlimited OData is not claimed. Upstream continuations and
relative `xml:base`/links resolve against the actual response URL after redirects. The
adapter walks at most 100 pages / 10,000 versions, then filters current artifact permissions
before counts, latest flags and pagination (100 entries per page). Downloads and continuation
URLs point back at the proxy. No upstream aggregate download count is disclosed.

XML has a 16 MiB document, 100,000-element and 32-level depth bound; DTDs, external entities,
malformed/duplicate metadata, unsafe links and deceptive content are refused before cache
publication. The [#669 source contract](https://github.com/weaveplatform/artifactweave/issues/669)
projects a bodyless upstream 204 into a validated empty Atom feed only on feed/query
endpoints. Service/schema 204 remains invalid. Pins in v2 `PackageHash`/`PackageHashAlgorithm`
(SHA-512, SHA-256 or SHA-1) are enforced independently of optional checksum-header policy,
with the same complete archive validation used by v3. Raw XML stays separate from rewritten,
permission-filtered public responses. Upstream no-store and disabled local storage remain
transient; metadata expiry and outage policy use the shared retrieval contract.

Chocolatey's configured URL retains the existing 14,400-second minimum metadata interval.
This implements the bounded feed contract; a native Chocolatey client/version matrix remains
P28/P41, alongside NuGet CLI, PowerShell and Visual Studio/MSBuild expansion.

## Upstream symbols and retention

Set `symbolServerUrl` to the upstream SSQP root. Requests at
`/artifactory/api/nuget/v3/<repository>/<file>.pdb/<key>/<file>.pdb` proxy the configured
upstream symbol identity, independently of v3 service discovery. The canonical cache and
permission path is `.symbols/<lower-file>/<upper-key>/<lower-file>`. Each complete PDB is
bounded to 64 MiB and must identify the requested MSF7 or Portable PDB GUID/age key before
admission; GET, HEAD and byte ranges preserve exact bytes. Missing symbols return 404;
wrong-key, malformed, oversized or unsafe upstream responses fail without publication.

Upstream PDB identities do not establish a trustworthy package owner. They require an
explicit symbol-path Read grant; a package-only grant does not imply symbol access. Current
permission/source/credential fences apply again at delivery. Local `.snupkg` extraction and
its source ownership/deletion rules remain separate; remote PDBs are cached upstream bytes,
not derived local symbols. Virtual symbol resolution belongs to P10.

`maxUniqueSnapshots` also applies to cached remote NuGet prereleases. Cache admission marks
deduplicated durable cleanup work, processed by the existing NuGet periodic worker. Zero
means unlimited; releases are retained. Cleanup removes old cached versions through the
item/cache deletion and generation fences, without rewriting upstream version lists. A
removed upstream version can be fetched again when requested.

`@nuget_v2_remote` proves .NET 10/11 search and cold restore through private custom contexts,
paging and download redirects, then a fresh-cache restore after upstream shutdown.
`@nuget_remote_symbols` uses PDBs generated by those SDKs and proves authenticated exact GET,
HEAD, range and offline byte reads. This is SSQP HTTP evidence, not a Visual Studio debugger
qualification. The two workflows are recorded separately from v3 in the P9 exit evidence.
