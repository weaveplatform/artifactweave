# Docker, OCI and Helm OCI remote repositories

Remote `docker`, `oci` and `helmoci` repositories support Distribution v2 reads:
tag/digest manifests, indexes and their platform manifests, blobs, tags and OCI
referrers. Repository-path, Artifactory-prefix and configured subdomain routing
use the same local authentication challenge as local registries. Remote uploads,
mounts and deletes are refused. Virtual repositories now resolve manifest/blob
GET and HEAD through their local, federated and remote members. Virtual push, listings, timestamp tags and local referrers are implemented below.

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
Each server keeps the tag listings it has read decoded in memory, at most 64 MiB
of them, the least recently read dropped first, so the later pages of a listing
do not decode it again.

`test/acceptance/features/registry_remote.feature` exercises pinned native Linux
crane, ORAS and Helm against a real artifactweave upstream and remote, including
multi-platform images, referrers, zap and offline reads. Deterministic tests cover
foreign destinations, partial bodies, digest/schema failures, rate limits and
permission/user/password/parent-token revocation during fetch. These tests prove
artifactweave behavior, not an observed Artifactory response.

Protocol sources checked 2026-10-22: [OCI Distribution v1.1.1](https://github.com/opencontainers/distribution-spec/blob/v1.1.1/spec.md),
[OCI descriptor URLs and integrity](https://github.com/opencontainers/image-spec/blob/v1.1.1/descriptor.md)
and [Helm OCI registries](https://helm.sh/docs/topics/registries/).


## Virtual repositories

See the [shared virtual rules](virtual-repositories.md) for member order, origins,
visibility and remote-policy behavior.

A virtual uses the first readable member holding the requested tag, manifest digest
or blob. Nested virtuals flatten in effective priority order; a priority member's
miss still falls through. The virtual's own image-path patterns apply to admins
as well, then each member keeps its own read permission and download policy.
Authenticate against the virtual image name as usual; a virtual-scoped pull token
does not need separate member scopes. Manifest responses name their serving member
in `X-Artifactory-Docker-Registry`.

Digest-location hints expire after 24 hours, use at most 10,000 entries, and recheck
current authority before reuse. Tags are never keyed in this cache: a remote tag's
shared metadata TTL still decides when to refresh. Offline and origin-restricted
requests retain cache-only behavior. A missing cached digest holder triggers a
fresh member walk; changed membership or patterns discard the virtual's hints.
`FindParentManifestLists` searches the selected member's retained manifest tree.
Virtual pushes publish into `defaultDeploymentRepo`, an existing local or
federated member. Deploy permission is checked on that member's image; upload
locations and token scopes keep the virtual name. Without a DDR, publishing
answers 405 with `Allow: GET`. Upload sessions support monolithic/chunked writes,
status and cancellation through the virtual URL. A write-only pusher's unreadable
HEAD probes return 404; GET still requires Read.

Blob HEAD can find base layers in other readable members. Manifest PUT imports
missing layers/config and index children into the DDR before the normal presence
and size checks. An uncached remote uses its own permissions/source policy; an
offline remote lends only cached content. Cross-repository mounts may name a
virtual source or destination. Virtual and DDR write patterns apply to the image
name, with ordinary REST item deployments retaining their full-path patterns.

Manifest/blob DELETE through a virtual answers 400 with an empty body;
`DeleteDockerTag` answers the Artifactory 400 envelope. Address the concrete member
to delete a tag. Catalog/tag listings and referrers are supported as described below.

A virtual may enable `cachingLocalForeignLayersEnabled` to fetch a missing foreign
layer named by a readable local/federated manifest through an allowed remote
member. This selection is inferred from 7.x's XSD, pending #216. The remote's own
external-layer switch, URL patterns, network/cache and Deploy/Cache policy still
apply. The blob is cached in the remote, preserving the local manifest. A cached
blob remains available through normal member resolution when foreign fetching is
subsequently disabled. Timestamp tags and virtual referrers are described in
[decision 0031](../research/decisions/0031-docker-oci-registry.md).

Catalog and tag endpoints return bounded sorted/deduplicated pages with virtual
and member patterns applied before page cuts. Remote catalogs are read from that
remote's own source/cache; configured project prefixes expose pullable names.
Catalog ReadAny permits a path-scoped member reader to list all its image names
from an admitted catalog; a cold fetch retains the remote cache-publication gate;
tag lists still apply per-tag visibility. These are separate from manifest/blob
selection and the digest-location hints. Timestamp tags choose the newest eligible
local/federated manifest, effective order on ties, before considering remotes;
digest resolution ignores that option. Virtual referrers require image Read first
and union readable local/federated descriptors; remotes do not contribute by default.

[crane/ORAS/Helm/Docker scenarios](../../test/acceptance/features/registry_virtual.feature)
retain pull/push/timestamp/referrer and catalog/tag controls. Exact closed answers
remain [I] in the [listing entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6969664218)
and [timestamp/foreign-layer entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6972246717).
