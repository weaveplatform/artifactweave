# Python remote repositories

P9 issue [#627](https://github.com/weaveplatform/artifactweave/issues/627) implements
remote Python simple indexes, Warehouse JSON metadata, wheels, source distributions,
PEP 658/714 metadata sidecars and explicitly advertised detached `.asc` signatures.
Uploads remain refused. Existing local and federated upload and lifecycle behavior is
unchanged.

Configure a remote repository with `packageType: "pypi"`. `pyPIRegistryUrl` is the
registry API base (default `https://pypi.org`); `pyPIRepositorySuffix` is appended to
that base for simple-index requests (default `simple`, including custom values such
as `+simple` or `python/simple`). The normal `url` still defines the configured
artifact origin and its credential scope. Repository credentials are never copied to
an unrelated origin discovered in metadata.

Clients use `/artifactory/api/pypi/<repo>/simple/` as their only index. Project names
are normalized according to PEP 503. The global `pypi.simple_json` setting controls
JSON negotiation; HTML remains available. `pypi.core_metadata` controls advertising
and serving sidecars. Full raw upstream HTML/JSON is retained independently of the
cached package index. Downloading or reindexing a subset of distributions cannot
remove uncached releases from the upstream listing.

The proxy rewrites relative and absolute distribution links to its public base URL
and canonical `<project>/<version>/<filename>` paths. It preserves hash fragments,
requires-python, yanked values and metadata hashes. Warehouse project/release JSON
keeps upstream metadata and filters and rewrites distribution URLs. Response ETags
identify the emitted bytes. Different callers and public URLs cannot contaminate
the raw metadata cache. Current caller, source, credentials and each disclosed file's
Read permission are checked before delivery. Project-list responses require a readable
real distribution and use bounded project discovery, with a maximum of 256 extra project lookups; exceeding the
bound fails explicitly rather than returning a falsely complete list.

Complete indexes are schema-validated before cache admission, independently of the
first caller's permissions. Parsing is bounded at 64 MiB and one million entries.
HTML and JSON accept Simple API major version 1; an absent HTML version means 1.0,
and malformed or conflicting HTML version declarations are rejected. HTML/JSON
conversion preserves boolean detached-signature declarations.
Archive identity and advertised MD5/SHA1/SHA224/SHA256/SHA384/SHA512 or Warehouse
BLAKE2b-256 digests are checked before publication, including no-store responses.
Digests absent from stored checksum columns are rechecked against cached bytes.
Remote admission also reads every archive entry and compression trailer, checks ZIP
entry CRCs, and bounds complete expansion at 16 GiB and one million entries. ZIP
directory parsing is separately bounded at 64 MiB; cancellation stops validation.
Metadata sidecars and signatures are bounded at 1 MiB; malformed content cannot
replace a valid cache entry. Detached signature bytes are preserved; signature trust
and cryptographic verification remain the consumer's policy.

Distributions may live on other origins, as the Python simple API permits. Every
fetch still uses the shared DNS/SSRF, redirect and credential-origin checks. Unsafe
schemes, embedded credentials, downgrade links and encoded traversal are rejected.
Caller query strings never select upstream download URLs. Shared refresh, negative
cache, outage and offline behavior follows the [retrieval contract](../implementation/remote-retrieval-contract.md).
With the referenced metadata and distributions cached, pip and uv can install into
fresh environments with their client caches disabled while the upstream is offline.

Private-index XML-RPC search remains assigned to P28. Index-hosted attestation and
provenance endpoints remain assigned to P23; remote responses do not advertise an
unimplemented provenance endpoint. These capabilities remain in scope in the
[capability roadmap](../implementation/capability-roadmap.md).

The controlled upstream and real pip/uv scenarios establish this implementation's
behavior. They are not observed Artifactory parity evidence; decision
[0037](../research/decisions/0037-pypi.md) remains Proposed and capture questions remain
tracked by [#216](https://github.com/weaveplatform/artifactweave/issues/216).

Relative index links use the actual response URL after redirects. The private raw
cache records its sanitized scheme, host and path so cold, refreshed, no-store and
offline reads resolve links identically. An HTML `base` element before links may
select another safe base; ambiguous multiple or late bases are rejected. Admission
validates links against that same base before publication, including an unchanged
body returned with a 304 at a new redirect destination. Rewritten client responses
never expose this internal base or upstream signed query parameters.

The native fixture includes a yanked newer release and a release requiring Python
`>=99`, so pip and uv choose the usable older wheel. It uses a lower-bound
incompatibility because [uv intentionally ignores dependency Requires-Python upper
bounds](https://docs.astral.sh/uv/concepts/resolution/); the proxy preserves the
advertised constraint and leaves client resolver policy to the client.
