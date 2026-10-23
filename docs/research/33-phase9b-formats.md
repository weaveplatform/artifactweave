# P9b: five newly identified package formats

Checked **2026-10-22** against the latest listed self-managed Artifactory **7.161.32**
([released 2026-10-06][release dates]); the [release index] lists SaaS **7.174.0**. This is a
documentation contract, **not** a capture from 7.161.32. The format guides are
rolling pages, and JFrog's public [SaaS release notes] document some features
after 7.161. JFrog's self-managed release-detail page is not public; availability
and response parity on 7.161.32 remain **[I]** until [#216] captures them.

Labels: **[D]** directly documented by JFrog or the named client owner;
**[S]** JFrog-owned client source; **[I]** an implementation hypothesis or an
unobserved target-version behavior. A documented native URL prefix is not by
itself proof of a repository-management `packageType` JSON enum value.

## Repository admission and package keys

| Format | `packageType` for repository JSON | Local | Remote | Virtual | Federated | Native base | Evidence |
| --- | --- | --- | --- | --- | --- | --- | --- |
| LuaRocks | `luarocks` **[I]** | Yes | Yes | Yes | Yes | `/artifactory/api/luarocks/{repo}` | [LuaRocks guide], [class matrix] |
| NVIDIA NIM | `nimmodel` **[I]** | No | Yes | No | No **[I]** | `/artifactory/api/nimmodel/{repo}` | [NIM guide], [Smart Remote prefixes] |
| Skills | `skills` **[I]** | Yes | No | No | No **[I]** | `/artifactory/api/skills/{repo}` | [Skills guide] |
| Agent Plugins | `agentplugins` **[D]** | Yes | No | No | No **[I]** | `/artifactory/api/agentplugins/{repo}` | [Plugins guide], [JFrog CLI plugins] |
| Agent Packages | `agentpackages` **[S]** | Yes | No | No | Yes | `/artifactory/api/agentpackages/{repo}` | [APM guide], [JFrog CLI template] |

The guides are authoritative for the Yes columns. A No for local/remote/virtual
is stated in each format guide or JFrog's [class matrix]. Federated is absent
from that generic matrix: the LuaRocks guide explicitly supports it, and the
APM guide explicitly supports APM protocol on local **and federated** repositories.
The NIM/Skills/Plugins federated prohibitions are **[I]** because their guides
only offer remote or local creation rather than an explicit federated rejection.
[#683] should reject those pairs pending target capture. Smart Remote is a
**remote configuration**, not a fifth repository class: LuaRocks supports it;
Agent Packages explicitly forbids it and ordinary replication. [P13] owns
actual federated peer convergence after P9b admits LuaRocks and Agent Packages
with local-style protocol behavior. [Repository API] uses `PUT
/artifactory/api/repositories/{repoKey}` with `rclass` and `packageType`.
Its published enums and the CLI repository template lag these new formats, so
four JSON keys above are inferred from native prefixes/display names and must
be confirmed on 7.161.32 before claiming vendor-exact configuration parity.

## LuaRocks — #684 local/federated, #685 remote, #686 virtual

**Implementation note (#684).** The local/federated handler admits only
root-level rockspecs and ZIP rocks whose embedded rockspec agrees with the
filename. It indexes the full `name-version-revision` identity, queues one
repository manifest on content changes, and publishes plain `manifest` plus
`manifest-5.1` through `manifest-5.5`. A read waits for queued work. Native
upload implements the upstream client sequence under the Artifactory prefix
and keeps `.asc` signatures beside their owner. The upload key in the URL is
redacted from access logs and error paths; Basic authentication comes from the
client's configured server URL. The exact Artifactory response fields and
`luarocks.*` property spellings remain **[I]** on [#216] until target capture.
Remote cache and virtual aggregation remain owned by #685 and #686, and P13
owns cross-peer convergence of federated content.

**[D]** A rocks server is a flat HTTP root. The native client reads `manifest`
and `manifest-<Lua version>` and fetches root-level
`{name}-{version}-{revision}.rockspec`, `.src.rock`, and
`.{architecture}.rock`. Artifactory indexes name/version/revision from filenames
and rockspec metadata; local add, remove, copy and move asynchronously rebuild
the manifest, and Recalculate Index does a full rebuild. Remote defaults to
`https://luarocks.org/repositories/rocks/`; only fetched files appear in the
remote cache browser. Virtual combines local/remote and can direct upload to
a configured local deployment target. See the [LuaRocks guide] and the
[LuaRocks manifest specification].

**[D]** Configure `rocks_servers` or `--only-server` with the native base.
`luarocks install NAME [VERSION]` resolves via the manifest. Configure
`upload_config.lua` and `luarocks upload name-version-revision.rockspec`; the
rockspec goes first and the CLI normally packs the matching source rock. The
CLI does not use `Authorization: Bearer` or its `--user`/`--password` flags for
upload; JFrog instructs Basic credentials in the server URL. For install,
`.netrc` plus curl, or URL credentials, are documented. The upstream
[LuaRocks.org upload API] defines `GET /api/tool_version`,
`POST /api/1/{key}/upload` (multipart `rockspec_file`) and
`POST /api/1/{key}/upload_rock/{version_id}` (multipart `rock_file`), with
JSON `module`, `version`, and `rock` results. **[I]** These upstream paths and
response bodies may differ behind JFrog's `/api/luarocks/{repo}` prefix;
capture real native upload traffic before freezing them as Artifactory wire
parity. The public rocks server also exposes `.json`/`.zip` manifest variants;
JFrog guide only promises `manifest` and `manifest-<version>`, so these variants
are likewise **[I]** for 7.161.32.

## NVIDIA NIM — #687

**[D]** Create only a NIM **remote** with upstream
`https://api.ngc.nvidia.com`, blank username and an NVIDIA Personal Key as
password. Pair it with a distinct Docker remote at `https://nvcr.io/`, using
`$oauthtoken` as username and the same key as password. The only supported
client is Docker; NGC CLI/SDK are not supported. The NIM container receives
`NGC_API_ENDPOINT` and `NGC_AUTH_ENDPOINT` pointing to
`{host}/artifactory/api/nimmodel/{repo}`, `NGC_API_KEY`, and
`NGC_API_SCHEME`. Docker pulls the matching image through the Docker remote.
The [NIM guide] describes `.jfrog/models/{org}/{name}/{version}/model_urls_mapping.json`
and `models/{org}/{name}/{version}/.jfrog_nim_model_info.json` plus model files
in the NIM remote cache. It lists `Nimmodel.*` package properties and says NIM
1.3+ only, with no anonymous access, Xray, Curation or direct cloud-storage
download. **[I]** The guide does not publish the exact NGC methods, query
parameters, response schemas or status mapping for NIM model fetches; do not
invent a Docker-only success criterion. Capture a controlled NIM run and
denial/fault responses in [#216].

## Skills — #688 storage/index, #689 discovery/install

**[D]** Skills is an open beta, **local-only** ClawHub v1-compatible registry.
`POST {base}/api/v1/skills` takes multipart `payload` JSON (at least lowercase
hyphenated `slug` and `version`) and one or more `files[]` parts, then stores
`{slug}/{version}/{slug}-{version}.zip`. `SKILL.md` YAML frontmatter populates
`skill.*` properties; a missing manifest can still store a bundle with
incomplete metadata. The parser caps `SKILL.md` at 5 MB. The guide documents:

| Operation | Path under `{base}` |
| --- | --- |
| Discovery | `GET /.well-known/clawhub.json` |
| List/detail/versions | `GET /api/v1/skills`, `/api/v1/skills/{slug}`, `/api/v1/skills/{slug}/versions`, `/api/v1/skills/{slug}/versions/{version}` |
| Search | `GET /api/v1/search?q=...&limit=...&offset=...` |
| Download/fingerprint | `GET /api/v1/download?slug=...&version=...`, `GET /api/v1/resolve?slug=...&hash=...` |
| Single file | `GET /api/v1/skills/{slug}/file?version=...&path=...` |
| Scan status | `GET /xrayStatus?path={URL-encoded in-repo path}` |

The [Skills guide] provides example list/detail/version JSON; search `limit`
is 1–100, fingerprint is 64 lowercase hex, and scan statuses include
`NOT_IN_ENTITLEMENT`, `XRAY_DISABLED_FOR_REPO`, `SCAN_IN_PROGRESS`,
`APPROVED`, `BLOCKED`. Only discovery is anonymously accessible; every other
Skills endpoint requires credentials. `jf agent skills` supports publish,
install, search, update, list and delete (JFrog CLI 2.98.0+; old namespace
`jf skills`). [P23] owns evidence signing; [P26] owns semantic/Xray scanning.
P9b should expose the documented status read and avoid claiming to perform a
scan unless the later security integration is installed.

## Agent Plugins — #690 index, #691 native clients

**[D]** Local-only beta. `jf agent plugins publish DIR --repo REPO` uploads
`{name}/{version}/{name}-{version}.zip`; Artifactory uses path identity even
when `plugin.json` disagrees and overwrites require permission. Original
harness manifests live at `.claude-plugin/plugin.json`,
`.cursor-plugin/plugin.json` or `.codex-plugin/plugin.json`. Artifactory sets
`agentplugins.*` properties and rebuilds root `marketplace.json` plus the
matching `claude-marketplace.json`, `cursor-marketplace.json`, or
`codex-marketplace.json`. Indexes list only each plugin's latest version;
there is no all-version index. Claude consumes
`GET {base}/claude-marketplace.json` as a native marketplace (its URL-credential
flow cannot use email-style usernames); `jf agent plugins install`, `list`
and `update` serve Claude/Cursor/Codex. [P23] owns evidence signing rather
than core package indexing. See the [Plugins guide] and [JFrog CLI plugins].

**Version boundary.** The rolling guide also describes root `plugin.json`
under Agent Plugins Standard 1.0.0, `vscode-marketplace.json`, and VS Code
client installation. JFrog's [SaaS release notes] first announce those in
**7.168.0 SaaS**, newer than the listed **7.161.32 self-managed** target.
Do not describe that extension as observed 7.161.32 behavior. [#690]/[#691]
may implement it as forward compatibility with explicit tests and an **[I]**
target-version marker; [#216] must capture availability and exact JSON on
7.161.32. `marketplace.json` structure is explicitly beta and may change.

## Agent Packages — #692

**[D]** Agent Packages is a beta APM 0.23.0+ registry. Local **and federated**
repositories accept `.zip` only at
`{owner}/{package}/{package}-{version}.zip`, without extra subfolders. APM
packages carry `.apm/` primitives and `apm.yml` or `apm.lock.yaml`; Artifactory
prefers `apm.yml` for indexed metadata. The guide explicitly rejects invalid
ZIP paths on local/federated, and forbids remote, virtual, Smart Remote and
ordinary replication. `apm experimental enable registries`, then
`apm config set registry.NAME.url {base}` and `.token TOKEN`, configures a
bearer-token registry. `apm publish --package owner/name` and
`apm install owner/name#version` (exact or SemVer expression) are native exits.
`jf setup apm` and `jf agent apm` are optional wrappers; JFrog CLI's
documentation says those commands are **not** in v2.122.0, so pin a later
verified version before accepting them. See [APM guide] and [JFrog CLI APM].

| Operation | Path under `{base}` |
| --- | --- |
| Versions | `GET /v1/packages/{owner}/{name}/versions` |
| Download | `GET /v1/packages/{owner}/{name}/versions/{version}/download` |
| Publish | `PUT /v1/packages/{owner}/{name}/versions/{version}` with `application/zip` |
| Search | `GET /v1/search?q=...&type=...&tag=...&limit=...&offset=...` |

The guide calls these endpoints "on local" but its limitations explicitly
say APM v1 also works on federated. Treat that as federated protocol
admission; peer convergence is [P13]. The guide does not publish example
version/search JSON, precise conflict/denial bodies, or the behavior of a
SemVer range sent to the server, so those remain **[I]**.

## Authorization, errors and qualification boundary

Admin or Project Admin permission is documented for creating each type.
Skills has the documented anonymous discovery exception; NIM explicitly
forbids anonymous access. Other operations use the credentials described
above, but the guides do **not** specify exact unauthorized versus forbidden
status, response envelope, path-level permission decisions, upload conflict
status, malformed-archive status or stale-index race behavior. These are
**[I]**, not vendor facts. Each P9b adapter should apply artifactweave's
existing Read/Deploy/Delete/Manage checks and the same project, permission,
cache, integrity and mutation fences as P6–P9. [#693] qualifies native
success/denial/fault paths, deletion/restore/reindex and the full type/class
matrix; [#216] compares vendor statuses, headers and bodies later. P10's
general virtual work must preserve LuaRocks virtual behavior from [#686].

## P19 capture ledger for #216

| ID | Target-version capture, with exact status/headers/body and storage side effect |
| --- | --- |
| P9B-1 | `PUT /api/repositories/{key}` for all five `packageType` candidates across L/R/V/F; compare class rejection, default layout, descriptor export, and Smart Remote/replication prohibitions. |
| P9B-2 | Native LuaRocks `upload` with rockspec then source rock, `manifest`/`manifest-5.x` reads, missing/denied/overwrite cases, virtual local+remote selection, warm/expired/offline cache and reindex. Capture all upload method/path/body/result shapes. |
| P9B-3 | NIM 1.3+ Docker run against paired NIM/Docker remotes: capture NGC methods, model mappings, authentication challenge, file bytes, metadata properties, failure/negative-cache responses and permission revocation. |
| P9B-4 | Skills multipart publish, discovery, list/detail/versions/search, fingerprint resolve, download/file, delete/reindex and scan-status with no Xray entitlement; compare anonymous and revoked-token responses. |
| P9B-5 | Agent Plugins harness ZIP publish/index/Claude and CLI install/update, multiple versions/layout changes, malformed manifest, narrow Read and revoke. Probe Standard/VS Code separately to establish whether 7.161.32 has post-7.168 SaaS behavior. |
| P9B-6 | APM ZIP publish/versions/download/search/install through local and federated, exact/range versions, bad path/ZIP, overwrite, anonymous/denied access, restore/reindex and replication prohibition. |

[LuaRocks guide]: https://docs.jfrog.com/artifactory/docs/luarocks-repositories
[NIM guide]: https://docs.jfrog.com/artifactory/docs/nvidia-nim-repositories
[Skills guide]: https://docs.jfrog.com/artifactory/docs/skills-repositories
[Plugins guide]: https://docs.jfrog.com/artifactory/docs/agent-plugins-repositories
[APM guide]: https://docs.jfrog.com/artifactory/docs/agent-packages-repositories
[class matrix]: https://docs.jfrog.com/artifactory/docs/repository-support-for-package-clients
[Repository API]: https://docs.jfrog.com/artifactory/reference/createRepository
[Smart Remote prefixes]: https://docs.jfrog.com/artifactory/docs/smart-remote-repositories
[SaaS release notes]: https://docs.jfrog.com/releases/docs/artifactory-saas-releases
[release index]: https://docs.jfrog.com/releases/docs/artifactory-release-notes
[release dates]: https://docs.jfrog.com/releases/docs/artifactory-end-of-life
[JFrog CLI plugins]: https://docs.jfrog.com/artifactory/docs/jf-agent-plugins
[JFrog CLI APM]: https://docs.jfrog.com/artifactory/docs/jf-agent-apm
[JFrog CLI template]: https://github.com/jfrog/jfrog-cli-artifactory/blob/master/artifactory/commands/repository/template.go
[LuaRocks manifest specification]: https://luarocks.org/docs
[LuaRocks.org upload API]: https://luarocks.org/docs/api
[#216]: https://github.com/weaveplatform/artifactweave/issues/216
[#683]: https://github.com/weaveplatform/artifactweave/issues/683
[#686]: https://github.com/weaveplatform/artifactweave/issues/686
[#690]: https://github.com/weaveplatform/artifactweave/issues/690
[#691]: https://github.com/weaveplatform/artifactweave/issues/691
[#693]: https://github.com/weaveplatform/artifactweave/issues/693
[P13]: ../implementation/plan.md#p13-peer-trust-and-federated-repositories
[P23]: ../implementation/plan.md#p23-evidence-and-provenance
[P26]: ../implementation/plan.md#p26-security-scanning-and-intelligence
