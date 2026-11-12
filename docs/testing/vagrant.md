# Vagrant box repositories

Phase 9c [#785](https://github.com/weaveplatform/artifactweave/issues/785) adds
native Vagrant metadata for local and one-node federated repositories. The
[JFrog guide](https://docs.jfrog.com/artifactory/docs/vagrant-repositories)
defines property-based publication and the metadata endpoint. The registered
class matrix adds one-node federation; remote and virtual Vagrant repositories
are unsupported. Cross-peer convergence remains P13.

## Publish and install

Create a repository with `packageType: vagrant` and `rclass: local` or
`federated`. Upload a `.box` to any item path with all three identity properties:

```sh
curl --fail --user "$ARTIFACTWEAVE_USER:$ARTIFACTWEAVE_TOKEN" \
  --upload-file example.box \
  "$ARTIFACTWEAVE_URL/artifactory/vagrant-local/releases/example.box;box_name=acme%2Fexample;box_provider=virtualbox;box_version=1.0.0"

export ATLAS_TOKEN="$ARTIFACTWEAVE_TOKEN"
export VAGRANT_SERVER_URL="$ARTIFACTWEAVE_URL/artifactory/api/vagrant/vagrant-local"
vagrant box add "$VAGRANT_SERVER_URL/acme/example" \
  --provider virtualbox --box-version 1.0.0
vagrant box list
```

Use HTTPS and a token whose principal can read the intended archive paths.
Vagrant 2.4.9 sends the token as a Bearer header to the configured server host,
including archive downloads. The token is never embedded in generated metadata
URLs, and a token in a URL's `access_token` query authenticates nothing
([DV-148](../implementation/deviations.md#security)). So Vagrant's legacy URL-token mode,
`VAGRANT_SERVER_ACCESS_TOKEN_BY_URL`, does not work: leave it unset.

`GET` and `HEAD /artifactory/api/vagrant/<repo>/<box-name>` expose readable
versions and providers, optional architecture, a SHA-256 checksum and each
box's download URL. A name can be a single component or
`namespace/name`. Each file must carry exactly one valid `box_name`,
`box_provider` and `box_version`; the provider must agree with the archive's
root `metadata.json`. Additional item properties can filter the metadata request
through the existing matrix-property syntax.

The download URL is Artifactory's native box file route,
`/artifactory/api/vagrant/<repo>/<path>.box`, where `<path>.box` is the box's
item path. Artifactory 7.x's catalogs point there
([RTFACT-17993](https://jfrog.atlassian.net/browse/RTFACT-17993)), and Vagrant
downloads from it
([RTFACT-27264](https://jfrog.atlassian.net/browse/RTFACT-27264)). `GET` and
`HEAD` of it serve the stored box as the item download does, with its checksum
headers. A `Range` request answers 206, so an interrupted download resumes;
Artifactory ignores the `Range` on this route, a bug that RTFACT-27264 and
[RTFACT-13807](https://jfrog.atlassian.net/browse/RTFACT-13807) report. The
route checks read permission on the path first, then the repository's blackout
and its include and exclude patterns, and answers 404 "Could not find resource"
when no file is stored at the path. A path that ends `.box` and carries no
matrix parameters is a box file, so a box whose name ends `.box` is reached only
with a matrix parameter, such as `;box_provider=virtualbox`.

The server validates tar, gzip-compressed tar and ZIP boxes before indexing,
including archive paths, member types, compression integrity and root metadata.
It streams VM disk contents, limits archives to 16 GiB compressed and 64 GiB
expanded, and checks ZIP central directories against 100,000 entries and 32 MiB
before parsing allocates memory. ZIP64 offsets support large VM disks. It never
executes the embedded `Vagrantfile`. The native client verifies the advertised
checksum before installing the selected box.

## Updates and lifecycle

Upload another box with the same name and provider and a new version, then run:

```sh
vagrant box outdated --global
vagrant box update --box acme/example --provider virtualbox
```

Deletion removes the affected metadata entry. Trash restore, copy and move use
the normal item hooks and retain the matching box identity. Changes to identity
properties reconcile the index; incomplete identity or a provider inconsistent
with the validated archive is not advertised. Derived archive proof properties
cannot be edited through the property API. Maintenance can rebuild the package
index through the format's existing reindex hook; this change adds no new
Vagrant-specific reindex HTTP API.

Metadata applies current read permissions and include/exclude patterns to every
archive, so a partially permitted user only sees permitted providers and
versions. Multiple readable files for the same version, provider and
architecture select the most recently updated file, with a stable path
tie-break. Metadata is private and not cached by clients or shared proxies.

## Native-client proof

The [acceptance image](../../test/acceptance/clients/vagrant.Dockerfile) installs
the official Vagrant 2.4.9 Linux amd64 package, checked against
[HashiCorp's release checksums](https://releases.hashicorp.com/vagrant/2.4.9/vagrant_2.4.9_SHA256SUMS).
This is the latest stable version listed by
[HashiCorp](https://developer.hashicorp.com/vagrant/install) on 2026-10-27;
2.4.10 builds are development prereleases. Linux amd64 is the published installer
architecture, so the image explicitly builds for that platform, including on
ARM Docker Desktop hosts.

The [`@vagrant` scenarios](../../test/acceptance/features/vagrant.feature) use a
fresh client home to install a pinned version, list it, discover and install an
update, select another provider from a ZIP box, verify extracted fixture bytes, and reject an
invalid token. Both local and one-node federated repositories run the same
native sequence. Controlled box fixtures contain metadata and a stand-in disk
payload; these tests prove registry operations and do not boot a VM.

```sh
docker build --platform=linux/amd64 -t artifactweave-accept-vagrant \
  -f test/acceptance/clients/vagrant.Dockerfile test/acceptance/clients
ARTIFACTWEAVE_VAGRANT_IMAGE=artifactweave-accept-vagrant \
ARTIFACTWEAVE_REQUIRE_DOCKER=1 ARTIFACTWEAVE_ACCEPT_TAGS=@vagrant \
  go test -count=1 -timeout=10m -run '^TestAccFeatures$' ./test/acceptance
```

On macOS, also set `ARTIFACTWEAVE_ACCEPT_LOCAL=1` and
`ARTIFACTWEAVE_VAGRANT_DESKTOP=1`; the client then reaches the host registry
through `host.docker.internal`.

Protocol sources, checked 2026-10-27:
[JFrog publication and connection](https://docs.jfrog.com/artifactory/docs/vagrant-repositories),
[box archive format](https://developer.hashicorp.com/vagrant/docs/boxes/format),
[catalog metadata](https://developer.hashicorp.com/vagrant/docs/boxes/box_repository),
and [Vagrant 2.4.9 authentication middleware](https://github.com/hashicorp/vagrant/blob/v2.4.9/plugins/commands/cloud/auth/middleware/add_downloader_authentication.rb).
Sources for the box file route and URL tokens, checked 2026-11-12:
[RTFACT-17993](https://jfrog.atlassian.net/browse/RTFACT-17993),
[RTFACT-27264](https://jfrog.atlassian.net/browse/RTFACT-27264),
[RTFACT-13807](https://jfrog.atlassian.net/browse/RTFACT-13807),
[RTFACT-17005](https://jfrog.atlassian.net/browse/RTFACT-17005) and
[RTFACT-26348](https://jfrog.atlassian.net/browse/RTFACT-26348).
