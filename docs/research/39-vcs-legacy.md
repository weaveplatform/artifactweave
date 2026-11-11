# VCS provider mappings for #844

Checked 2026-11-03. This report separates primary endpoint documentation from
controlled fixture evidence. A fixture is not an observed Artifactory capture.

## Bitbucket Server and Data Center (`STASH`)

JFrog's [VCS guide](https://docs.jfrog.com/artifactory/docs/vcs-repositories)
distinguishes current Bitbucket Server from Stash before 5.1.0. Its clone URL
example contains `/scm/{project}/{repository}.git`; configure the server base
URL, including any deployment context, without the project/repository suffix.

The versioned [Bitbucket Server 5.1.0 REST contract](https://docs.atlassian.com/bitbucket-server/rest/5.1.0/bitbucket-rest.html)
documents the archive endpoint and its parameters. The same contract remains
in [Bitbucket 9.0](https://developer.atlassian.com/server/bitbucket/rest/v900/api-group-repository/).

| Operation | Request relative to the configured server context |
| --- | --- |
| Ref discovery | `GET scm/{project}/{repository}.git/info/refs?service=git-upload-pack` |
| Archive | `GET rest/api/1.0/projects/{project}/repos/{repository}/archive?at={commit}&format={zip\|tar.gz}` |
| Selected file | Read the validated archive for that commit and return one repository-relative file as ZIP |

The REST endpoint accepts commit IDs or refs; artifactweave resolves a tag or
branch to a full object ID first. `format` chooses ZIP or gzip-compressed TAR.
The optional `prefix` parameter is deliberately omitted: rootless provider
archives are valid and their bytes must be preserved. Public repositories
permit anonymous archive reads; private repositories require `REPO_READ`.
These facts are present in the versioned 5.1.0 contract.

Both upstream URLs stay on the configured origin and deployment context.
Ref/archive redirects must preserve the complete request URL, including the
commit query. Credentials use the existing remote source credential store and
transport policy. The cache identity already includes the external URL and
settings revision, preventing a changed provider endpoint from selecting old
bytes. Existing archive CRC, entry, size, traversal and selected-file limits
apply before cache admission or extraction.

`internal/artifacts/ecosystem/vcs/http/paths_test.go` verifies the context-aware
mappings; `test/server/artifacts/vcs/vcs_legacy_test.go` verifies immutable ref resolution,
redirect restrictions and authorization-before-fetch. The pinned curl
scenarios in `test/acceptance/features/vcs_legacy.feature` cover public/private
upstreams, reference listing, unchanged archives, selected files and offline
cache deletion.

## Stash before 5.1 (`OLDSTASH`)

The first-party archive-plugin sources establish a different URL:
`GET plugins/servlet/archive/projects/{project}/repos/{repository}?at={commit}&format={zip|tar.gz}`.
Ref discovery still uses `scm/{project}/{repository}.git/info/refs`.

Verified published artifacts:

| Source release | SHA-256 | Relevant source |
| --- | --- | --- |
| [Stash archive 1.3.2](https://packages.atlassian.com/maven-public/com/atlassian/stash/plugin/stash-archive/1.3.2/stash-archive-1.3.2-sources.jar) | `25ca9917ff9ba2c65f0009405d62a92993a168d00774149a3f3d27fad3def434` | `ArchiveServlet.java`, `ArchiveFormat.java`, `static/archive.js`, `atlassian-plugin.xml` |
| [Bitbucket archive 2.1.5](https://packages.atlassian.com/maven-public/com/atlassian/bitbucket/server/bitbucket-archive/2.1.5/bitbucket-archive-2.1.5-sources.jar) | `55a2c567281d1df9720d8ecde39c3d48db0db6e80444450b55a70eeb6ef678ed` | `internal/archive/web/ArchiveServlet.java`, `internal/archive/rest/ArchiveResource.java`, `atlassian-plugin.xml` |

Version 1.3.2 builds the servlet URL in its UI and reads `at`, `format` and
optional `filename` in the servlet. Version 2.1.5 retains that servlet for
compatibility and internally forwards to its `/rest/archive/latest/.../archive`
resource; this is a servlet dispatch, not an HTTP redirect. The common servlet
URL therefore works across both verified plugin generations. The
[Atlassian Marketplace release history](https://marketplace.atlassian.com/apps/1211094/archive-plugin-for-bitbucket-server/version-history)
identifies 2.1.5 as compatible with Bitbucket Server 4.0.0 through 5.0.10.

The archive plugin must be installed for this provider. Upgrading the server
to 5.1 or later removes the plugin in favor of built-in archive support, per
[Atlassian's implementation announcement](https://jira.atlassian.com/browse/BSERV-2732).
After that upgrade, change `vcsGitProvider` from `OLDSTASH` to `STASH` while
retaining the server/context URL. This migration does not retire support for
an existing pre-5.1 installation with the verified plugin.

## Artifactory peer (`ARTIFACTORY`)

Configure the documented VCS API repository URL, for example
`https://peer.example/artifactory/api/vcs/source-vcs`. The current
[smart remote guide](https://docs.jfrog.com/artifactory/docs/smart-remote-repositories)
requires the `api/vcs` prefix. A plain `/artifactory/source-vcs` URL from the
older general VCS example is rejected with explicit migration guidance;
artifactweave does not infer a peer repository from an arbitrary path.

| Operation | Peer request under `/artifactory/api/vcs` |
| --- | --- |
| Tags | `GET tags/source-vcs/{owner}/{repository}` |
| Branches | `GET branches/source-vcs/{owner}/{repository}` |
| Archive | `GET downloadCommit/source-vcs/{owner}/{repository}/{commit}?ext={zip|tar.gz}` |
| Selected file | `GET downloadTagFile` or `downloadBranchFile` with `{ref}!{path}?ext=zip` |

Authored [response-shape notes](evidence/vcs-legacy/response-shapes.md) retain
[provenance](evidence/vcs-legacy/provenance.json) for the publisher OpenAPI schema
version `1.0.0`, fetched 2026-10-19: source URLs, HTTP status and SHA-256. Hashes
match the original local `.specoracle/manifest.json`. Proprietary publisher
definitions remain solely in that ignored local cache and are not committed. The tag and branch contracts define arrays of
objects with `name`, `commitId` and `isBranch`. Both are read through the
shared metadata cache, validated, combined under the existing reference count
limit, and filtered by current read permissions. Duplicate names, wrong ref
kinds, missing fields, invalid object IDs and malformed JSON are rejected.

The peer's `refs` endpoint only specifies a JSON string, without its inner
format. The adapter obtains refs from the documented tag/branch objects
instead; it does not invent a parser for that string. Outward artifactweave
`refs` formatting and slash-bearing ref parity remain comparison items for
[#216](https://github.com/weaveplatform/artifactweave/issues/216).

Selected-file requests do not interpret an unknown peer archive wrapper. They
use the documented [tag file](https://docs.jfrog.com/artifactory/reference/downloadFileWithinVcsTag)
and [branch file](https://docs.jfrog.com/artifactory/reference/downloadFileWithinVcsBranch)
APIs, validate one exact requested regular ZIP entry (plus its ancestor directory
entries), and return the peer's bytes unchanged.
These endpoints name mutable refs, so their cache entries use mutable-source
freshness and a ref/file identity, not a previously resolved commit. Metadata zap
leaves mutable binaries intact; expiry or explicit cache deletion refreshes them.
Peers
returning an unexpected ZIP layout are rejected rather than having a prefix guessed.

Stash archives have no implicit wrapper directory. Their file selection is exact:
`sub/README.md` is never returned for `README.md`. The standard
[Git archive](https://git-scm.com/docs/git-archive) commit marker is a global PAX
`comment` for TAR or a ZIP comment. Archive admission allows one leading global
PAX header containing only that valid object ID; recognizable commit metadata
must match the requested immutable commit. Other global records, conflicting
or late headers, unsafe paths and corrupted payloads are rejected. The cache
variant changes so older entries cannot bypass this identity validation.

## Native verification and remaining comparison scope

`vcs_legacy.feature` runs 24 scenarios across the three providers with the
current pinned Maven image's curl 8.18.0 (`8.18.0-1ubuntu2.8`). Fixtures are unmodified outputs of `git archive`, with a reproducible generator
and version/commit/hash manifest under `internal/server/vcs/testdata`. Each provider
has anonymous and private upstream cases, tags/branches/refs, byte-identical
archive downloads, selected-file ZIPs, failed client authentication before
upstream access, changed refs after cache zap, authorized offline reads and
cache deletion. Peer files are refreshed after explicit cache deletion and remain
available offline. Further cases reject malformed refs/archives/files, wrong Git
commit metadata and unsafe redirects and preserve a sanitized upstream 503. Go server tests also cover
invalid ref/file paths, denied identities, exact origin/path/query scope,
immutable commit selection and aggregate metadata limits.

These are controlled native fixtures shaped by primary endpoint contracts,
not live Artifactory or Bitbucket captures. No configured licensed Artifactory
target, credential environment entry, or retained target container was available
in this workspace. The original plugin repositories now return 404; official
Maven source artifacts provided the missing contract. Exact current Artifactory
outbound request captures remain supporting #216 work. No VCS-specific reindex
or cache-restore endpoint is documented; existing cache zap/delete behavior is
preserved rather than introducing new lifecycle APIs.
