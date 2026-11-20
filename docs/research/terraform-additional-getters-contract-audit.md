# Terraform additional getters: contract audit for #668

Researched 2026-11-19 at base `abfb4c62bbd3c6c2ccb4bdd592789227a1bfa76f`.
Scope: the complete [#668](https://github.com/weaveplatform/artifactweave/issues/668)
contract, following closed #631. This is a source audit and implementation/qualification
plan, not an Artifactory observation. No client, Git/SSH/cloud getter, build or vendor
trial was run during the initial audit. Later controlled native results are recorded
below; no Git/SSH/cloud getter or vendor trial was executed. ADR0040 remains Proposed.

Evidence labels: **[D]** first-party documentation; **[S]** version-pinned first-party
source; **[L]** inspected repository implementation/evidence; **[I]** proposed local
choice; **[C]** outstanding live-vendor capture. The current issue body and maintainer
comments were read from the session's freshly fetched read-only GitHub captures.

## Outcome and scope boundary

The implementable scope is bounded HTTP(S) vanity documents, checksum-file retrieval,
additional GitHub shorthand normalization and explicit qualification of snapshot/ref/
depth/subdirectory semantics, plus real Terraform **and** OpenTofu qualification and
the documentation handoff. These are independent seams, but shipping only a parser,
API fixture or research document does not satisfy #668. Existing #631 native
consumption, local/provider/backend and virtual-module behavior remain required.

Generic Git, Hg, SSH, S3 and GCS are conditional families in the issue, not permission
to execute installed tools or discover ambient credentials. Their documented client
syntax alone creates no Artifactory parity obligation. An explicit vendor target and
reviewed credential/transport design must precede any activation. OpenTofu's extra
OCI getter likewise does not expand this Terraform issue automatically.

[#1291's maintainer disposition](https://github.com/weaveplatform/artifactweave/issues/1291#issuecomment-6882320399)
expressly leaves document Read checks and list-filter paths unchanged until P19 TF-6.
Neither this audit nor the getter work may partially move those checks. This is a
separate, already assigned capture, not a blocker to implementing additional getters.

## Pinned clients and what their dependency actually exposes

**[L]** `deploy/check.sh` and the generated client recipe pin Terraform **1.17.1**.
There is no OpenTofu binary/version pin in the inspected repository. **[S]** Terraform's
[v1.17.1 go.mod](https://github.com/hashicorp/terraform/blob/v1.17.1/go.mod) requires
`github.com/hashicorp/go-getter v1.8.6`. Its
[getter configuration](https://github.com/hashicorp/terraform/blob/v1.17.1/internal/getmodules/getter.go)
uses its own explicit getter/decompressor maps and no package-fetch detectors; its
HTTP getter enables netrc and sets `XTerraformGetLimit: 10`. Dependency defaults are
therefore insufficient evidence of Terraform support.

**[D/S]** Use official stable **OpenTofu 1.13.9**, released 2026-11-05, as the proposed
second-client pin; the [official release](https://github.com/opentofu/opentofu/releases/tag/v1.13.9)
is not a prerelease. Its [go.mod](https://github.com/opentofu/opentofu/blob/v1.13.9/go.mod)
pins go-getter **v1.8.8**. Its
[getter map](https://github.com/opentofu/opentofu/blob/v1.13.9/internal/getmodules/getter.go)
and [HTTP construction](https://github.com/opentofu/opentofu/blob/v1.13.9/internal/getmodules/installer.go)
also override defaults, enable netrc and bound vanity recursion to ten.

Official release metadata supplies reproducible archive pins:

| Client asset | SHA-256 from official release asset metadata |
|---|---|
| [tofu_1.13.9_linux_amd64.zip](https://github.com/opentofu/opentofu/releases/download/v1.13.9/tofu_1.13.9_linux_amd64.zip) | `bc44eb6b06446ba83bd4072114891987f590f66cca712737a7f1147c50c539ce` |
| [tofu_1.13.9_darwin_arm64.zip](https://github.com/opentofu/opentofu/releases/download/v1.13.9/tofu_1.13.9_darwin_arm64.zip) | `29ba2f4c78556a8d25476e5070fe4745495ef649c4a3b827170eb7436d23feb3` |

Qualification must verify downloaded assets against the release's
[SHA256SUMS](https://github.com/opentofu/opentofu/releases/download/v1.13.9/tofu_1.13.9_SHA256SUMS)
and existing trust procedure, retain exact binary `version`/embedded dependency
evidence, and install neither client during this research-only task. This pin is a
proposal for implementation, not an assertion that OpenTofu acceptance already exists.

The later implementation now installs the pinned OpenTofu Linux archive alongside
Terraform in the registry-client action, checks its recorded official SHA-256 and
exports a separate executable path. The dependency catalog and generated native
inventory/recipe record that pin. Local native results are qualified below; a new
client pin still requires actual Linux CI success before its CI qualification.

## Registry location and HTTP vanity are different operations

**[D]** The [Module Registry Protocol](https://developer.hashicorp.com/terraform/internals/module-registry-protocol)
requires a bodyless 204 download response containing `X-Terraform-Get`. It accepts
module source syntax except another registry address. Its `/`, `./` and `../` relative
forms resolve against the download endpoint. **[S]** Terraform's
[registry client](https://github.com/hashicorp/terraform/blob/v1.17.1/internal/registry/client.go)
resolves those forms against its requested download URL; its
[installer](https://github.com/hashicorp/terraform/blob/v1.17.1/internal/initwd/module_install.go)
then admits only a direct remote package, excluding local paths and recursive registries.
The registry installer fetches the whole package and appends the final directory
literally. That is why artifactweave resolves upstream globs to a concrete directory.

**[D]** The [HTTP module-source reference](https://developer.hashicorp.com/terraform/language/block/module#http-urls)
describes a separate vanity GET with `terraform-get=1`, a successful 200 response,
header preference over an HTML `meta name="terraform-get"`, and automatic archive
bypass for recognized extensions. **[S]** The pinned
[HTTP getter v1.8.6](https://github.com/hashicorp/go-getter/blob/v1.8.6/get_http.go)
actually accepts any 2xx, adds the parameter with `Add`, chooses a nonempty header
before body parsing, and does not check HTML MIME. Its non-strict XML parser matches
tag/attribute names case-insensitively but the `name` value exactly; it takes the
first nonempty matching `content`, stopping at `<body>` or `</head>`. The source
itself receives no final-response-base `ResolveReference`. The
[v1.8.8 HTTP implementation](https://github.com/hashicorp/go-getter/blob/v1.8.8/get_http.go)
is byte-identical. These permissive client details do not override server admission.

### Proposed bounded server contract [I]

This is a concrete implementation target, not inferred vendor behavior:

| Concern | Contract to implement and verify |
|---|---|
| Dispatch | Classify a validated source as a qualified archive, GitHub snapshot or HTTP(S) vanity document. An unknown archive hint is an error; it must not fall through into vanity. Never treat a registry coordinate or local path as a relative web URL. |
| Request | Fetch vanity with GET and exactly one `terraform-get=1`. Preserve ordinary query keys; reject duplicate/conflicting reserved parameters. Remove `archive` and `checksum` before the package request. Do not add `terraform-get` to a recognized archive or checksum request. |
| Query propagation | Each selected source owns its query. Do not copy a registry/vanity request's token, query or `terraform-get` into the next source. Preserve explicit child query values. A query-only relative reference must not recover stripped parent secrets from private response-base metadata. |
| Status/header | Vanity success is 200; registry-location success stays 204. For vanity, a present header must be one nonempty field, within 8 KiB and valid source text. Invalid/duplicate headers fail instead of selecting HTML. Header selection wins over body metadata; its source does not depend on body MIME, which is discarded by the control projection. Still drain/validate the complete response within the document bound before publication. Never split an arbitrary URL on comma; reject multiple header fields and validate the complete single value as one source. |
| HTML | Without a header, require an explicit `text/html` or `application/xhtml+xml` representation and a bounded UTF-8/ASCII document. Match a `meta` element with case-insensitive tag/attribute names and exact `name="terraform-get"`; take the first nonempty `content` before body/end-head. Decode character references once, then apply the ordinary source grammar. Duplicate `name`/`content` attributes, parser errors, unsupported encodings and missing/empty source fail. No script, CSS, iframe, image, link or HTML `<base>` fetch/authority. A later source must not rescue an invalid first selected source. |
| Relative source | Split `//subdirectory` and query before URL normalization. Resolve a relative HTTP source against the **sanitized actual final response** scheme/host/path, preserving the existing #631 final-base design. Revalidate the destination after resolution. This is a deliberate server contract, broader than the pinned vanity getter implementation. |
| URL grammar | Retain the existing 8 KiB source bound and refusal of controls, backslashes, userinfo, opaque URLs, fragments, unsafe encoded traversal and HTTPS downgrade. Explicit force prefixes are limited to qualified forms. Validate registry exclusion before resolving a schemeless value; permit qualified GitHub shorthand separately. |
| Bounds | At most ten vanity documents per selected module resolution; the eleventh fails before contact. Keep the common maximum ten HTTP redirects per individual request, independently. Each complete document is at most 16 MiB, so the chain permits at most 160 MiB of vanity-document bytes. Keep the existing 256 version-location lookup bound. Count on cached/offline paths too. |
| Cycles | Keep a request-local visited set of canonical logical source identities: normalized absolute URL plus exact meaningful query and source controls. Track redirected effective documents too. Self, two-node, redirect-assisted and cache-only cycles fail before revisiting. A finite hop bound also catches query-mutating cycles. Do not log the query or visited identities. |
| Storage | Retain validated raw/control metadata and actual response base, separately from original archives and request-specific public URLs. Validate complete document grammar and destinations before metadata admission. Version validation variants when extending semantics. Do not store caller-specific success, permission filtering, URLs or credentials in the raw document. |

The single-header control projection is a new narrow opt-in or equivalent closed
adapter operation. Existing `ModuleDownloadLocation` accepts only 204/JSON projection;
changing it or generic HTTP success/MIME rules globally would weaken unrelated adapters.
Header-selected vanity responses may be projected without pretending arbitrary body
bytes are JSON; the operation must explicitly define its status/header/body admission.
Body-selected HTML must be read completely within the bound before publication even if
the source was found early. Every selected address still passes ordinary source grammar.

## Checksums: file syntax is an additional acquisition, not a URL escape hatch

**[S]** The pinned [checksum implementation](https://github.com/hashicorp/go-getter/blob/v1.8.6/checksum.go)
recognizes `checksum=file:<location>`, retrieves it with a second getter client, and
selects the first matching GNU/BSD record or a filename-free digest. Hash lengths
infer MD5/SHA-1/SHA-256/SHA-512; explicit algorithm names are case-insensitive. Native
matching includes basename, `*`/`?` markers, paths relative to the checksum file and
absolute paths; malformed nonmatching lines can be skipped. The
[v1.8.8 file](https://github.com/hashicorp/go-getter/blob/v1.8.8/checksum.go)
differs only in its copyright year. Relative checksum locations are not resolved
against the archive's HTTP response by this code; normal detectors/Pwd apply.

**[S]** The [getter client](https://github.com/hashicorp/go-getter/blob/v1.8.6/client.go)
removes archive/checksum controls from the underlying request, chooses the longest
configured archive suffix and checks compressed/original file bytes before extraction.
The [pinned README](https://github.com/hashicorp/go-getter/blob/v1.8.6/README.md#checksumming)
documents file retrieval and GNU/BSD formats; it does not grant artifactweave access
to a local checksum path or another transport.

**[I]** First qualify absolute HTTP(S) checksum-file locations only, under the same
origin/path/external-pattern and DNS/credential rules as every other fetch. Reject
file/SSH/cloud schemes, userinfo, fragments, nested `checksum=file:` retrieval and
archive/vanity interpretation for checksum files. A relative web-checksum extension
would need an explicit response-base rule and separate tests; never feed it to local
file detection. Keep at most one checksum-file acquisition per package, a 16 MiB file
bound and an 8 KiB line bound. Require 200, complete bytes and exactly `text/plain`
or `application/octet-stream` MIME (optional UTF-8/US-ASCII charset on text); reject
HTML and control characters other than record whitespace. These are the complete
initial MIME alternatives, not a wildcard or content-sniffed HTML allowance.

Qualify these portable records: GNU `<hex>  <filename>`, `<hex> *<filename>` and
`<hex> ?<filename>`; BSD `<algorithm> (<filename>) = <hex>`; and a file containing
one standalone digest. Accept blank lines. Validate hex, algorithm and digest length
strictly, including explicit algorithms. Match the selected package URL's decoded
basename before transport redirects; where supported, relative identifiers use POSIX
URL paths, not host-OS filesystem rules. Reject an absent selected record, conflicting
or duplicate selected records and malformed nonblank records rather than silently
accepting a later digest. Broader native first-match/path quirks are not claimed.

Bind checksum URL, algorithm, selected filename and digest into the archive descriptor/
representation identity. Enforce it even when optional upstream checksum headers are
ignored. A refreshed checksum selecting different bytes cannot authorize a previously
signed archive. Never publish invalid archive bytes, and never replace a valid entry
with a mismatch. Warm/offline operation must retain the validated checksum decision
and authority without making a new upstream call.

## Compression: qualify the client maps, not the library defaults

The explicit maps cited above give this finite matrix:

| Format/alias | Terraform 1.17.1 | OpenTofu 1.13.9 | #668 consequence |
|---|---|---|---|
| ZIP; gzip `.gz`; tar.gz / tgz; bzip2 `.bz2`; tar.bz2 / tar.tbz2; XZ `.xz`; tar.xz / txz | Configured | Configured | Server already validates these. Exercise native cold/warm consumption and canonical outgoing archive hints. A single compressed file is not implicitly a TAR container. |
| `tbz2` | Configured | Absent | Existing server canonicalization to `tar.bz2` can keep delivery portable; prove it with both clients. |
| Plain `tar` | Absent | Absent | Existing server accepts it and can emit `archive=tar` unchanged. This is a same-kind compatibility finding requiring a real native before/after probe and inline resolution under #668; do not narrow #631 silently. Preserve original TAR bytes while deriving a supported delivery format if needed, with distinct signed digests/format. |
| zst / tar.zst / tzst | Absent | Absent | The [dependency's default map](https://github.com/hashicorp/go-getter/blob/v1.8.6/decompress.go) includes these, but neither pinned native map does. Do not advertise new native Zstandard support from library capability. Any server-side transcoding must have its own bounded validator and two-client proof. |
| `archive=false`, unknown hint, loose file/directory listing | Not an extraction promise | Not an extraction promise | Refuse as unqualified module packages; do not reinterpret a failed archive as vanity. |

The plain-TAR finding was reproduced and corrected locally after this audit, with
native before/after evidence recorded below. Existing complete ZIP CRC, compression
trailer/XZ-footer, entry/expanded-size and link-graph limits remain mandatory for every
new source. If derived bytes are necessary, original checksum/cache identity stays
separate from delivered checksum, length, ETag, range, archive hint and signed identity.

## GitHub shorthand and snapshot qualification

**[S]** Terraform's own [Git detection](https://github.com/hashicorp/terraform/blob/v1.17.1/internal/getmodules/moduleaddrs/detect_git.go)
preserves the query, adds `.git` to `github.com/owner/repo`, and treats extra path
segments as a subdirectory. Its [normalization](https://github.com/hashicorp/terraform/blob/v1.17.1/internal/getmodules/moduleaddrs/detect_remote_shorthands.go)
combines a detected subdirectory with an explicit `//` selector. OpenTofu wraps its
GitHub detector to remove and then restore query parameters. Therefore testing only
raw go-getter's [GitHub detector](https://github.com/hashicorp/go-getter/blob/v1.8.6/detect_github.go)
would miss client-level query behavior. Qualify `github.com/o/r`, optional `.git`,
`github.com/o/r/modules/demo`, explicit `//modules/demo`, and a shorthand subdirectory
combined with another selector; reject empty owner/repository and malformed encoding.

**[S]** The pinned [Git getter](https://github.com/hashicorp/go-getter/blob/v1.8.6/get_git.go)
uses the Git executable, checks out a selected ref and recursively initializes
submodules. Positive depth is passed to clone and submodule operations; shallow clone
uses `--branch`, so a direct commit ID is not equivalent to a named branch/tag at
positive depth. SSH-key and inherited Git environment handling show why directly
enabling this getter is not an acceptable server transport shortcut. The
[v1.8.8 change](https://github.com/hashicorp/go-getter/blob/v1.8.8/get_git.go)
also removes every case-insensitive `.git` entry on update, rather than directories
alone; dependency equality must not be assumed.

**[D]** GitHub [source archives](https://docs.github.com/en/repositories/working-with-files/using-files/downloading-source-code-archives)
are branch/tag/commit snapshots without history; moving a branch/tag can change its
contents, and even an unchanged tree does not guarantee stable compressed bytes.
The [archive API](https://docs.github.com/en/rest/repos/contents#download-a-repository-archive-tar)
supports a commit/branch/tag ref and redirects downloads. Native Git checkout expressions
such as `HEAD~1` are not automatically valid GitHub archive refs.

**[I]** Continue an HTTP archive implementation, with no Git process or credential
helper. Explicitly qualify omitted ref/HEAD, named branch, tag, slash-containing refs,
`refs/heads/...`, `refs/tags/...` and full commit SHA. Test branch/tag-name collision
and unsupported Git revision expressions. Preserve existing accepted depth behavior
until the native comparison establishes a deliberate snapshot-specific rule; accepting
and ignoring a positive depth does not establish shallow-clone parity. The
commit-plus-positive-depth discrepancy must be resolved/documented in this issue,
not hidden by a happy-path tag fixture. Bind the resulting archive digest and exact
source controls into current signed delivery, and define warm snapshot behavior for
a moved ref; do not call a mutable ref intrinsically immutable.

Submodules and LFS have separate limits:

- **[S]** Git's [v2.56.1 archive implementation](https://github.com/git/git/blob/v2.56.1/archive.c)
  emits a gitlink as a directory and does not recurse into it. **[I]** A GitHub
  snapshot cannot stand in for recursive clone materialization. Preserve snapshot
  content and explicitly qualify this limitation; never fetch `.gitmodules` targets.
  Test a module requiring an absent submodule and prove no submodule contact occurs.
- **[D]** GitHub's [LFS archive setting](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/managing-repository-settings/managing-git-lfs-objects-in-archives-of-your-repository)
  defaults to pointer files; administrators can include GitHub-hosted LFS objects,
  while external LFS objects are excluded. **[I]** Preserve archive bytes; never follow
  `.lfsconfig` or hydrate pointers. Test pointer preservation and pre-materialized
  archive content separately. Neither outcome proves native clone/LFS parity.
- **[S]** [SourceDirSubdir/SubdirGlob](https://github.com/hashicorp/go-getter/blob/v1.8.6/source.go)
  splits before the query and requires exactly one glob match. OpenTofu's
  [package selection](https://github.com/opentofu/opentofu/blob/v1.13.9/internal/getmodules/subdir.go)
  retains the complete package for relative sibling references. **[I/L]** Keep the
  server's stronger real-directory/link-escape checks, concrete outgoing selector
  and complete original package; missing/ambiguous/link selectors fail before admission.

## JFrog evidence and the captures still required

**[D]** The [current Terraform repository page](https://docs.jfrog.com/artifactory/docs/terraform-opentofu-and-terraform-backend-repositories)
establishes separate registry/storage URLs, Smart Remote API prefixes, GitHub as the
recommended official-registry provider, OpenTofu registry settings, and GitLab/Terraform
Cloud external-dependency patterns. However, on 2026-11-19 its provider row also lists
BitBucket/Stash/Artifactory/Custom. The issue records earlier Terraform-specific
guidance that other Git providers are unsupported; that exact restriction was not
recoverable in the current page. Treat this as a documentation-version/target-contract
gap, not new authorization or proof of non-GitHub getter parity. The current
[generic remote settings](https://docs.jfrog.com/artifactory/docs/remote-repositories#additional-settings-for-terraform-remote-repositories)
enumerate provider selectors but do not specify a safe generic clone contract.

**[C]** Retain these finite cases for #216/P19, with exact Artifactory build/settings,
client versions, request count/destinations, sanitized status/headers, body hashes,
stored paths/metadata and warm/offline results:

| Capture | Question the source audit cannot settle |
|---|---|
| TF-GET-1 | Which supported registry/storage configurations follow 200 header and HTML vanity sources; exact precedence, relative base after redirect, query propagation and duplicate/malformed behavior? |
| TF-GET-2 | Which checksum-file URL schemes, record grammar, failure statuses and cache paths does that build use? Does failure preserve prior validated bytes? |
| TF-GET-3 | GitHub shorthand extra segments, branch/tag/SHA/HEAD, slash refs, depth/ref collision and mutable-ref warm caching; exact archive format and cache path. |
| TF-GET-4 | Whether Git submodules or LFS are fetched/materialized, with purpose-made public or disposable scoped-credential fixtures; never production credentials. Which non-GitHub providers are actually supported in that build? |
| TF-GET-5 | Supported compression/hints and invalid/truncated payload handling; original versus derived delivery, source rewrite and query/signature behavior. |

No row is a captured observation yet. Existing RTFACT-30340/VCS-layout evidence in
ADR0040 supports the constrained GitHub tag/cache inference, not all these semantics.
TF-6/#1291 remains its existing separate capture with checks unchanged.

## Independent implementation seams and finite full-issue acceptance

**[L]** Inspected: `remote_source.go`/`remote_source_test.go`, `remote.go`/`remote_test.go`,
`remote_archive*.go`, `remote_links*.go`, `module_rewrite.go`, `signed.go`, shared
`source.Resource`, `module_location_test.go`, `metadata_response_test.go`, server cache
tests and `terraform_remote.feature`/steps. Existing fixtures cover direct archives,
source/refusal/revocation, redirected final bases, selectors/links, checksum pins,
refresh/no-local-store and cold/warm/offline Terraform. They do not execute OpenTofu
or qualify the new vanity/checksum/shorthand forms.

| Seam | Concrete work under #668 | Required evidence before calling it delivered |
|---|---|---|
| A: source grammar/classification | Separate terminal archives/GitHub snapshots from qualified HTTP vanity; preserve selector/query ordering, add single-slash GitHub shorthand paths, reject registry/local/unsupported families. | Parser cases for every accepted form, invalid force/query/encoding, shorthand query preservation and recursive registry exclusion. No incidental network activity in parsing. |
| B: vanity source acquisition | Closed 200 header/HTML metadata operation, actual final base, bounds/visited-set, complete validation and raw metadata cache identity. Preserve existing 204 projection and generic behavior. | Header-over-meta, meta-only, mixed case, entities, head/body cutoff, invalid-first/duplicates, MIME/status/encoding, 8 KiB/16 MiB boundaries, ten/eleven documents, cycles through redirects/cache, explicit query propagation and forbidden-host-before-contact tests. |
| C: checksum file | One guarded auxiliary HTTP(S) read; exact grammar/filename/digest selection; bind checksum decision into package identity. | GNU text/binary/portable markers, BSD, standalone digest, final newline/CRLF, blank/malformed/duplicate/missing/oversized records, wrong digest/algorithm, redirect and denied origin. Both stored and transient bytes fail before archive publication; corrected input retries immediately. |
| D: snapshot/content delivery | Qualify ref/depth/submodules/LFS; preserve entire archive, concrete selector and safe links; resolve plain-TAR native finding; canonical portable aliases. | Actual before/after TAR probe; cold selected branch/tag/SHA identity and warm behavior, extra shorthand path, nested sibling modules, pointer/submodule limitations without auxiliary contact, exact original hashes and separately correct derived hashes/format/ETag/ranges. |
| E: shared authority/fences | Route **every** new document/checksum/archive request through source policy, current source/credential generation and epoch, not an independent HTTP client. Combine all chain result fences for publication/signing/final bytes. | Change credentials/source/policy/zap epoch during each blocked acquisition and after issuance; revoke/expire signer and change effective virtual membership. No stale capability, cache poison or credential propagation to unrelated origins. Cancellation closes streams/temp files. |
| F: two real clients | Pin Terraform 1.17.1 and OpenTofu 1.13.9; extend executable acceptance/recipes/CI with isolated client configuration and caches. | For **each newly accepted form**, both clients cold-init and apply identifiable outputs through artifactweave, then repeat during upstream outage/offline with a fresh client module cache. Assert module version, extracted file contents, nested relative module behavior, stored original bytes and no client contact outside artifactweave. |
| G: failures/cache modes | Define invalid upstream grammar/content as 502 without successful partial metadata or new invalid archive; preserve established missing/auth/source status mapping. Retain local-store-disabled, TTL, metadata zap and offline semantics. | Malformed headers/HTML/checksum, mismatch, unsupported source, traversal/link escape, oversize/depth/cycles and unsafe destination all fail deterministically. Existing valid bytes are not overwritten. Local `storeArtifactsLocally=false` validates transiently and leaves no reusable archive; cold offline cannot manufacture bytes. Preserve the shared contract's distinct treatment of upstream HTTP `Cache-Control: no-store`. |
| H: complete regression/docs handoff | Update ADR0040 (still Proposed), operations, source/retrieval contracts, roadmap, recipe **source** and generated evidence fingerprints through existing tooling. Link any conditional transport owners and vendor captures precisely. | Existing #631, direct/virtual modules, local/federated/provider registry/mirror and local-only state-backend acceptance all remain passing; repository quality/coverage gates pass. Document supported forms and limits from executable evidence, distinguish [I]/[C], and keep #1291 unchanged. |

The expected native dataset is finite: header vanity, HTML vanity, redirected relative
vanity, a multi-document chain, GNU/BSD/standalone checksum-file packages, additional
GitHub shorthand paths/ref cases and every newly portable compression/delivery form.
Reuse one package with distinguishable versions, a selected directory and a relative
sibling; do not substitute a provider install or API response for module extraction.
Negative cases can use deterministic server tests, but new accepted forms require the
two real native clients in both cold and warm-server/fresh-client modes.

All A–H work can ship as #668 without another implementation wave: none depends on
changing #1291 or implementing generic VCS/cloud transports. #216 retains unobserved
vendor parity; it does not remove the local implementation/native acceptance work.
Conditional Git/Hg/SSH/S3/GCS (and unrequested OCI) remain disabled unless their explicit
target and credential/transport contract is reviewed. Any later activation needs a
named owner and the same complete matrix, rather than an implicit ambient getter.
Closing #668 requires A–H implementation and qualification, not this audit alone.

### Permanent standalone SQL/native acceptance progress (2026-11-19)

The permanent `terraform_getters.feature` scenarios run pinned Terraform 1.17.1 and
OpenTofu 1.13.9 against the standalone application with SQLite and isolated PostgreSQL
17.12 scratch schemas, through both direct remote and virtual registry coordinates.
Clients select the newer matching version from a list containing older valid
locations. An unselected archive is never acquired. Each installation checks the
selected version and concrete directory, distinguishable apply outputs, relative
sibling modules where applicable and the exact cached original source bytes. It
repeats after stopping the controlled upstream and setting the remote offline, with
a fresh native HOME, configuration and module cache. Child environments are allowlisted;
no ambient Git, SSH, cloud, netrc or CLI credentials enter them.

Eight scenarios pass locally with 19 source forms and two cache phases: 304 module
installations, zero skipped scenarios, 102.965 seconds including the test build, with
the recorded runtime/acceptance source hashes unchanged. The forms are header/HTML/
redirect-relative/chained vanity, GNU/BSD/standalone checksum files, a vanity-selected
checksum file, plain TAR, tgz, tar.gz, tar.bz2, tbz2, tar.tbz2, tar.xz, txz and single
gzip/bzip2/XZ files. The private evidence packet is
`cover/terraform-getters/registry-native-fixed/{source.json,result.json,native.log}`.
This extends the earlier source-URL probes with actual registry version negotiation;
it does not turn the earlier in-memory probes into SQL evidence retroactively.

Linux CI must perform native HTTPS service discovery through the TLS fixture,
without a host-services override. On macOS the pinned executables do not trust the
fixture through `SSL_CERT_FILE`, so local acceptance explicitly uses the existing
loopback service override populated from the application's real discovery document.
The proof labels that mode `local-service-override`. The local result qualifies
registry versions/download/install behavior but does not claim native HTTPS discovery.

The expanded matrix exposed another same-kind defect: both real clients reject a
single compressed file when their module installer requests a directory, reporting
that gzip/bzip2/XZ files can only unarchive to a single file. The frozen-source
before packet `registry-native-expanded` records those failures across all eight
scenarios. The inline correction derives a tar.gz with one decoded regular file,
named from the selected URL without its compression suffix. Source config rewriting
still applies to `.tf`/`.tf.json`; non-config data keeps the existing bounded streaming
limit. Cached original bytes/digests remain separate from signed public format/digest.
The direct/virtual HTTP regression covers all three formats with unchanged config,
rewritten JSON config and non-config content, including format-tamper refusal.

This is not complete #668 qualification: the remaining GitHub snapshot/ref/depth/
submodule/LFS matrix, Linux HTTPS CI, existing local/provider/backend regression and
final quality/coverage acceptance still need their own evidence. Generic Git/Hg/SSH/
cloud getters remain disabled and live Artifactory behavior remains unobserved.

## Local same-kind TAR correction (2026-11-19)

The actual remote HTTP handler over its existing in-memory fixture was exercised
with **Terraform 1.17.1** and **OpenTofu 1.13.9**, using isolated native homes and
no inherited getter credentials. Each plain-TAR source failed native initialization
with `XML syntax error ... illegal character code U+0000`; both tar.gz controls
initialized, applied and produced the expected constant module output. The
production source was unchanged during the before probe. This is controlled
artifactweave/client evidence, not an Artifactory observation or SQL-backend proof.

The local correction uses the existing guarded derived tar.gz representation for
plain TAR even when no module-source text needs rewriting. The original source
archive stays in the cache with its original digest; public delivery advertises
its derived format and digest. Both native failures become passing init/apply/output
round trips, and both tar.gz controls remain passing. The
[direct/virtual HTTP regression](../../internal/artifacts/ecosystem/terraform/http/plain_tar_delivery_test.go)
checks original source bytes, module content/permissions, derived checksum, HEAD,
ETag, ranges and signed-format tampering. The full Terraform HTTP race/shuffle
suite and configured package lint pass. Source/probe packets are retained under
`cover/terraform-getters`; these local results do not complete the full A–H matrix.

## Local GitHub shorthand grammar progress (2026-11-19)

The parser now treats additional path segments in a qualified `github.com/owner/repo`
shorthand as a subdirectory and appends an explicit `//` selector, preserving the
selected ref and existing snapshot cache identity. An optional `.git` suffix retains
the same repository identity. Path validation precedes URL resolution, so cleaning
cannot hide traversal or encoded separators. Destination policy still runs on the
resulting archive URL, and an explicit non-Git force prefix cannot silently become
a GitHub snapshot. Absolute forced-Git URLs retain their existing grammar.

Named unit cases cover these forms, branch/tag slash refs, full commits, the existing
snapshot-specific positive-depth behavior, malformed selectors/encoding, duplicate
controls and configured/external destination policy. These are parser results only;
new native cold/warm-client acceptance remains required before calling this seam
qualified. No Git process, credential helper, submodule or LFS retrieval was enabled.

## Local bounded vanity implementation and native proof (2026-11-19)

The source classifier and registry-location validation variant now admit qualified
HTTP(S) vanity documents alongside terminal archives and GitHub snapshots. A distinct
closed metadata projection admits only a complete 200 response, selects one valid
`X-Terraform-Get` header or a UTF-8/ASCII HTML representation, and preserves private
caller-independent control bytes. Header-selected responses still read the whole
bounded body before publication. HTML control bytes use base64 within JSON to avoid
unbounded escaping overhead. The existing 204 registry projection remains separate.

The adapter selects the first nonempty HTML `terraform-get` source before body/end-head,
rejects duplicate relevant attributes and validates the selected source before cache
admission. Relative sources use the sanitized actual response base. Each logical
vanity GET adds exactly one `terraform-get=1`; ordinary HTTP redirects retain the
shared transport's existing redirect semantics. Children retain their own explicit
query, with no parent query recovery. Opaque identities of requested and effective
documents detect cycles without persisting their private response query. At most ten
documents are acquired, including cached/offline reads, each with a 16 MiB input bound.
Metadata paths omit those document identities; the existing cache's keyed URL
fingerprint distinguishes raw documents without exposing their private query.

All new acquisitions use the existing source-policy service and combine every hop's
current-authority and usage callbacks. Direct/virtual HTTP controls pass for cold,
warm and offline reuse, both local-store settings, upstream `no-store`, invalid-input
retry, ten/eleven hops, cycles, redirected aliases and original-byte delivery.
Blocked-body controls pass for user credential changes, source changes, metadata zap,
virtual member removal and cancellation, with no signed capability or archive fetch.
Closed-contract tests refuse invalid requests before source resolution, and the
original registry projection controls continue to pass.

**Local native evidence:** Terraform 1.17.1 and OpenTofu 1.13.9 each passed header,
HTML, redirected-relative and chained source initialization/application/output in
cold mode and with warm offline server storage plus a fresh client directory:
**16 round trips, zero skips**. Every package selected `root/modules/demo` and evaluated
a relative sibling module; the native manifest retained both paths and cached source
archives retained their exact original bytes. Native homes, CLI configs and netrc
were isolated and no ambient getter credentials were inherited. The source stayed
unchanged during this probe. Evidence is retained in
`cover/terraform-getters/vanity-native-final`. The final repeat passed in 8.684 seconds
with all source and probe hashes captured before execution.

These results exercise the production remote HTTP handler over controlled in-memory
fixtures. They are not standalone-server/SQL-backend qualification, permanent CI
native acceptance or Artifactory observations. The probe harness obtains the registry
download answer and passes its rewritten signed source to each native client; it does
not exercise native registry discovery or version negotiation. Full two-client registry
acceptance still needs that path. Checksum-file support, the remaining
snapshot/native matrix and documentation/CI handoff remain required by A–H; #668
stays open and ADR0040 remains Proposed.

The final local race/coverage pass uses unchanged production source and passes the
90% per-package threshold: remote source 463/485 (95.464%), remote policy 1,409/1,564
(90.090%) and Terraform HTTP 2,929/3,253 (90.040%). Architecture, configured lint,
test-kind and generated-recipe checks pass. These are changed-package local profiles;
the full repository's 95% combined gate remains separate required CI evidence.

## Local checksum-file implementation and qualification (2026-11-19)

Absolute HTTP(S) `checksum=file:` sources now use a separate closed, bounded
metadata operation. It requires complete 200 bytes, `text/plain` or
`application/octet-stream`, supported text encodings and no disallowed controls.
Files are at most 16 MiB, records at most 8 KiB. GNU text/binary/portable markers,
BSD records and one standalone digest are supported for MD5/SHA-1/SHA-256/SHA-512.
The complete file must parse: missing, duplicate, conflicting, malformed and
unmatched selected records fail before archive acquisition. Filenames match the
archive URL's decoded basename before archive redirects; qualified relative
records use POSIX paths against the original checksum URL on the same origin.

The URL parser refuses relative/local/SSH/cloud checksum targets, userinfo,
fragments, HTTPS downgrade and nested getter controls. Query values remain owned
by each source. A vanity chain may carry one checksum-file URL into its terminal
archive; conflicting file URLs fail. The checksum URL, selected filename and
digest participate in the private archive descriptor. Every read uses the normal
source/credential, DNS/redirect, cache and final-authority fences.

An archive rejected with the adapter's sanitized 502 response leaves its checksum
decision unconfirmed. The result's current-authority callback forgets only that
exact stored metadata generation through the existing transactional CAS eviction;
a newer replacement is preserved. This permits corrected input to retry immediately
without advancing TTL. Policy-only edits, revoked callers, non-network intent and
storage-error identity have dedicated controls. Transient reads retain no reusable
file or archive. Refreshing a checksum to different bytes rejects an old signed
archive capability before it can replace the prior valid copy.

The local race/coverage run passed on frozen source: remote source 470/494
(95.142%), remote policy 1,462/1,621 (90.191%), Terraform HTTP 3,057/3,393 (90.097%)
and the unchanged Smart Remote package 665/681 (97.651%). Configured lint and the
focused parser, core rejection and HTTP suites passed.

Pinned Terraform 1.17.1 and OpenTofu 1.13.9 passed GNU, BSD, standalone and
vanity-chain checksum-file init/apply/output probes in both cold and offline-server/
fresh-client modes: **16 checksum round trips**. A repeat retained **16 vanity
round trips and four TAR controls**, all **36 with zero skips**, original archive
bytes, selected directories and relative sibling modules. All production and
probe hashes were captured before execution and remained unchanged. Evidence is
under `cover/terraform-getters/checksum-native` and `checksum-quality`.

These are production HTTP-handler probes over controlled in-memory fixtures. Native
clients consume rewritten signed sources obtained by the probe harness; full native
registry discovery/version negotiation, standalone/SQL qualification, the remaining
snapshot/compression matrix, permanent two-client CI acceptance and final operations/
ADR/roadmap handoff remain required. #668 remains open; no live vendor comparison
or final repository-wide coverage qualification is claimed.

## Snapshot and source-capability qualification progress (2026-11-19)

The later snapshot matrix runs both pinned clients against the actual application
router and SQLite/PostgreSQL stores. Its immutable fixture bindings supply DNS,
certificate trust and physical dial addresses for the controlled registry/GitHub/
codeload HTTPS servers. The production resolver, pool, TLS server-name verification,
redirect policy, credential handling and current-authority callbacks remain active.
This is an in-process application-router proof, distinct from the 19-form feature's
standalone-process proof and from live GitHub/Artifactory observations.

Fourteen source/ref/content forms cover omitted ref/HEAD, optional `.git`, additional
shorthand paths, explicit and combined selectors, forced GitHub HTTPS, slash branch,
qualified branch/tag, unqualified collision, full commit, commit plus positive depth,
LFS pointer data and already-materialized LFS data. Each runs cold, with an upstream
branch moved while the server cache is fresh, and offline with a new native HOME and
module cache. Selected versions/directories, sibling modules, outputs and exact
original bytes retain identity. An absent submodule produces the pinned client's
bounded directory refusal; neither `.gitmodules` nor `.lfsconfig` causes auxiliary
source requests. The collision fixture proves preservation of the requested ref;
it does not establish GitHub or Artifactory's live branch/tag precedence.

The two pinned clients also run eight real native clone controls against disposable
local Git repositories with file-only transport, no inherited Git configuration or
credentials, and no SSH/cloud access. Tags, positive-depth named branches and ordinary
commits install; a full commit plus positive clone depth is refused as an unavailable
named ref. The snapshot's accepted commit/depth combination is therefore an explicit
HTTP snapshot choice, not shallow-clone parity. These controls do not assert history
depth for local Git file transport.

All snapshot/control cases passed in 108.093 seconds including the test build, with
recorded production/fixture hashes unchanged. A subsequent combined coverage-enabled
run passed the 19-form standalone features, snapshot matrix and clone controls in
334.540 seconds; **all 3,414 Go-source hashes remained unchanged**. It covers 304
standalone getter installs plus 336 snapshot installs, 24 expected missing-submodule
refusals and eight clone controls (six installs, two expected depth/ref refusals).
Both native suites explicitly label the macOS loopback service override; Linux
must still perform HTTPS discovery without it. Private source/results/log/covdata
packets are retained under `cover/terraform-getters/snapshot-native` and
`combined-native-final`. Acceptance-only coverage is not the repository quality gate.

**[D/I]** Git's [ref-name contract](https://git-scm.com/docs/git-check-ref-format)
separates literal names from revision/reflog expressions. The adapter now applies
that literal grammar before URL mapping, while retaining one-level names, HEAD,
qualified refs, Unicode names and commit IDs. Unsafe operators, controls, dot/lock
components and malformed separators fail before admission. No Git command is added
to the server. Location validation moves to v4 and vanity validation to v3 so older
control admissions are not reused under the stricter grammar.

The audit exposed a same-kind capability gap: six direct/virtual controls showed
that an old signed module download still answered 200 after source query,
checksum-file URL or selector changes when archive bytes were identical. The inline
repair binds the normalized terminal source descriptor into the capability using a
purpose-derived keyed digest. GitHub depth is retained in that descriptor even
though it does not change HTTP snapshot bytes. Missing/malformed binding claims are
refused before acquisition; current normalized source identity is checked again
before signed archive reads. Public claims disclose no source URL/query, and the
keyed digest prevents offline parameter guesses. The same six controls now refuse
old capabilities 403 and serve fresh capabilities with unchanged original bytes.
This Terraform-only correction remains inline; local/provider capability contracts
and shared source error sanitization are unchanged.

The native snapshot/clone checks have a required Terraform-selected Linux CI step
whose Go covdata joins the native acceptance artifact. ADR0040, operations, source/
retrieval contracts, recipe source, dependency pin references and the roadmap now
record these exact forms and limits. Full exact-head Linux/native/95%-aggregate/
90%-each qualification and the existing registry/provider/backend regressions remain
required before #668 closes. #1291 remains explicitly deferred; generic Git/Hg/SSH/
cloud transports remain inactive, with #216/P19 retaining the conditional target and
live-vendor capture obligations.

After normally integrating maintainer-merged main
`3ae2fc2aa624b0d9cd7c915e91a4e4b675729970`, the complete getter/snapshot/clone matrix
passed again in **228.840 seconds**, with **all 3,428 Go-source hashes unchanged**
and the same 640 installs, 24 expected missing-submodule refusals and eight clone
controls. The full Terraform HTTP race/shuffle suite passed on the integrated code
at **3,085/3,421 statements (90.178%)**. Architecture/CI-selector/recipe tests,
test-name checks, all 36 dependency-sync tests and runtime/acceptance lint passed;
the combined standalone and in-process native covdata also merged successfully.
The integrated evidence packet is `cover/terraform-getters/integrated-final`.
These remain local results; fresh exact-head full Linux qualification is still
required, with no replacement of the repository's 95%/90% gates.
