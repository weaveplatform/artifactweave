# Phase 9b qualification

This ledger records the five repository types added after the original format
inventory. It distinguishes documented client behavior and controlled tests
from wire details that still need an Artifactory capture. The final combined
gate, exact commit and run URL must be recorded here before closing
[#693](https://github.com/weaveplatform/artifactweave/issues/693) and the
[#681 epic](https://github.com/weaveplatform/artifactweave/issues/681).

## Format and client matrix

L, R, V and F mean local, remote, virtual and federated repositories. F below
proves native behavior on one node; cross-peer replication belongs to P13.
Class admission is also checked by `internal/server/repositories_format_class_test.go`.

| Format | Classes | Native route and client evidence | Implementation PRs |
| --- | --- | --- | --- |
| LuaRocks | L/R/V/F | `/artifactory/api/luarocks/{repo}`; `luarocks` 3.11.0 native publish/install on L and F, remote cached install on R, and mixed local/remote install and deployment on V. The client uses `--only-server` and fresh install trees. See `luarocks_local.feature`, `luarocks_remote.feature`, `luarocks_virtual.feature` and [qualification details](luarocks.md). | [#713](https://github.com/weaveplatform/artifactweave/pull/713), [#729](https://github.com/weaveplatform/artifactweave/pull/729), [#731](https://github.com/weaveplatform/artifactweave/pull/731) |
| NVIDIA NIM | R, paired Docker R | `/artifactory/api/nimmodel/{repo}` accepts documented NGC metadata, repository and model file/ZIP URLs; `nimmodel_remote.feature` pairs metadata with a native `crane pull` of the matching image through an independent Docker remote and repeats both offline. This is a controlled NGC fixture, not an observed NIM 1.3 container session. See [NIM qualification](nimmodel.md). | [#749](https://github.com/weaveplatform/artifactweave/pull/749) |
| Skills | L | `/artifactory/api/skills/{repo}`; pinned `jf agent skills` publish, list, search, install, update and delete in `skills.feature`, with a fresh client home. See [Skills qualification](skills.md). | [#732](https://github.com/weaveplatform/artifactweave/pull/732), [#734](https://github.com/weaveplatform/artifactweave/pull/734) |
| Agent Plugins | L | `/artifactory/api/agentplugins/{repo}`; pinned `jf agent plugins` publish, Cursor install/update and Claude marketplace/ZIP reads in `agent_plugins.feature` and `agent_plugins_clients.feature`, with a fresh client home. | [#735](https://github.com/weaveplatform/artifactweave/pull/735), [#736](https://github.com/weaveplatform/artifactweave/pull/736) |
| Agent Packages | L/F | `/artifactory/api/agentpackages/{repo}`; APM 0.23.0 native publish, version selection and install on L, plus native publish/install on one-node F in `agent_packages.feature`, each with isolated project and consumer directories. See [operator guide](../operations/agent-packages.md). | [#737](https://github.com/weaveplatform/artifactweave/pull/737) |

The [contract report](../research/33-phase9b-formats.md) records the target
repository classes and the documented/inferred boundary. JFrog's
[release index](https://docs.jfrog.com/releases/docs/artifactory-release-notes)
still listed self-managed **7.161.32** and SaaS **7.174.0** when checked on
2026-10-23. This checks the published version boundary, not a runtime capture.
Repository configuration, native
protocol, metadata and package admission all matter: repository CRUD or a
generic file response alone does not satisfy a matrix row.

## Security, cache and lifecycle review

| Boundary | Controlled evidence and remaining limit |
| --- | --- |
| Class and route ownership | Repository class admission rejects unsupported pairs. Native handlers are mounted only for their documented classes. LuaRocks V checks each member and its deployment target; Agent Packages F uses local protocol behavior without claiming peer convergence. |
| Caller permissions | LuaRocks manifest/rock, Skills discovery/search/download, Agent Plugins marketplace/ZIP and Agent Packages versions/search/download filter by the current item read grant. Native publish and overwrite use write/delete grants. NIM model and paired Docker grants are independent. Relevant status/principal suites live in `internal/server` and the format packages. |
| Untrusted metadata and archives | LuaRocks manifest tables are parsed without execution; rockspec and rock ZIP identities agree with their paths. Skills, Agent Plugins and Agent Packages validate bounded ZIP content and path identity before indexing. NIM metadata is bounded and validated before cache publication; truncated or malformed replies cannot become successful entries. |
| Remote source isolation | LuaRocks and NIM use the P9 source policy for origin-bound credentials, target and redirect restrictions, refresh and offline cache behavior. Controlled upstream tests cover denial, malformed responses, cache admission, failed refresh and unauthorized redirects. The NIM Docker remote has a separate upstream credential and cache. |
| Item lifecycle | LuaRocks manifests, Skills packages, Agent Plugins marketplace entries and Agent Packages indexes follow deploy/delete/restore/copy/move or format-specific reindex tests. NIM cached metadata uses the shared remote cache lifecycle. Exact target-specific status, property and cache sidecar parity is still [I] on #216. |
| Restart | `TestP9bPackageIndexesSurviveRestart` closes and reopens a file-backed SQLite store, then reads each new type's indexed version and metadata. `TestRemoteCachePersistence` covers the shared remote cache across reopen; format reindex tests rebuild from saved content. This proves storage continuity, while P13 owns peer recovery. |

The format-specific regression suites include `internal/format/luarocks`,
`internal/server/luarocks`, `internal/server/nimmodel`,
`internal/format/skills`, `internal/server/skills`,
`internal/format/agentplugins`, `internal/server/agentplugins_test.go`,
`internal/format/agentpackages` and the root server class/route tests.
Representative fault and lifecycle tests are
`TestRemoteRockRechecksManifestAndFileAuthorityAtDelivery`,
`TestVirtualDeploymentRequiresValidLocalTargetAndBothGrants`,
`TestNIMDenialAndInvalidUpstreamDoNotPublish`,
`TestNIMMetadataAndPairedDockerRegistryKeepSeparateAuthority`,
`TestSkillsReadsRespectVersionPermissions`,
`TestAgentPluginMarketplacesTrackTreeAndPermissions`,
`TestAgentPackagesAPMProtocolAndLifecycle`, and
`TestManifestFollowsDeleteRestoreCopyAndReindex`.
The full CI gate must retain the 95% total and 90% per-package coverage floors
and pass race/shuffle tests on all three operating systems, native acceptance,
lint, security, OCI, Terraform, spec parity and cross-compilation.

## Final combined-tree gate

Pending merge of #749 and this qualification PR. Record the merged `main` SHA,
full workflow-dispatch URL, required job conclusions, merged coverage and native
client outcomes here. A branch run or earlier PR gate does not qualify a later
merged head. Any native skip caused by a missing pinned client fails sign-off.

The acceptance clients use controlled upstreams and isolated homes. Their
successful runs prove this server's documented behavior under those fixtures;
they do not assert byte-for-byte parity with a live Artifactory 7.x instance.
NIM's NGC request/response capture, `.jfrog` sidecars and remaining
`Nimmodel.*` properties, along with the other inferred wire details, remain
tracked in [#216](https://github.com/weaveplatform/artifactweave/issues/216)
for P19.

## Receiving phases

- **P10:** wider native virtual composition beyond LuaRocks' P9b class.
- **P13:** actual peer convergence for LuaRocks and Agent Packages federated repositories.
- **P23:** signed Evidence and release bundle behavior, including native agent client verification.
- **P26:** actual Xray/semantic scans and verdicts for Skills and other supported content.
- **P19/#216:** target-instance response, status, property and cache-layout comparison.
- **#673 and #714:** dependency-aware selective CI and remaining server test
  packages. They share the P9b milestone but are a separate CI track; keep the
  milestone open until they complete, even after the five-format epic closes.
  Full release and phase qualification remains required.
