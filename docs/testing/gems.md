# RubyGems and Bundler client guide

The native RubyGems route is `https://<host>/artifactory/api/gems/<repo>/`.
Use a repository with `packageType: gems`. Local, remote, virtual and
one-node federated repositories serve the compact `names`, `versions` and
`info/<gem>` indexes, RubyGems `specs.4.8.gz` and `quick/Marshal.4.8`
gemspecs, and archives under `gems/`. A gem archive's filename must agree
with its embedded name, version and platform. Cross-peer federation is
tracked by P13.

With an account allowed to read the repository, configure the source over
HTTPS and install a pinned version:

```sh
gem install rake -v 12.3.1 --source https://<user>:<token>@<host>/artifactory/api/gems/<repo>/ --clear-sources --no-document
```

For Bundler, write `source 'https://<user>:<token>@<host>/artifactory/api/gems/<repo>/'`
and `gem 'rake', '12.3.1'` to a Gemfile, then run `bundle install`. Avoid
committing credentials in Gemfiles or lockfiles. `gem push` sends the archive
to `/artifactory/api/gems/<repo>/api/v1/gems`; the standard Artifactory item
`PUT /artifactory/<repo>/gems/<file>.gem` is also accepted. A local or
one-node federated repository can rebuild its index with
`POST /artifactory/api/gems/<repo>/reindex`. Deletion and restore through the
item API update the index after reindex.

The server validates archive checksums, bounded gem metadata and identity
before storing a gem. Compact `versions` rows include the MD5 of each
`info` body, and `info` rows include SHA256 of each archive. Remote archive
fetches require a matching index entry and validate the downloaded archive
against it. Repeated gem names in the remote append-only `versions` index
accumulate additions and yanks; the last row supplies the current `info`
MD5. Remote metadata is rendered from gem entries the caller can read, so
denied archives are absent from compact, Marshal and JSON indexes. Remote
cache freshness, offline reads and redirect rules follow the shared remote
source policy. A virtual repository walks its configured
members in order, deduplicates identical archive paths and omits entries
that the caller cannot read in both the virtual and member repositories.

The [current JFrog guide](https://docs.jfrog.com/artifactory/docs/rubygems-repositories)
documents compact indexes on L/R/V, but says **virtual priority resolution
is limited after deprecation of `/dependencies`**. The signed
[format inventory](../implementation/package-format-inventory.json) calls
for ordered virtual members. Here, ordering selects the first readable
member for an identical archive path; Bundler may still choose among
different versions according to its own dependency solver. This is a
specific compatibility boundary, not a claim that JFrog guarantees
priority selection for every Bundler resolution. The
[RubyGems compact index specification](https://guides.rubygems.org/rubygems-org-compact-index-api/)
defines the index and checksum fields; the
[RubyGems API](https://guides.rubygems.org/rubygems-org-api/) defines push.

## Reproducible client evidence

[`@gems` acceptance](../../test/acceptance/features/gems.feature) runs
`gem install` and `bundle install` against all four approved classes. The
[Ruby client image](../../test/acceptance/clients/ruby.Dockerfile) pins
Ruby 4.0.9 by digest. The genuine
[`rake-12.3.1.gem`](../../internal/format/gems/testdata/rake-12.3.1.gem)
was downloaded from RubyGems.org and has SHA256
`cf37840d136743411e4bd601885363e34dce537675306464528b0cafe428b1c5`.

```sh
docker build -t artifactweave-accept-ruby -f test/acceptance/clients/ruby.Dockerfile .
ARTIFACTWEAVE_ACCEPT_LOCAL=1 ARTIFACTWEAVE_RUBY_DESKTOP=1 \
  ARTIFACTWEAVE_RUBY_IMAGE=artifactweave-accept-ruby \
  ARTIFACTWEAVE_ACCEPT_TAGS=@gems make accept
```
