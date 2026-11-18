# Virtual adapter conformance table

[ADP-08 #1709](https://github.com/weaveplatform/artifactweave/issues/1709)
qualifies the 23 adopted adapters through one executable entry point:
[`TestAccVirtualAdapterConformance`](../../test/server/artifacts/virtual_adapter_conformance_test.go).
`TestUnitVirtualAdapterConformanceInventory` compares the row keys with an
independent, explicit list of all 23 types, rejecting omissions, extras and
duplicates. The table uses the assembled application runtime, real SQLite or
PostgreSQL, and a private upstream. It does not use a fake protocol or compute
its expected answers with `virtual.Members`.

The table supplements the retained [native-client evidence](../operations/virtual-repositories.md#family-evidence).
It is handler and SQL qualification, not a claim that these HTTP requests run
23 external package-manager installations.

## Graph and independent expectations

The repository API first creates empty `outer` and `inner` virtuals, then links
them normally. Empty virtuals and cycles are accepted; resolution skips repeats
([decision 0008 R1/R2](../research/decisions/0008-virtual-repositories.md)).

```text
outer = [origin, local, inner, origin, outer]
inner = [federated, outer, local, inner]

then:
outer = [origin, inner, local, origin, outer]
inner = [federated, outer, local, inner, federated]

finally:
outer = [origin, inner, origin, outer]
inner = [outer, inner]
```

`origin` is configured before `local`, but ordinary hosted members precede
ordinary remotes. The first graph therefore selects `local` for a shared
identity. Moving `inner` before `local` selects its federated member instead.
Removing hosted members selects the repeated remote once, despite both cycles.
Each supported real member also has a direct native control.

Merged indexes contain a shared identity with distinct owner metadata and one
unique contribution per real member. Assertions require every unique
contribution once and the complete shared record from the explicit expected
owner. Release registries use shared version `1.0.0` and unique versions
`2.0.0` (local), `3.0.0` (federated), and `4.0.0` (remote). File routes compare
distinct valid representations. Content-addressed protocols retain identical
bytes for one hash and use actual item download counts plus upstream requests
to identify the serving member.

## Row inventory

The fixture helpers are split by representation, but all rows run from the
single entry point above.

| Type | Native observation | Explicit format expectation |
| --- | --- | --- |
| `gems` | `api/gems/{repo}/gems/rake-12.3.1.gem` | First valid gem representation; compact-index checksum authorizes the remote body. |
| `luarocks` | `api/luarocks/{repo}/demo-1.0-1.rockspec` | First rockspec; the remote manifest authorizes its identity. |
| `pub` | `api/pub/{repo}/api/packages/demo` | Version union; winning version retains its owner's pubspec description. |
| `alpine` | `v3.20/main/x86_64/APKINDEX.tar.gz` | Package union; duplicate record retains its owner's `T` field. |
| `rpm` | `repodata/repomd.xml` and its checksum-named primary | Package union; duplicate retains its owner's description; generated primary digest validates. |
| `opkg` | `feed/Packages` | Filename union; duplicate retains its owner's description. |
| `debian` | `dists/stable/main/binary-amd64/Packages` | Package union; duplicate retains its owner's raw description; upstream Release binds the index. |
| `cran` | `src/contrib/PACKAGES` | Package union at equal versions; duplicate retains its owner's license. |
| `p2` | `compositeContent.xml`, nested composite and member `content.jar` | Hierarchical child URLs, repeat/cycle suppression and unchanged site bytes; no invented flattened P2 index. |
| `ansible` | Galaxy v3 collection versions and each version document | Version union; winning release retains its owner's description. |
| `chef` | Supermarket cookbook and version documents | Version union; winning release retains its owner's license. |
| `puppet` | Forge v3 release list | Version union; winning release retains its owner's manifest summary. |
| `conda` | `api/conda/{repo}/noarch/repodata.json` | Filename union; duplicate retains its owner's license. |
| `bower` | Native refs and each version's binary archive | Version union; actual winning archive contains the expected owner's file. |
| `cocoapods` | CDN names and each podspec | Name union; winning podspec retains its owner's summary. |
| `composer` | Composer 2 `p2/acme/demo.json` | Version union; winning release retains its owner's description. |
| `swift` | `api/swift/{repo}/acme/demo/1.0.0.zip` | First valid source ZIP; remote release metadata binds its checksum. |
| `conan` | Conan v2 recipe revision's `conanmanifest.txt` | First complete revision; manifest bytes identify the owner. |
| `bazelmodules` | `modules/demo/1.0.0/MODULE.bazel` | First complete version with module and valid source; same-origin remote source stays authorized. |
| `hex` | `api/hex/{repo}/tarballs/decimal-2.0.0.tar` | First tarball; the remote signed package record binds its exact bytes. |
| `nix` | Native binary-cache NAR route | Hosted download counts identify the first holder; only the remote-only graph fetches the hash-bound upstream archive. |
| `gitlfs` | Native LFS object download | Hosted download counts identify the first holder; remote-only graph uses batch and hash-bound object download. |
| `huggingfaceml` | Hub `resolve/main/config.json` | First model holder; real native hosted commits and upstream Git blob identity distinguish owners. |

P2's second hosted repository is **local Generic**, not federated or local P2:
its [existing class/type contract](../research/decisions/0008-virtual-repositories.md)
permits local Maven-family/Generic members. Hex also uses two locals because its
[existing repository-class matrix](../../internal/artifacts/api/repo/repo_test.go)
does not permit federated Hex repositories. The common fixture's repository key
`federated` identifies this second hosted position; these two rows explicitly
use `rclass=local`. Other rows exercise an actual federated member.

P2 preserves composites: the first root lists `local, inner, origin`; reading
`outer/inner` lists `federated, local`, with the outer/self cycles omitted. After
moving `inner` first, the root lists `inner, origin`; that nested composite still
lists `federated, local`. Site files remain addressable under those member URLs.

## Running the table

```sh
GOWORK=off GOFLAGS=-p=2 go test ./test/server/artifacts \
  -run '^TestUnitVirtualAdapterConformanceInventory$' -count=1

GOWORK=off GOFLAGS=-p=2 go test ./test/server/artifacts \
  -run '^TestAccVirtualAdapterConformance$/^sqlite$' -count=1 -v

ARTIFACTWEAVE_TEST_POSTGRES_DSN='postgres://aw:aw@127.0.0.1:5432/artifactweave?sslmode=disable' \
  GOWORK=off GOFLAGS=-p=2 go test -p 1 ./test/server/artifacts \
  -run '^TestAccVirtualAdapterConformance$/^postgres$' -count=1 -v
```

Without an explicit PostgreSQL DSN, the existing test helper starts the pinned
PostgreSQL container where supported. Each row uses a fresh scratch schema and
drops only that schema. A failed or skipped backend does not qualify that lane.
Run the same entry point with `-race -shuffle=on` for the concurrency control.
