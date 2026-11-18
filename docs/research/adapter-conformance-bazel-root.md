# ADP-08 table and Bazel registry-root finding

## Qualification gap

[ADP-08 #1709](https://github.com/weaveplatform/artifactweave/issues/1709)
started as a missing qualification artifact, not a reproduced runtime defect.
The core resolver's table, the router kit's fake protocol and the generic item
world prove different contracts. The protocol download-policy table and
individual adapter tests do not provide one guarded inventory of all 23
adopted adapters over the same remote-first, nested, repeated and cyclic graph.

The new [table and row inventory](../implementation/virtual-adapter-conformance.md)
exercise real native handlers and real SQL. Each row has an independent
expected holder or contribution set, direct controls and a supported remote
contribution. Metadata fixtures model the stored index state that native reads
consume; they do not claim to retest every format's archive admission. The
existing external-client evidence remains linked separately.

Empty virtuals and cycles are deliberately created and linked through the
normal repository API. Decision 0008 R2 accepts these configurations and skips
repeated virtual/real members during resolution. P2 retains composites over
supported local Generic members; Hex retains its local/remote/virtual class
matrix. These are existing contracts, not table exceptions invented to obtain
a passing result.

## Confirmed existing Bazel defect

On independent, unchanged merged main
`fb67c046de93fee4da1968487a365b05ad8c4cc0`, a private registry configured as
`http://127.0.0.1:<port>` is accepted by the repository API. The same upstream
serves its module metadata, `MODULE.bazel` and a source document whose archive
URL is `http://127.0.0.1:<port>/source.tar.gz`.

| Request/configuration | Main answer |
| --- | --- |
| Direct remote `MODULE.bazel`, slashless root | 200 |
| Direct remote `source.json`, slashless root | 502 |
| Remote-only cyclic virtual `MODULE.bazel`, slashless root | 404 |
| Direct source and complete virtual row, same root with trailing `/` | 200 |

The source-policy implementation is identical on the initial `cbed189b` and
independent `fb67c046` trees (SHA-256
`4da1dc39c31b6d34729d13679f9c592790e63c034582fd4f664bf376b70f10b6`).
Only test overlays were used for the independent baseline. The new unit
contract fails solely for the empty root path; explicit `/`, scoped paths,
sibling-prefix, outside-prefix, origin and scheme controls retain their
expected results.

`allowSourceURL` parsed the configured URL then used `path.Clean(base.Path)`.
An absent URL path becomes the filesystem-style `.`. Consequently a valid
same-origin `/source.tar.gz` fails the prefix comparison. The virtual adapter
requires a usable source before selecting a version, so it passes that member
over; this explains the virtual 404 without changing the virtual walk.

The [operator guide](../operations/bazel-modules.md) itself recommends a root
such as `https://bcr.bazel.build` without a trailing slash. The current
[JFrog Bazel Modules guide](https://docs.jfrog.com/artifactory/docs/bazel-modules-repositories)
documents proxying BCR and aggregating local and remote modules. The URL
equivalence is independently specified by
[RFC 3986 section 6.2.3](https://www.rfc-editor.org/rfc/rfc3986#section-6.2.3):
HTTP's empty path and `/` identify the same root. Neither source specifies an
exact Artifactory error for this implementation defect, and no such vendor
status is claimed.

## Narrow correction and controls

Normalize only an empty configured URL path to `/` before the existing
same-origin/path-prefix check. Keep scheme, host, port, scoped-path,
credential, traversal and explicitly allowed external-source checks. The
cross-protocol row retains the slashless URL and requires the remote-only
virtual result; a fixture-only trailing-slash workaround would conceal the
defect.

`TestUnitSourceRegistryRootAndScopedPrefix` covers empty/slashed roots,
scoped/slashed prefixes, sibling and outside-prefix paths, foreign origins,
matching/foreign ports, scheme downgrade and embedded credentials. Existing
source tests retain explicit external-pattern and integrity controls. The
existing pinned Bazel native-client fixture now also configures a slashless
root, so its remote and virtual builds must receive the fixture archive
through that corrected policy boundary; local/federated builds and the
yanked-version refusal remain controls.

This is one Bazel-only behavior discovered while qualifying that row and is
fixed in the same PR under CONTRIBUTING's artifact-kind rule. Exact issue
searches for `Bazel source URL`, `Bazel trailing slash` and `Bazel source.json`
found no existing leaf covering it. The live #1709 findings distinguish this
confirmed defect from the original qualification-only report and retain every
original acceptance criterion. No shared source service, generic URL policy,
configuration shape or unrelated adapter runtime is changed.

No live Artifactory trial or capture was used. This local qualification and
standards-based defect do not assert new inferred vendor behavior or publish
anything to the inference comparison issue.
