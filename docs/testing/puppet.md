# Puppet native repositories

[#784](https://github.com/weaveplatform/artifactweave/issues/784) implements
Puppet Forge v3 module installation and dependency resolution for local,
remote, virtual, and one-node federated repositories. Cross-peer federation
remains P13.

The [JFrog Puppet guide](https://docs.jfrog.com/artifactory/docs/puppet-repositories)
and [class matrix](https://docs.jfrog.com/artifactory/docs/repository-support-for-package-clients)
were checked on **2026-10-28**. The Puppet page reported an update five months
previously and describes the Artifactory 7.x contract without a specific patch
version; this is documentation verification, not live Artifactory parity (#216).
The [official Forge API documentation](https://help.puppet.com/core/current/Content/PuppetCore/module-delete-release-api.htm)
was also checked. On that date commercial Puppet Core documentation identified 9.0.0;
the latest publicly published [puppet gem](https://rubygems.org/gems/puppet) and
[open source release](https://github.com/puppetlabs/puppet/releases) available for
that reproducible client proof were **8.10.0**. The [generated image inventory](ci-client-images.md)
records the current client pin. The reviewed 8.10.0 source's
[`forge.rb`](https://github.com/puppetlabs/puppet/blob/8.10.0/lib/puppet/forge.rb)
and [`repository.rb`](https://github.com/puppetlabs/puppet/blob/8.10.0/lib/puppet/forge/repository.rb)
define release pagination, dependency metadata, SHA-256 verification, and
repository-relative file URIs. Dependency range syntax follows the official
[`semantic_puppet` parser](https://github.com/puppetlabs/semantic_puppet/blob/main/lib/semantic_puppet/version_range.rb)
checked on the same date, including bounded OR and hyphen ranges.

## Connection recipe

Create a repository with `packageType: puppet`. Upload the module archive to
`<owner>-<module>/<owner>-<module>-<version>.tar.gz` through the ordinary item API:

```sh
curl --fail -H "Authorization: Bearer $ARTIFACTWEAVE_TOKEN" \
  --upload-file acme-widget-1.0.0.tar.gz \
  https://registry.example/artifactory/puppet-local/acme-widget/acme-widget-1.0.0.tar.gz

puppet module install acme-widget \
  --module_repository https://registry.example/artifactory/api/puppet/puppet-local \
  --forge_authorization "Bearer $ARTIFACTWEAVE_TOKEN" \
  --target-dir ./modules
```

Use the virtual repository key in the same URL to resolve dependencies across
ordered members. A remote repository's URL is its Forge API root, normally
`https://forgeapi.puppet.com`. The native adapter supplies the mandatory Forge
query fields itself; `propagateQueryParams` need not be enabled. Tokens need
Read on each advertised archive path and on each selected virtual member.
Deploy requires Write, and replacement also requires Delete.

`GET .../v3/releases?module=acme-widget` lists versions and dependency constraints.
`GET .../v3/modules/acme-widget` and `GET .../v3/releases/acme-widget-1.0.0`
return module and release metadata. Search currently supports an exact module
name (`GET .../v3/modules?query=acme-widget`). Arbitrary search queries and nested
virtual members are refused explicitly.

The release list and the search page as the whole-repository lists below do: 20
entries unless `limit` asks for up to 100, a larger `limit` served as 100, under
the Forge's `pagination` object written before the results. Its links keep
`module` (or `query`) first, then `limit` and `offset`, as 7.x's own answer does
([RTFACT-14557](https://jfrog.atlassian.net/browse/RTFACT-14557):
`"first" : "/v3/releases?module=sql&limit=20&offset=0"`), and every answer
writes `&`, `<` and `>` as they are. The Puppet client follows `next`, and
`puppet_forge` walks from `first`. Whether 7.x caps the release list and the
search at 100 as it caps the modules list is [I] (#216).

The release list honours `exclude_fields`, which the Puppet client sends
separated by commas (`readme,changelog,license,uri,module,tags,supported,`
`file_size,downloads,created_at,updated_at,deleted_at`, since PUP-9364) and the
Forge's v3 API description gives separated by spaces: the top-level record
fields it names are left out, and a name no record has leaves nothing out. Its
links then keep `sort_by` and `exclude_fields` after `module`, so every page
answers as the first did. Both are [I] (#1327).

Release and module records carry the fields of JFrog's REST API reference for
the four Forge operations (archived jfrog.com/help pages) with the ones 7.x's
answers add: a release's `slug`, `module` (with its `owner` and
`deprecated_at`), `supported`, `file_md5`, `file_sha256`, `downloads`,
`readme`, `changelog`, `license` and dates, and a module's `downloads`, dates,
`endorsement`, `module_group`, `owner` (with `slug` and `gravatar_id`),
`homepage_url` and `issues_url`. A hosted release takes `file_md5`,
`downloads` and its dates from its archive item, written in the Forge's form
(`2017-07-16 12:07:00 +0000`) in UTC, and its `tags` from metadata.json, as the
Forge derives them; its `readme`, `changelog` and `license` are null, since 7.x
reads only an archive's metadata.json. A remote relays its upstream's values
([RTFACT-19130](https://jfrog.atlassian.net/browse/RTFACT-19130)), `tags`
included. A release's `metadata` is its whole metadata.json: `name`, `version`,
`source`, `author`, `license`, `summary`, `description`, `project_page`,
`issues_url`, `tags` and `dependencies` first, in the order RTFACT-19130's
6.8.6 answer writes them and null where the file has none, then the file's
other keys in the file's order (#1325) [I].

A hosted or virtual module takes its downloads (summed), dates (earliest and
latest) and links (the current release's `project_page` and `issues_url`) from
its releases [I]. A remote's module relays the downloads, dates, `supported`,
`endorsement`, `module_group`, owner `gravatar_id` and links of its upstream's
own record, `v3/modules/<m>`, asked with `exclude_fields=current_release
releases`, and lists the releases its release pages give the caller. When the
upstream answers 404 for that record, `v3/modules/<m>` answers 404 and the
search finds nothing, while the release list still lists the module's releases
(#1326) [I]. RTFACT-19130 reports a 6.8.6 remote answering 404 for a module the
Forge served; that is the fault the report names, not an answer to keep.

`GET .../v3/releases` and `GET .../v3/modules` with no query list every release
and module a local or federated repository hosts that the caller may read, as
7.x's [Get Puppet Releases](https://docs.jfrog.com/artifactory/reference/getpuppetreleases)
and [Get Puppet Modules](https://docs.jfrog.com/artifactory/reference/getpuppetmodules)
do. They page 20 entries unless `limit` asks for up to 100 (a larger `limit` is
served as 100, as 7.x serves it, RTFACT-30529), from `offset`. Each answer
carries the Forge's `pagination` object, `limit`, `offset`, `first`,
`previous`, `current`, `next` and `total`, as JFrog's sample answer does;
`puppet_forge`, which r10k and librarian-puppet use, reads it there. Modules
are listed by name and releases by module name, newest first. A virtual
repository answers both lists 404, as 7.x does
([RTFACT-15072](https://jfrog.atlassian.net/browse/RTFACT-15072)). A remote
repository still answers them 400: JFrog support says a remote serves them, but
no source shows what it lists.

Delete and trash restore use the normal item APIs and update the native view.
`POST /artifactory/api/puppet/{repo}/reindex` is the Artifactweave maintenance
endpoint for rebuilding hosted module indexes from archives. It requires Manage
and runs the existing persistent reindex worker. Remote metadata refresh uses
`POST /artifactory/api/zap/{repo}-cache/v3/releases` (and `.../v3/modules` for
the module records); the ordinary source policy
controls TTL, offline access, and fallback to complete cached metadata during a
transient upstream outage. A cold upstream failure cannot create a release.

## Validation and evidence

Archive admission checks package/path identity, one matching root metadata.json,
unique JSON identity and dependency fields, semantic versions, dependency
constraints, gzip integrity, and safe TAR paths. Links and duplicate entries are
refused. Limits are 64 MiB compressed, 128 MiB unpacked, 1 MiB metadata, 4,096
archive entries and 256 dependencies. Release metadata is limited to 16 MiB per
page and 4,096 releases overall; the adapter constructs each pagination request
and never follows upstream pagination URLs.

A remote asks its upstream for 20 releases a page, the Forge's own default
([RTFACT-14274](https://jfrog.atlassian.net/browse/RTFACT-14274)), with
`exclude_fields=reference tasks plans pdk validation_score malware_scan
deleted_for`: the fields no record answers, REFERENCE.md's text among them.
A page keeps each release's `readme`, `changelog` and `license`, which the
records relay, and the Forge's 100 a page of theforeman-foreman came to
9,947,601 bytes on 2026-11-12; puppetlabs-apache's README, CHANGELOG and
LICENSE come to about 240 KB a release and its REFERENCE.md to 398 KB in its
sources, so 100 of them would pass the 16 MiB a page (#1300). The Forge serves
at most 100 whatever `limit` asks (it answered `limit=1000` with
`"limit": 100` on 2026-11-12), so a smaller page costs only more requests.
What 7.x's remote asks is [I] (#216). A client's own `exclude_fields` is not
passed on. Each page starts after the releases the last one held. A page short
of 20 with more after it is accepted only when the upstream's
`pagination.limit` equals what the page held; short for any other reason, it
answers 502, since the remote cannot tell where the next page starts. A
release whose metadata.json is larger than the 1 MiB an archive's may be is
invalid upstream metadata (502), since no archive could match it.

Remote metadata and archive URLs must remain under the configured Forge origin
and path. Every archive redirect is checked against that boundary. Download
validation checks the advertised SHA-256, size, archive identity and dependency
metadata before publication. Cache hits revalidate metadata and current read
permissions. Virtual metadata is filtered before disclosure and each download
reselects its current ordered member.

The [`@puppet` features](../../test/acceptance/features/puppet.feature) run the
[pinned Puppet client image](../../test/acceptance/clients/puppet/Dockerfile).
Controlled root and helper modules are committed with their source metadata and
archives. A fresh client installs both modules through L and one-node F, through
R with an archive redirect followed by an offline install, and through V with
the dependency in a second member. Each install uses a separate Puppet cache.
[`puppet_paging.feature`](../../test/acceptance/features/puppet_paging.feature)
installs the oldest of 25 releases of a local module, which the second page of
its release list holds, so the client follows `pagination.next`.
The Go format contract covers delete, restore, transfer and reindex. Protocol
and full application acceptance cover denied reads/writes, invalid metadata,
archive corruption, stale authority, source failure, cache refresh/offline,
version pagination and virtual member ordering and changes.

The image runs Puppet on Ruby 3.3, and its Dependabot entry holds it there. On
Ruby 3.4 the pinned Puppet cannot extract any module archive:
`Puppet::Util.safe_posix_fork` closes every descriptor it lists in
`/proc/self/fd`, including the one its own walk is reading, and Ruby 3.4 raises
`Errno::EBADF` on that walk where Ruby 3.3 ignored it
([#1126](https://github.com/weaveplatform/artifactweave/issues/1126),
[Red Hat bug 2349352](https://bugzilla.redhat.com/show_bug.cgi?id=2349352)). On
Ruby 4 the gem does not install, because every Facter 4 release declares Ruby
below 4.0.

```sh
docker build -t artifactweave-accept-puppet test/acceptance/clients/puppet
ARTIFACTWEAVE_PUPPET_IMAGE=artifactweave-accept-puppet \
ARTIFACTWEAVE_REQUIRE_DOCKER=1 ARTIFACTWEAVE_ACCEPT_TAGS=@puppet \
  go test ./test/acceptance -run '^TestAccFeatures$' -count=1
```

On macOS additionally set `ARTIFACTWEAVE_ACCEPT_LOCAL=1` and
`ARTIFACTWEAVE_PUPPET_DESKTOP=1` for Docker Desktop host access.
