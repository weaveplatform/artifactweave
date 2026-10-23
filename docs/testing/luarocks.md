# LuaRocks local and federated qualification

Issue [#684](https://github.com/weaveplatform/artifactweave/issues/684)
implements a flat rocks server under
`/artifactory/api/luarocks/{repo}`. The native client reads the generated
`manifest-<Lua version>` and downloads the rock named there. `GET/HEAD` use
the item read grant; native upload requires the item write grant and an
overwrite additionally needs delete. The upload key segment is redacted from
server access logs and error bodies. The server never places Basic credentials
from the upload URL in its response.

The format validates a root-level rockspec's `package` and `version` against
its filename and scans rocks as bounded ZIP archives, rejecting unsafe entries
and an embedded rockspec that does not match. Its work queue derives the
manifest after deploy, overwrite, delete, copy, move and restore. Full reindex
reconciles package records and queues the same derived work. Signatures sent
as `rockspec_sig` or `rock_sig` are retained as `.asc` sidecars, and the item
service moves or deletes them with their owner.

The real `luarocks` 3.11.0 client in the pinned Alpine 3.20 image runs the
[`@luarocks` acceptance scenario](../../test/acceptance/features/luarocks_local.feature):
native rockspec/source upload followed by pinned and latest installs. The
local/federated API and lifecycle tests also cover denial, invalid layout,
malformed rockspec, invalid rock archive, restore, copy, and full reindex.
Remote and virtual behavior belongs to #685 and #686. Cross-peer federation
is tracked separately.

The JFrog guide documents the base URL, flat layout and automatic manifest
recalculation. The upstream client source documents its upload request
sequence and manifest structure. We have not captured Artifactory 7.161.32's
native upload response or property keys; these remain [I] on #216.

- [JFrog LuaRocks guide](https://docs.jfrog.com/artifactory/docs/luarocks-repositories)
- [LuaRocks manifest writer](https://github.com/luarocks/luarocks/blob/main/src/luarocks/manif/writer.lua)
- [LuaRocks upload client](https://github.com/luarocks/luarocks/blob/main/src/luarocks/cmd/upload.lua)
