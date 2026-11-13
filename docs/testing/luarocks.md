# LuaRocks local, federated, remote and virtual qualification

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

The real `luarocks` client in the [pinned client image](ci-client-images.md) runs the
[`@luarocks` acceptance scenario](../../test/acceptance/features/luarocks_local.feature):
native rockspec/source upload followed by pinned and latest installs. The
same feature also publishes and installs through a one-node federated
repository. The local/federated API and lifecycle tests also cover denial, invalid layout,
malformed rockspec, invalid rock archive, restore, copy, and full reindex.
Issue [#685](https://github.com/weaveplatform/artifactweave/issues/685)
adds the remote rocks-server path. It reads upstream `manifest` and
`manifest-5.1` through `manifest-5.5` as bounded, non-executable Lua tables,
keeps version/architecture metadata authoritative, and filters each disclosed
rock by the caller's item read grant. The handler asks the shared P9 source
policy to cache raw metadata and only fetches a rock after its exact filename
appears in the current upstream manifest. Rockspecs and ZIP rocks pass the same
identity and archive admission as local uploads before entering remote cache.
The parser accepts dotted package names found in the public registry. Historical
manifest entries without a `-revision` suffix are omitted from the projected
manifest because they cannot pass the shared package identity validator; safe
current entries remain usable.

The [remote native-client scenario](../../test/acceptance/features/luarocks_remote.feature)
packs a source rock, installs it through the remote URL with a fresh LuaRocks
client using `--only-server`, and installs again from the server cache after an upstream outage.
The remote adapter tests cover narrow grants, cold/offline reads, refresh and
zap, malformed/truncated metadata, unsafe rocks and unapproved redirects with
upstream request counts. The remote parser and delivery-fence tests include
malformed Lua syntax, duplicate identities, cancellation and authorization
revocation; they keep the LuaRocks package above the 90% coverage floor.

Issue [#686](https://github.com/weaveplatform/artifactweave/issues/686) adds a
virtual URL over configured LuaRocks local and remote members. It
validates each member's class and package type at request time, filters every
member manifest by that member's read rights, and merges package, version,
revision and architecture records in configured member order. If members
publish the same exact rock filename, the first member wins. The same order
selects artifact bytes. Remote member reads use the P9 source policy, so a
warm offline cache remains available without forwarding caller credentials or
allowing the client to fetch directly from upstream. Native upload through the
virtual URL writes only to its configured local deployment member;
the caller needs write rights on both the virtual path and the target path.
Unconfigured or invalid targets are refused.

The [virtual native-client scenario](../../test/acceptance/features/luarocks_virtual.feature)
installs local-only, remote-only and mixed dependencies using the virtual URL
and `--only-server`, publishes through its local target, and repeats the mixed
install after taking the upstream offline. The full release qualification must
run all three LuaRocks scenarios and the merged coverage gate. Cross-peer
federation is tracked separately in P13.

The client asks for `manifest-<Lua version>.zip`, and for
`manifest-<Lua version>.json` first under LuaJIT, before
`manifest-<Lua version>`, and moves on to the next name when one fails
([manif.lua](https://github.com/luarocks/luarocks/blob/v3.13.0/src/luarocks/manif.lua),
luarocks 3.13.0). The JFrog guide names only the `manifest` and
`manifest-<version>` index files, and the manifest work builds only
`manifest` and `manifest-5.1` through `manifest-5.5`. A local or federated
repository serves any other `manifest-5.` name only when that file is
stored, and otherwise answers 404 "Could not find resource" without queuing
the manifest work. No JFrog source says what 7.x answers for these names, so
the 404 is [I] on #216. An uploaded rock or rockspec whose stored content
fails a read while it is checked answers 500 and is not stored: only content
that breaks the rules is refused 400. A remote rock or manifest whose cached
copy fails a read while it is checked, or that the server's storage fails to
write, commit or publish, answers 500 "internal error" and is not cached; an
upstream rock or manifest that breaks the rules still answers 502
([#1269](https://github.com/weaveplatform/artifactweave/issues/1269); the 500
is [I] on #216).

The JFrog guide documents the base URL, flat layout and automatic manifest
recalculation. The upstream client source documents its upload request
sequence and manifest structure. We have not captured Artifactory 7.161.32's
native upload response or property keys; these remain [I] on #216.

- [JFrog LuaRocks guide](https://docs.jfrog.com/artifactory/docs/luarocks-repositories)
- [LuaRocks manifest documentation](https://luarocks.org/docs)
- [LuaRocks manifest writer](https://github.com/luarocks/luarocks/blob/main/src/luarocks/manif/writer.lua)
- [LuaRocks manifest reader, luarocks 3.13.0](https://github.com/luarocks/luarocks/blob/v3.13.0/src/luarocks/manif.lua)
- [LuaRocks upload client](https://github.com/luarocks/luarocks/blob/main/src/luarocks/cmd/upload.lua)
