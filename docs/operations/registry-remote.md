# Docker, OCI and Helm OCI remote repositories

Remote `docker`, `oci` and `helmoci` repositories support Distribution v2 reads:
tag/digest manifests, indexes and their platform manifests, blobs, tags and OCI
referrers. Repository-path, Artifactory-prefix and configured subdomain routing
use the same local authentication challenge as local registries. Remote uploads,
mounts and deletes are refused; virtual aggregation remains P10.

```sh
curl --fail --user admin "$REGISTRY/artifactory/api/repositories/dockerhub" \
  --request PUT --header 'Content-Type: application/json' \
  --data '{"rclass":"remote","packageType":"docker","url":"https://registry-1.docker.io","enableTokenAuthentication":true}'
crane pull "$REGISTRY_HOST/dockerhub/library/alpine:latest" alpine.tar
```

Authenticate the client against artifactweave. Configure the upstream account on
the remote separately. `dockerProjectId` prefixes the upstream image namespace;
it is not part of the downstream name. The [upstream authentication guide](../implementation/remote-registry-authentication.md)
describes challenge realms, Docker Hub's trusted token endpoint and credential
rotation. Caller tokens, passwords and cookies never become upstream credentials.

Every cold GET or HEAD resolves a complete representation and verifies SHA-256
before delivery. A cold range fetches and validates the whole blob first. Manifest
bytes remain unchanged, including OCI signatures and subjects. Content type,
schema, digest headers and referenced manifest/config sizes are checked; an index's
child manifests and configs are verified before publishing the index. Layers are
fetched lazily and verified when requested. Warm GET/HEAD, ranges and conditions
use cached bytes. The supported digest algorithm is SHA-256; additional algorithms
remain owned by P28.

Tags use metadata freshness; digest addresses are immutable. Zap expires tag and
listing metadata while retaining complete blobs and digest-addressed manifests.
A refreshed tag cannot replace bytes under the previous digest. `offline` serves
retained content without upstream traffic. Shared stale/error, negative-cache,
no-store and explicit deletion fences follow the [retrieval contract](../implementation/remote-retrieval-contract.md).
Successful GET/range delivery records use; HEAD and 304 do not.

Cached manifests and indexes use `<image>/<tag-or-sha256:hex>/manifest.json`;
blobs use `<image>/_uploads/sha256__<hex>`. Both manifest kinds retain their exact
media type and native Docker properties/package indexing. This remote index
filename differs from local pushes' `list.manifest.json`: a cache key and its
published item must have the same path even before the response type is known.
Digest folders retain a colon, unlike Artifactory's documented remote
`sha256__<hex>` form, to keep legal tags of that spelling separate from immutable
content. These distinctions are artifactweave implementation choices **[I]**, awaiting the
P19 comparison in [#216](https://github.com/weaveplatform/artifactweave/issues/216).
Tag metadata participates in native item indexing while authoritative tags and
referrer listings stay independent of the locally cached subset. Helm config
content supplies chart metadata to the existing package hook. Remote pulls do not
apply local push tag-retention settings.

Foreign layer descriptors require `externalDependenciesEnabled: true`, at least
one matching `externalDependenciesPatterns` URL pattern, and approval by the
shared destination policy. Disabled, unmatched or unsafe descriptors fail closed
before the manifest is returned, without rewriting signed bytes. The registry
blob endpoint is tried first; a 404 can use the descriptor's first allowed URL.
Redirects must pass the same pattern and destination checks. External requests
carry no registry authorization, cookies or custom secret headers. Clients receive
validated bytes through artifactweave, never a generated direct foreign URL.

Metadata bounds are 4 MiB per manifest/referrer document and 16 MiB per combined
tag listing, with at most 128 upstream pages. Index traversal allows eight levels,
64 children per index and 256 fetched child references per traversal. Descriptor
lists are bounded to 4096 entries and 16 URLs per descriptor. Oversized graphs and
unsafe pagination links fail without publishing the parent representation.

`test/acceptance/features/registry_remote.feature` exercises pinned native Linux
crane, ORAS and Helm against a real artifactweave upstream and remote, including
multi-platform images, referrers, zap and offline reads. Deterministic tests cover
foreign destinations, partial bodies, digest/schema failures, rate limits and
permission/user/password/parent-token revocation during fetch. These tests prove
artifactweave behavior, not an observed Artifactory response.

Protocol sources checked 2026-10-22: [OCI Distribution v1.1.1](https://github.com/opencontainers/distribution-spec/blob/v1.1.1/spec.md),
[OCI descriptor URLs and integrity](https://github.com/opencontainers/image-spec/blob/v1.1.1/descriptor.md)
and [Helm OCI registries](https://helm.sh/docs/topics/registries/).
