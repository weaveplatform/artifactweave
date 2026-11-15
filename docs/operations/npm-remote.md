# npm remote repositories

An npm remote serves full and abbreviated packuments, version documents, dist-tags,
`/-/v1/search`, and scoped/unscoped tarballs at
`/artifactory/api/npm/<repository>/`. Configure the repository's `url` as the upstream
registry root, then use the normal npm registry and authentication settings. A reader
needs Read permission; cache population does not require Deploy. npm publication,
unpublish, deprecation and dist-tag writes to a remote answer 405.

Raw upstream JSON is cached independently from the package index. Each response filters
versions and search results by their canonical tarball paths, rewrites `dist.tarball`
through the public base URL, and computes validators for those response bytes. Unknown
compatible metadata, upstream `dist.integrity`, `dist.shasum` and signature fields remain
present. Two public URLs cannot poison each other's metadata, and reindexing cached
packages does not replace upstream signatures with local values. Current caller,
repository, credential and per-disclosed-path permissions are checked before delivery.

Tarballs use the shared remote transport and ingestion policy. SHA-512/SHA-384/SHA-256/SHA-1
metadata pins are checked before publication and on cached retrieval, independently of
optional upstream checksum-header policy. Integrity verification selects the strongest
supported algorithm and accepts any matching digest at that strength; a weaker match
cannot rescue a stronger mismatch. SHA-384 is validated against complete bytes,
including offline reuse. No local signatures are generated. Metadata expiry, tag
movement, negative caching, outage fallback, no-store and offline behavior
follow the [remote retrieval contract](../implementation/remote-retrieval-contract.md).
`npm ci` can use a fresh client cache while the server is offline after the referenced
packuments and tarballs have been fetched.

Each version's `dist.tarball` is rewritten to the version's canonical path under the
remote, and a tarball download fetches the file from the URL the upstream's packument
gives, resolved against the configured registry URL: under the registry, elsewhere on
its host, or on another host such as a CDN. 7.x follows a tarball's location in the same
way: a registry whose tarballs lie outside its own root stopped answering 404 in 7.24.1
([RTFACT-23931](https://jfrog.atlassian.net/browse/RTFACT-23931)), and 7.161.26 uses an
npm handler's download URL on another host as it stands. No repository field gates it.
`externalDependenciesEnabled` and `externalDependenciesPatterns` belong to an npm
virtual repository's dependency rewrite (P10); an npm remote ignores them (#1574).

Safety rules still apply to every tarball URL. Userinfo, fragments, non-HTTP(S) schemes,
traversal and HTTPS downgrades are refused, so the packument and its version and
dist-tags reads answer 502. The transport separately rejects prohibited network
destinations, such as private and cloud metadata addresses an operator has not allowed,
after DNS and every redirect, so that tarball answers 502 while its version stays
listed. Origin credentials are not sent to another authority unless `allowAnyHostAuth`
is on and that authority uses HTTPS
([transport contract](../implementation/remote-http-transport.md)).

P10 owns virtual-repository dependency-spec rewriting and
`externalDependenciesRemoteRepo`, including Git shorthand dependencies. These are
separate from the remote `dist.tarball` handling above. Local and federated repositories
sign with ECDSA and serve `/-/npm/v1/keys` (#362, decision 0034). A remote relays its
upstream's `dist.signatures` and answers `/-/npm/v1/keys` with the upstream's keys
document (#1318, decision 0034, "Signatures"), so `npm audit signatures` verifies
packages installed through a remote of a signing upstream; virtual signature and key
merging is #1319, and P28 owns audit proxying. Audit requests do not receive a synthetic
empty-success response. P21/#262 owns browser login;
legacy authentication remains available.

`npm_remote.feature` runs npm 11 and npm 12: cold scoped/unscoped install, lockfile URLs
and integrity, then `npm ci` from a second empty client cache with upstream stopped and
the server offline. The fixture demonstrates artifactweave behavior. Artifactory
comparison and the inferred remote error/filtering shapes remain P19/#216 work, among
them the `dist.tarball` 7.x writes for a tarball outside the registry, which no source
shows [I].
