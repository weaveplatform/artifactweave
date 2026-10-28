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
was also checked. Current commercial Puppet Core documentation identifies 9.0.0;
the latest publicly published [puppet gem](https://rubygems.org/gems/puppet) and
[open source release](https://github.com/puppetlabs/puppet/releases) available for
this reproducible client proof are **8.10.0**. The pinned client source's
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

Delete and trash restore use the normal item APIs and update the native view.
`POST /artifactory/api/puppet/{repo}/reindex` is the Artifactweave maintenance
endpoint for rebuilding hosted module indexes from archives. It requires Manage
and runs the existing persistent reindex worker. Remote metadata refresh uses
`POST /artifactory/api/zap/{repo}-cache/v3/releases`; the ordinary source policy
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

Remote metadata and archive URLs must remain under the configured Forge origin
and path. Every archive redirect is checked against that boundary. Download
validation checks the advertised SHA-256, size, archive identity and dependency
metadata before publication. Cache hits revalidate metadata and current read
permissions. Virtual metadata is filtered before disclosure and each download
reselects its current ordered member.

The [`@puppet` features](../../test/acceptance/features/puppet.feature) run the
[pinned Puppet client image](../../test/acceptance/clients/puppet.Dockerfile).
Controlled root and helper modules are committed with their source metadata and
archives. A fresh client installs both modules through L and one-node F, through
R with an archive redirect followed by an offline install, and through V with
the dependency in a second member. Each install uses a separate Puppet cache.
The Go format contract covers delete, restore, transfer and reindex. Protocol
and full application acceptance cover denied reads/writes, invalid metadata,
archive corruption, stale authority, source failure, cache refresh/offline,
version pagination and virtual member ordering and changes.

```sh
docker build -t artifactweave-accept-puppet \
  -f test/acceptance/clients/puppet.Dockerfile test/acceptance/clients
ARTIFACTWEAVE_PUPPET_IMAGE=artifactweave-accept-puppet \
ARTIFACTWEAVE_REQUIRE_DOCKER=1 ARTIFACTWEAVE_ACCEPT_TAGS=@puppet \
  go test ./test/acceptance -run '^TestAccFeatures$' -count=1
```

On macOS additionally set `ARTIFACTWEAVE_ACCEPT_LOCAL=1` and
`ARTIFACTWEAVE_PUPPET_DESKTOP=1` for Docker Desktop host access.
