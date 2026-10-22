# NuGet v3 remote repositories

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
this adapter. V3 writes answer 405; remote v2/symbol retrieval remains the separate
P9 [#629](https://github.com/weaveplatform/artifactweave/issues/629) implementation.
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
URLs must be absolute HTTP(S), without credentials, fragments or traversal. HTTPS
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
