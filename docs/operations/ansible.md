# Ansible collections

ArtifactWeave serves Galaxy v3 collections from `ansible` local, remote, virtual,
and one-node federated repositories. The [client recipe](../client-recipes/ansible.md)
records the current ansible-core and Python pins; acceptance builds its own collections
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

Use local or federated repositories for publish, or a virtual repository with a
default deployment repository, where the collection lands and whose import task the
virtual repository answers, so `ansible-galaxy collection publish -s
.../api/ansible/<virtual>` completes. A virtual repository without one answers 405
"No local repository was configured as local deployment repository for the
(<virtual>) virtual repository." with `Allow: GET`, and a remote answers 405. All discovery, version metadata and archives require read permission;
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

Virtual repositories merge their members in the shared walk of decision 0008: nested
virtual members are walked, a cycle or a member listed twice is skipped, and local and
federated members come before remote ones. The first readable member providing a
version wins, and artifact downloads resolve through that same order. A member that
misses, that the caller may not read, or that fails is passed over; a failing member
is logged as a warning. Read is each member's alone, so a token with Read on the
members only, and no `ANY` target, resolves through the virtual repository. Missing
configured members and members of another type are skipped. The version list is
merged on every request: Artifactory 7.161.26 saves no retrieval cache period for an
Ansible virtual.

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

## Virtual repositories

Galaxy collection versions merge readable real members; archive URLs preserve
the selected member and stay on the virtual. The existing remote/nested behavior
and DDR publication described above use the [shared virtual rules](virtual-repositories.md). This adapter
merges per request. [Native Galaxy evidence](../../test/acceptance/features/ansible.feature)
covers remote plus nested local dependencies and publication; inferred cache
choices remain in the [ADP-04 #216 entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6893506348) [I].
That entry's older cached-as-Chef proposal predates the merged per-request
implementation documented in decision 0008; the comparison remains open.
