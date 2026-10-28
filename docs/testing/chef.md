# Chef native repositories

[#783](https://github.com/weaveplatform/artifactweave/issues/783) implements the
Chef Supermarket cookbook API for local, remote, virtual and one-node federated
repositories. Cross-peer convergence remains P13.

The [JFrog Chef guide](https://docs.jfrog.com/artifactory/docs/chef-repositories),
[repository class table](https://docs.jfrog.com/artifactory/docs/repository-support-for-package-clients)
and [Chef Supermarket API](https://docs.chef.io/supermarket/supermarket_api/)
were checked on **2026-10-28**. The JFrog guide describes the Artifactory 7.x
contract without a patch-specific compatibility claim; comparison with a live
Artifactory build remains #216. The approved INV-01 contract includes L/R/V/F.

The pinned real client is **Knife 19.0.134**, with **Chef 19.4.46** and
**knife-art 1.0.2**, the latest releases published in the official RubyGems
[Knife](https://rubygems.org/gems/knife), [Chef](https://rubygems.org/gems/chef)
and [knife-art](https://rubygems.org/gems/knife-art) feeds on that date. The
[client image](../../test/acceptance/clients/chef.Dockerfile) pins Ruby 4.0.9 by
manifest digest. JFrog's documented `knife-art` plugin supplies basic HTTP
authentication; plain `knife supermarket` uses Chef's RSA authentication flow.
Berkshelf is deprecated in the current JFrog guide and is not the client target.

## Connection recipe

Create a repository with `packageType: chef`. Keep the client configuration
private because its URL contains credentials. Use a dedicated user or access
token with Write for publishing and Read for installation. For example,
`knife.rb` can read the endpoint from an environment variable:

```ruby
knife[:supermarket_site] = ENV.fetch('CHEF_REGISTRY_URL')
cookbook_path ['./cookbooks']
node_name ENV.fetch('CHEF_REGISTRY_USER')
```

Set `CHEF_REGISTRY_URL` to
`https://<user>:<password-or-token>@registry.example/artifactory/api/chef/chef-local`,
URL-encoding the credentials. Then:

```sh
knife artifactory share widget Other --config knife.rb
knife artifactory install widget 1.0.0 --cookbook-path ./installed --config knife.rb
knife artifactory unshare widget 1.0.0 --yes --config knife.rb
```

Knife's installation directory must belong to an initialized Git repository;
the client commits installed cookbooks there. A cookbook source includes
`metadata.rb` and recipes; Knife builds `metadata.json` and the TAR archive.
Publish also accepts the Supermarket multipart `tarball` field. A version is
immutable through the native publish route: repeating a version returns 409.

Use a virtual key in the same URL to install across ordered L/R/F members.
Each returned version must pass the virtual and selected member permissions
and include/exclude patterns. First readable member wins a duplicate version.
Metadata links point to the virtual endpoint and each request resolves current
membership. Nested virtual membership returns 400 through the shared seam.

For R, configure the repository URL as the Supermarket root, normally
`https://supermarket.chef.io`. The adapter generates its own bounded pagination
queries; `propagateQueryParams` need not be enabled. Downloads may redirect to
object storage. Source DNS/SSRF and redirect limits apply, and credentials are
not forwarded across origins. Metadata links stay within the configured
registry origin and path. The shared source policy governs cache TTL, offline
reads, transient-failure fallback, read revocation and cache invalidation.

## Protocol and lifecycle

The API prefix is `/artifactory/api/chef/{repo}/api/v1`:

- `GET/HEAD /cookbooks` and `/search?q=widget` return filtered, paginated catalogs.
- `GET/HEAD /cookbooks/widget` returns version URLs and the latest readable version.
- `GET/HEAD /cookbooks/widget/versions/1_0_0` returns archive and dependency metadata;
  `latest` resolves the highest readable numeric version.
- `GET/HEAD /cookbooks/widget/versions/1_0_0/download` returns the validated archive.
- `POST /cookbooks/widget.tgz` accepts knife-art's raw binary publish;
  `POST /cookbooks` accepts the multipart Supermarket publish.
- `DELETE /cookbooks/widget/1.0.0` accepts knife-art's unshare;
  `/cookbooks/widget/versions/1_0_0` and whole-cookbook deletion are also supported.

The canonical item path is `widget/widget-1.0.0.tar.gz`. Ordinary item delete,
trash restore, copy and move preserve the native index. Manage-authorized
`POST /artifactory/api/chef/{repo}/reindex` rebuilds hosted indexes from archives
using the persistent reindex worker. Remote metadata refresh uses
`POST /artifactory/api/zap/{repo}-cache/api/v1/cookbooks`.

Remote cached metadata is private to the native API: generic item, checksum,
archive-entry and folder-download routes cannot disclose its unfiltered bytes.
Copy, move and checksum reuse cannot export it to another repository. A
non-administrator can restore it only to its original repository and path.
Ordinary cookbook archives retain normal item operations and permissions.

## Validation and evidence

Admission validates canonical path and manifest identity, two- or three-part
numeric versions, dependency and platform constraints, unique JSON fields,
complete gzip CRC and TAR structure. Archives require one cookbook-name root
and `metadata.json`; links, traversal, duplicate entries and trailing archives
are refused. Bounds: 64 MiB compressed, 128 MiB expanded, 1 MiB manifest,
4,096 entries, and 256 dependency/platform records. Metadata documents are
bounded to 4 MiB; catalogs and versions to 4,096 entries. Search accepts
`q`, `start` and `items` only, with at most 1,000 returned entries per page.

Remote downloads validate size, declared SHA-256 when present, cookbook identity,
and the manifest's dependencies and platforms before cache publication. A
metadata authority change or invalid archive fails the request. Metadata and
catalog output are rebuilt for the current caller and use `private, no-store`.

The [`@chef` feature](../../test/acceptance/features/chef.feature) publishes real
Knife-built archives, installs a controlled cookbook and dependency through L/F,
performs remote redirect, refresh and offline installations into fresh Git
directories, and installs dependencies across two virtual members. Go protocol,
format-contract and full-router tests cover malformed metadata, unsafe archives,
store/upstream failures, authorization narrowing, stale source authority,
ordered members, delete/restore/reindex and generic-route disclosure attempts.

```sh
docker build -t artifactweave-accept-chef \
  -f test/acceptance/clients/chef.Dockerfile test/acceptance/clients
ARTIFACTWEAVE_CHEF_IMAGE=artifactweave-accept-chef \
ARTIFACTWEAVE_REQUIRE_DOCKER=1 ARTIFACTWEAVE_ACCEPT_TAGS=@chef \
  GOWORK=off go test ./test/acceptance -run '^TestAccFeatures$' -count=1
```

On macOS also set `ARTIFACTWEAVE_ACCEPT_LOCAL=1` and
`ARTIFACTWEAVE_CHEF_DESKTOP=1` for Docker Desktop host access.
