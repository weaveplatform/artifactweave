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

Absolute and relative tarball references under the configured registry root are
rewritten to artifactweave. A reference outside that root requires
`externalDependenciesEnabled: true` and a matching `externalDependenciesPatterns` URL
pattern. Unsafe schemes, userinfo, fragments, traversal and HTTPS downgrades are refused.
The transport separately rejects prohibited network destinations and does not send
origin credentials to another authority. An allow pattern does not bypass that policy.

P10 owns virtual-repository dependency-spec rewriting and
`externalDependenciesRemoteRepo`, including Git shorthand dependencies. These are
separate from the remote `dist.tarball` handling above. P28/#362 owns local ECDSA signing,
key discovery and native signature verification, and P28 owns audit proxying. Audit
requests do not receive a synthetic empty-success response. P21/#262 owns browser login;
legacy authentication remains available.

`npm_remote.feature` runs npm 11 and npm 12: cold scoped/unscoped install, lockfile URLs
and integrity, then `npm ci` from a second empty client cache with upstream stopped and
the server offline. The fixture demonstrates artifactweave behavior. Artifactory
comparison and the inferred remote error/filtering shapes remain P19/#216 work.
