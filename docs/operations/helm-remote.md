# Classic Helm remote repositories

Remote `helm` repositories serve an upstream `index.yaml`, chart archives and
provenance through `/artifactory/api/helm/<repo>/`. Helm OCI uses the separate
Docker/OCI adapter (#624). Virtual index merging remains P10.

```sh
curl --fail --user admin "$REGISTRY/artifactory/api/repositories/charts" \
  --request PUT --header 'Content-Type: application/json' \
  --data '{"rclass":"remote","packageType":"helm","url":"https://charts.example/releases/"}'
helm repo add charts "$REGISTRY/artifactory/api/helm/charts" --username reader
helm repo update charts
helm pull charts/example --version 1.0.0 --verify --keyring trusted-public.gpg
```

The raw upstream index has its own metadata cache. Downloading or reindexing one
chart cannot replace that index with a listing of the downloaded subset. Metadata
TTL, upstream validators, zap, stale/error policy, offline and no-store follow the
[shared retrieval contract](../implementation/remote-retrieval-contract.md). A
changed index becomes visible without downloading every archive. An offline read
needs both the cached index and the selected chart/provenance; uncached versions
remain unavailable.

Every delivered URL points through this repository. Relative URLs resolve against
the actual index response URL after redirects, including cached responses. An
explicit `chartsBaseUrl` supplies the relative chart base instead. Absolute URLs
inside the configured repository, redirected index directory or explicit chart
base are permitted. Other destinations require `externalDependenciesEnabled: true`
and a matching `externalDependenciesPatterns` URL pattern on the initial request
and every redirect hop. Their local route is
`_external/<scheme>/<host>/<path>`. Only targets advertised by a validated index
can be fetched through these routes; a caller cannot supply another target or query.

Userinfo, fragments, traversal and HTTPS downgrades are refused. All fetches still
use the common DNS/destination, proxy and credential-origin checks. A pattern does
not authorize otherwise forbidden private addresses. Upstream credentials are
configured on the repository separately from the client's artifactweave credentials.
With `helm.relative_urls: false`, rewritten URLs use the request's public base and
the `/api/helm/<repo>/` route. Otherwise they are repository-relative.

Remote index responses include only readable chart locations, ordered by version,
and recheck current access before delivery. This follows the P9 source contract;
the existing local Helm whole-index permission behavior is unchanged. The rewritten
response has its own ETag, independent of the raw upstream validator. Upstream
fields, annotations, dependency aliases and signing-key metadata are retained.

Charts are immutable cache artifacts. Cold retrieval verifies an advertised
SHA-256 digest, the complete gzip envelope, bounded chart metadata and matching
chart name/version before publishing anything. Provenance bytes are preserved;
admission checks the OpenPGP clear-sign envelope, chart identity and declared
SHA-256 against the index when present. **Helm verifies cryptographic trust with
the consumer's keyring.** The server does not infer a trusted signer from upstream
metadata. Cached provenance is checked against the current index identity and
digest before every delivery, including offline reads and conditional responses.
A structurally valid but tampered signature fails `helm pull --verify`.
The API refuses remote publishing.

Bounds are 32 MiB and 100,000 versions per index, 2 MiB per provenance document,
and the existing Helm archive limits (100 MiB expanded; 5 MiB per file), and one million
tar entries. Remote admission rejects traversal, absolute paths, links and special
entries, and requires complete tar termination and gzip trailers.
Malformed or ambiguous metadata is rejected before caching rather than publishing
a partial index. These admission/filtering and `_external` compatibility choices
are **[I]** until the P19 comparison in [#216](https://github.com/weaveplatform/artifactweave/issues/216).

`helm_remote.feature` proves Helm 4.3.1 cold `repo add/update`, signed pulls and
`verify`, byte preservation, fresh-client offline reuse in both URL modes and
rejection of a tampered signature. Controlled upstream tests cover redirects,
MIME variants, proxy/credential boundaries, metadata refresh/zap, no-store,
malformed bodies, digest/identity failures, permissions and cancellation.

Primary protocol sources checked 2026-10-22:
[Helm chart repositories](https://helm.sh/docs/topics/chart_repository/),
[Helm provenance](https://helm.sh/docs/topics/provenance/),
[Helm metadata validation](https://github.com/helm/helm/blob/v4.3.1/pkg/chart/v2/metadata.go),
and [JFrog Helm repositories](https://docs.jfrog.com/artifactory/docs/kubernetes-helm-chart-repositories).
These tests prove artifactweave behavior; supported Helm generation qualification
continues under P28/P41.
