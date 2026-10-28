# Ansible collections

ArtifactWeave serves Galaxy v3 collections from `ansible` local, remote, virtual,
and one-node federated repositories. The supported, pinned native client is
**ansible-core 2.21.5** (Python 3.14); the acceptance suite builds its own collections
and installs a ranged dependency from an empty client directory.

## Connect ansible-galaxy

Configure an isolated `ansible.cfg` and protect its credentials:

```ini
[galaxy]
server_list = artifactweave

[galaxy_server.artifactweave]
url = https://<host>/artifactory/api/ansible/<repository>/
username = <user>
password = <password-or-access-token>
```

```sh
export ANSIBLE_CONFIG="$PWD/ansible.cfg"
ansible-galaxy collection build ./collection --output-path ./dist
ansible-galaxy collection publish ./dist/team-example-1.0.0.tar.gz
ansible-galaxy collection install 'team.example:>=1.0.0,<2.0.0'
```

Use local or federated repositories for publish. Remote and virtual repositories
serve collection discovery, dependency metadata and downloads; publishing to them
returns 405. All discovery, version metadata and archives require read permission;
a publication requires deploy permission on its final archive path. Publishing an
existing version through Galaxy returns 409.

Direct item deployment uses
`collections/<namespace>/<name>/<version>/<namespace>-<name>-<version>.tar.gz`.
Namespace/name use lowercase letters, digits and underscores, beginning with a
letter. Versions are three-component semantic versions, including prerelease/build
suffixes. The archive identity must match the path. Local and federated item
writes, copy/move, delete, restore and the existing item reindex work rebuild the
package index. There is no invented Ansible-specific REST reindex endpoint.

## Remote and virtual repositories

Set a remote repository's URL to a Galaxy API root (for example,
`https://galaxy.ansible.com/`) or an explicit `/api/v3` or published Galaxy v3 content
root. Metadata uses the shared source cache; archive URLs are rewritten to this
registry. DNS, redirects, upstream credentials, offline mode, metadata refresh/zap,
and `storeArtifactsLocally=false` remain governed by the common remote policy.
Metadata retains every consulted source fence and rechecks exact version read
permission before delivery. Off-origin archive URLs support CDNs through the
shared destination policy; repository credentials and custom headers stay on their
authorized origins. Repository include/exclude patterns also apply to the canonical
archive path. A cold offline miss returns 404. Warm metadata is decoded on every request, so a
new client can resolve dependencies after the upstream becomes unavailable.

Remote pagination accepts at most 100 pages of 100 versions, follows only the same
collection endpoint on the same origin, and rejects cycles or duplicate versions.
Each metadata document is bounded to 2 MiB. Invalid metadata returns 502; upstream
status failures retain the shared source policy's status mapping.

Virtual repositories merge readable concrete local, remote and federated members
in configured order. The first readable member providing a version wins, and
artifact downloads resolve through that same order. A 404 member falls through;
other upstream failures stop resolution. Both virtual and member permissions apply.
Missing configured members are skipped. Nested virtual or incompatible members
return 400 through the existing package-router contract; generic nested graph
resolution remains P10. This is an operation boundary within the supported virtual
class, not a claim of nested-virtual support.

## Integrity and limits

Before an archive is admitted, the adapter checks its SHA-256, gzip/tar structure,
`MANIFEST.json`, the `FILES.json` checksum and every listed file checksum. Remote
archive dependencies must also match the advertised version metadata. Unsafe paths,
links, duplicate entries, extra unlisted files, invalid dependency ranges and
identity mismatches are rejected before an item is published. Archives are limited
to 128 MiB compressed, 512 MiB of file content, 4,096 entries and 1 MiB per manifest;
expanded tar framing is separately bounded. Galaxy's base64 multipart upload is
bounded after decoding. Dependencies are limited to 256 names and 1,024 characters
per constraint. Roles and collection signature publication are outside this
collection adapter; existing platform signing ownership is unchanged.

## Evidence and sources

Checked on **2026-10-28** against the current published
[JFrog Ansible guide](https://docs.jfrog.com/artifactory/docs/ansible-repositories),
[package-client class matrix](https://docs.jfrog.com/artifactory/docs/repository-support-for-package-clients),
[Ansible installation guide](https://docs.ansible.com/projects/ansible/latest/collections_guide/collections_installing.html),
[release support matrix](https://docs.ansible.com/projects/ansible-core/devel/reference_appendices/release_and_maintenance.html),
and [ansible-core 2.21.5 release](https://pypi.org/project/ansible-core/2.21.5/).
JFrog's class table lists L/R/V; INV-01 additionally approves one-node F. The
repository's release baseline is Artifactory 7.161.32; this check is against the
published contract and does not claim a live Artifactory server-version probe.
Cross-peer federation remains P13.

The required [native feature](../../test/acceptance/features/ansible.feature) proves
L/F publish/install, V dependency installation, R redirect and offline installation,
corrupt archive refusal with no cached item, and invalid-credential publish/install refusal. The
[pinned client image](../../test/acceptance/clients/ansible.Dockerfile) is built in the
full and Ansible-scoped CI profiles. Package acceptance tests cover lifecycle,
permissions, member priority, pagination, refresh and upstream failure. The shared
format contract exercises copy/move, delete, restore and reindex across its stores.
