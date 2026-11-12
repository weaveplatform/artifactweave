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

The qualification on that date used **Knife 19.0.134**, with **Chef 19.4.46** and
**knife-art 1.0.2**, the latest releases published in the official RubyGems
[Knife](https://rubygems.org/gems/knife), [Chef](https://rubygems.org/gems/chef)
and [knife-art](https://rubygems.org/gems/knife-art) feeds on that date. The
[generated image inventory](ci-client-images.md) records the current Knife, Chef,
knife-art and Ruby pins, including the base image manifest digest. JFrog's documented `knife-art` plugin supplies basic HTTP
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
knife artifactory list --config knife.rb
knife artifactory search widget --config knife.rb
knife artifactory install widget 1.0.0 --cookbook-path ./installed --config knife.rb
knife artifactory unshare widget 1.0.0 --yes --config knife.rb
```

`list` and `search` ask for `items=9999999` to avoid paging, and add `order` for
`--sort-by` and `user` for `--owned-by`. The registry answers with one page of
at most 1,000 cookbooks and takes `order` and `user` without applying them
([#1305](https://github.com/weaveplatform/artifactweave/issues/1305)). A search
looks in each cookbook's name, description and maintainer, as the
[JFrog guide's "Search for Chef Cookbooks"](https://www.jfrog.com/confluence/display/JFROG/Chef+Cookbook+Repositories)
says 7.x does for a local repository.

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
queries; `propagateQueryParams` need not be enabled. It asks the upstream for
100 cookbooks a page (`start=<n>&items=100`), since Supermarket serves at most
`API_ITEM_LIMIT` a page, 100 when that is not set, whatever `items` asks; on
**2026-11-12** `https://supermarket.chef.io/api/v1/cookbooks?start=0&items=1000`
answered 100 of its 4,078 cookbooks. A catalog page reads only the upstream
pages that cover it, from the 100-aligned page that holds `start`, so Knife's
list reads 10 pages and no cookbook or version record, and an upstream of any
size up to 2^30 cookbooks is served page by page
([#1306](https://github.com/weaveplatform/artifactweave/issues/1306)).
Supermarket's page states no limit, so a catalog page with more after it that
holds fewer than 100 answers 502, as does any page of more than 1,000
([#1298](https://github.com/weaveplatform/artifactweave/issues/1298)). A search
lists the matching cookbooks the remote's cache holds, then forwards the query
once to the upstream's `/api/v1/search` and adds its answer, leaving out a
cookbook already listed, as the JFrog guide describes. A virtual repository
lists its hosted members' cookbooks, then each remote member's upstream, in
member order; `start` counts on into the upstream's offsets and `total` adds
the upstream's. Downloads may redirect to
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
bounded to 4 MiB; hosted catalogs and versions to 4,096 entries. The catalog
and search accept `q`, `start`, `items`, `order` and `user` only, each once,
and return at most 1,000 entries per page.

Remote downloads validate size, declared SHA-256 when present, cookbook identity,
and the manifest's dependencies and platforms before cache publication. A
metadata authority change or invalid archive fails the request. Metadata and
catalog output are rebuilt for the current caller and use `private, no-store`.

The [`@chef` feature](../../test/acceptance/features/chef.feature) publishes real
Knife-built archives, installs a controlled cookbook and dependency through L/F,
performs remote redirect, refresh and offline installations into fresh Git
directories, and installs dependencies across two virtual members. The
[`@chef` knife catalog feature](../../test/acceptance/features/chef-knife-catalog.feature)
runs `knife artifactory list` and `search` against a local repository. Go protocol,
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
