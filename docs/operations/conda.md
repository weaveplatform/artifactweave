# Conda channels

ArtifactWeave serves native Conda channels from `conda` local, remote and virtual repositories, and from a one-node federated repository. The tested client is micromamba 2.8.1. [JFrog's current Conda guide](https://docs.jfrog.com/artifactory/docs/conda-repositories), checked 2026-10-25, documents the channel URL and package layout; its package-client class table lists local, remote and virtual. Cross-peer federation is owned by P13.

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

[JFrog documents optional CEP-16 shards](https://docs.jfrog.com/artifactory/docs/conda-repositories#enable-conda-metadata-sharding) for local, remote and virtual repositories in self-managed Artifactory 7.161.15 and later. Sharding is disabled by default. This implementation currently serves the standard JSON and Zstandard metadata, and does not yet expose `repodata_shards.msgpack.zst` or content-addressed MsgPack shards. The direct P9c Conda sharding follow-up owns those opt-in endpoints, source cache behavior and shard-enabled client proof. The classic endpoints remain available regardless of that setting.
