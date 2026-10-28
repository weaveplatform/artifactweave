# Conda channels

ArtifactWeave serves native Conda channels from `conda` local, remote and virtual repositories, and from a one-node federated repository. The tested client is micromamba 2.10.0. [JFrog's current Conda guide](https://docs.jfrog.com/artifactory/docs/conda-repositories), checked 2026-10-28, documents the channel URL and package layout; its package-client class table lists local, remote and virtual. Cross-peer federation is owned by P13.

## Connect a client

Point the Conda client at `https://<host>/artifactory/api/conda/<repository>`. For a subchannel, append its prefix: `/artifactory/api/conda/<repository>/<subchannel>`. A `.condarc` with a token-backed channel is:

```yaml
channels:
  - https://<url-encoded-user>:<token>@<host>/artifactory/api/conda/<repository>
default_channels:
  - https://<url-encoded-user>:<token>@<host>/artifactory/api/conda/<repository>
```

Deploy `.conda` or `.tar.bz2` archives with `PUT /artifactory/<repository>/<subdir>/<package-file>`, where `<subdir>` is `noarch`, `linux-64`, `osx-arm64`, or another Conda platform. Prefix a subchannel path before the subdirectory. The package filename and `info/index.json` must agree on name, version, build and platform. Invalid archives are rejected. The native route exposes `repodata.json`, `repodata.json.zst`, `current_repodata.json`, `current_repodata.json.zst`, `notices.json`, and indexed package downloads. Metadata is filtered to packages the caller can read.

Hosted metadata is rebuilt after publish, delete and restore. `POST /artifactory/api/conda/<repository>/reindex` waits for a full rebuild; `?async=true` queues it and returns immediately. Remote metadata and archives use the source policy's cache, refresh, offline and redirect rules. A virtual channel combines allowed members in configured order; the first member providing a package filename wins.

The [acceptance feature](../../test/acceptance/features/conda.feature) installs both archive types through the pinned client, including L/F/R/V, remote redirect and offline cache, and checks delete, restore, reindex and corrupt remote bytes. The controlled legacy fixture and current conda-forge `.conda` fixture are recorded in [testdata](../../internal/format/conda/testdata/README.md).

## CEP-16 sharded repodata

[JFrog's Conda guide](https://docs.jfrog.com/artifactory/docs/conda-repositories#enable-conda-metadata-sharding), checked 2026-10-28, documents opt-in sharding for self-managed Artifactory **7.161.15+** and L/R/V. ArtifactWeave also serves it for its one-node F contract. Enable the server setting, then reindex existing hosted repositories:

```yaml
conda:
  shards_enabled: true
```

The equivalent environment variable is `ARTIFACTWEAVE_CONDA_SHARDS_ENABLED=true`. It defaults to false: native shard endpoints return 404 while classic endpoints remain available. `POST /artifactory/api/conda/<repository>/reindex` regenerates classic and shard metadata together; new publishes, deletes and restores use the same worker transaction.

The [CEP-16 specification](https://conda.org/learn/ceps/cep-0016/) (accepted; last updated 2024-07-22) defines `<subdir>/repodata_shards.msgpack.zst` and content-addressed `<subdir>/<sha256>.msgpack.zst` documents. Hashes in MessagePack are binary. ArtifactWeave hashes the compressed shard bytes, validates archive identities and digests, and generates deterministic shards per package name. The index includes `info.subdir` for current micromamba. Package URLs use the configured public base; shard URLs stay relative to the index. Configure `base_url` correctly when using a reverse proxy.

For the tested micromamba **2.10.0** client, enable `MAMBA_USE_SHARDED_REPODATA=true`. Use the existing authenticated channel URL and register the same account in its credential store so package downloads resolved from `base_url` retain authentication:

```sh
micromamba auth login https://<host>/artifactory/api/conda/<repository> \
  --username <user> --password-stdin
MAMBA_USE_SHARDED_REPODATA=true micromamba create --yes --prefix ./environment \
  --override-channels --channel 'https://<url-encoded-user>:<token>@<host>/artifactory/api/conda/<repository>' <package>
```

For current conda **26.9.4**, the documented settings are `repodata_use_shards: true` and `solver: libmamba`; conda-libmamba-solver **26.6.0+** delegates shard fetching to conda and removed the older `plugins.use_sharded_repodata` setting. These versions were checked against [conda's stable documentation](https://docs.conda.io/projects/conda/en/stable/dev-guide/deep-dives/sharded.html), the [solver changelog](https://github.com/conda/conda-libmamba-solver/blob/main/CHANGELOG.md), and the [micromamba 2.10.0 release](https://github.com/mamba-org/mamba/releases/tag/2.10.0) on 2026-10-28. Native executable evidence uses the digest-pinned micromamba image in [conda.Dockerfile](../../test/acceptance/clients/conda.Dockerfile).

Remote channels prefer validated upstream shards when enabled and use classic metadata only when the upstream shard **index** returns 404. A missing, malformed or mismatched package shard fails the request; it never triggers classic fallback. Indexes and shards retain independent source-cache identities, source revisions, credential revisions, refresh and offline semantics. URL bases and redirects must remain in the configured remote origin/path; external package bases are rejected. A cold remote view validates its member shards before disclosure so file-level permissions can remove individual builds. Parsing limits compressed and expanded documents to 64 MiB, nesting to 32 levels, records/collection sizes to 200,000 and MessagePack nodes to 1,000,000; the combined snapshot also has a 64 MiB budget counting the larger of compressed bytes and retained record JSON for each shard.

Virtual channels select records in member order, apply member and virtual permissions, and hash the resulting per-name shards. They never reuse an upstream hash for changed content. Metadata is private; shard responses carry `immutable` with mandatory revalidation so shared caches cannot bypass current permissions. A hash that no longer describes the current readable records returns 404: clients must refresh their index after package removal or replacement. There is no historical shard retention window.

CEP-16 documents are served through the native `/artifactory/api/conda/` endpoint. Generic `/artifactory/<repository>/` index and shard downloads, including checksum sidecars and archive-entry variants, return 404 because persisted documents contain the unfiltered repository snapshot; generic folder archives omit them too. Copy and move operations report and skip these documents, including folder descendants, and checksum deployment cannot reuse their stored bytes. Ordinary package transfers remain available. Classic raw metadata retains its existing generic item boundary: its own path permissions govern access. Use the native endpoint when metadata must reflect individual package permissions.

The required `@conda` feature includes `@conda_shards` proof for L/F/R/V and remote offline reuse. It explicitly enables sharding, clears both package and shard caches, requires successful index **and shard** requests, rejects any classic repodata request, and checks installed fixture bytes. HTTP acceptance additionally covers delete/restore/reindex, read filtering, hash failures, redirect confinement, refresh and permission/source changes during a shard fetch.
