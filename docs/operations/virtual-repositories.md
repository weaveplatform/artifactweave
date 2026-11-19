# Virtual repositories

A virtual repository gives clients one URL over real local, federated and remote
repositories. It owns configuration and, for selected formats, derived metadata;
artifacts remain in a member or its remote cache. The [generated type matrix](../implementation/package-format-matrix.md)
separates the resolution mechanism from client/server evidence. Registered `V` is
not itself a native-client qualification claim. A federated member here is the
one-node repository contract; cross-peer convergence belongs to P13.

This guide describes merged implementation through main `621523d38` (2026-11-18).
The [qualification and epic closeout record](../testing/phase10-signoff.md)
distinguishes delivered work from the remaining final gate. GitHub closed #1571
with documentation PR #1708; [EPIC-QUA #1528](https://github.com/weaveplatform/artifactweave/issues/1528)
owns final qualification, the plan status and decision 0008's acceptance. The
[source-backed rules](../research/decisions/0008-virtual-repositories.md) retain
unsettled compatibility choices as [I]. The [inference register](#inference-register)
links the actual existing #216 entries; no live Artifactory capture is claimed.

## R1. Effective member order

For item downloads, the six groups are priority local/federated, priority remote
caches, priority remote upstreams, then the same three groups without priority.
Within a group, use configured depth-first order. Thus a listed remote normally
follows local members; `priorityResolution` moves it into the priority groups.
A priority miss still falls through. An expired remote cache entry does not hide
a later fresh copy; the selected remote's own freshness, offline and stale rules
still decide whether it can serve bytes.

Package handlers use the same effective real-member order but ask each remote
once, through its source/cache policy. A format can give priority package-level
meaning: npm and NuGet shadow an entire matching package before query filters.
They do not simply choose the highest version across all members.

## R2. Nesting and configuration

Nested virtuals flatten depth first. Each virtual is visited once; cycles,
self-members and diamonds do not loop. A real member retains its first position.
Nested patterns can drop a route before a member contributes. A missing stored
member is skipped. The repositories API checks membership and deployment targets
when saving configuration; it accepts an empty virtual and cycles. Exact save
statuses and the closed-validator choices are [I] ([ENG-04 entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6919104004)).

Members normally share the virtual's type. A generic virtual accepts any type;
Maven/Gradle/Ivy/sbt/P2 and OCI/Helm OCI have their recorded group exceptions.
P2 can include local Maven-family or Generic repositories. Classic Helm and Helm
OCI are separate protocols, and Docker is not interchangeable with OCI. See
[R11's complete save rules](../research/decisions/0008-virtual-repositories.md#r11-package-types).

## R3. Misses, permissions and failures

A member miss, blackout, excluded path or unreadable member allows a later holder
to answer. Item/Maven walks remember member Read denials: if nothing serves the
request, a denial can produce 403; otherwise the result is 404. Anonymous denial
uses the normal 401 challenge. Package handlers count unreadable constituents as
absent and retain their protocol's not-found answer. Whole-repository documents
can require Read at a member root; a grant only on one descendant need not admit
an index. Docker catalogs are a separate repository-level ReadAny contract: a
path-scoped reader can list all image names in that readable member once a
usable catalog is cached; a cold fill retains the remote's own publication gate.

A remote's upstream 401/403 describes its configured credentials, not a request
for the client to authenticate. Family implementations pass their expected upstream
failures/misses over. Conan/Bazel/Hex/Nix retain authentication failures as errors;
Bazel's priority-remote control returns 401 despite a later local holder. This is
the recorded [I] [ADP-06 failure contract](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6946397513),
not a new caller Read-denial or a blanket normalization of native errors.
The inferred failure/refusal details and root gates remain
[I] ([shared package entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6931452037), [root-document corrections](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6941939885)).
The raw `maven-metadata.xml` interceptor returns a sanitized 500 for classified
item or authority lookup storage faults. A failure while opening, reading or
parsing a member's content can still allow a later member to serve, or retain
the existing miss or denial response. [Merged #1695](https://github.com/weaveplatform/artifactweave/pull/1695)
and the [phase/source audit](../research/virtual-metadata-faults-source-audit.md)
record this boundary and its current-version compatibility limits.
[Merged #1700](https://github.com/weaveplatform/artifactweave/pull/1700)
preserves source lookup SQL faults and their original cause before consumer
classification; the former #1692 loss of storage identity is fixed. The separate
existing-copy clock-write fault in [#1704](https://github.com/weaveplatform/artifactweave/issues/1704)
remains tracked by the qualification record until its correction merges.
At the recorded merged baseline, a source lookup SQL failure is still suppressed
into a cached 200 index in virtual folder browsing.
[#1723 / PR #1726](https://github.com/weaveplatform/artifactweave/pull/1726)
corrects that separate cross-kind defect: trusted source failures and failed
final authority checks abort the complete index; ordinary initial upstream
listing fallback remains intentional. Its
[full CI](https://github.com/weaveplatform/artifactweave/actions/runs/44430053878)
passed at `d19d9787c0f62fbfd812cc84f79173f4bf19aeb4`; maintainer merge is still
required before final qualification. The correction retains the observed
project source-authority answer without claiming a vendor HTML 500 response.
[Merged #1705](https://github.com/weaveplatform/artifactweave/pull/1705) retains a
complete expired repository file after an explicit origin 404 and advances its
validation clock. Physical index files retain that file contract; native API
documents retain their own miss behavior. The [source trace](../research/expired-remote-copy.md)
records the boundary, strict HTTP-cache controls and modern-version limits.

## R4. Hiding unauthorized resources

`hideUnauthorizedResources`, on the virtual or globally, turns a walk's 401/403
into 404. Without hiding, anonymous denied requests challenge normally. Protocol
handlers retain their native envelopes and early authentication gates; NuGet's
forced-auth virtual challenges before its resource routing. The challenge body
and closed package-handler details remain [I] ([status entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6927146188)).

## R5. Patterns and actual paths

The addressed virtual's patterns apply first, including for administrators.
Nested virtual and real-member patterns filter contributions before paging or
merging. A virtual member attempt checks policy before member Read, the explicit
exception to direct-addressed Read-first behavior in DV-057. Permissions still
apply to the actual item that will be served.

Maven-family item reads translate independently from the addressed virtual's
layout to every nested/real repository. Member checks use the attempted path.
Plain files and `[RELEASE]` may retry the original spelling after a nonterminal
unfound translated result; snapshots and `maven-metadata.xml` do not. Native
package-handler walks retain their own paths. Closed-addon/header details are
[I] ([MVN-06 entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6974481140)).

## R6. Deployment, deletion and transfers

Item PUT, checksum deployment and archive/folder deployment use the virtual's
`defaultDeploymentRepo` (DDR), a local or federated member. Deploy permission and
admission apply at that target path. A missing/invalid target answers 405 with
`Allow: GET` before body admission. Writes keep the exact given path; a different
DDR layout does not invoke read translation. Multipart session creation and its
closing checksum deployment have distinct existing gates, as described in
[R6](../research/decisions/0008-virtual-repositories.md#r6-writes).

Native publication is family-specific. npm, PyPI, NuGet, Go, Docker/OCI/Helm OCI
and Maven clients have DDR flows linked below; adopted protocols include the
recorded RubyGems, LuaRocks, Swift, Conan, Ansible and Git LFS/Hub flows. Classic
Helm publishes via the item route. A resolve-only native endpoint does not gain
a publishing API merely because generic item PUT can address its DDR.

Item DELETE through a virtual returns empty 404 and mutates no member. Copy/move
into virtual, remote or cache targets is refused. Copy/move out of a virtual and
virtual property writes retain their existing refusals. Exact transfer statuses
remain [I] ([write entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6923119893)). Native exceptions retain their own contracts:
Docker manifest/blob DELETE returns empty 400, NuGet DELETE/relist empty 404, and
npm unpublish explicitly acts on its DDR. Address a real member for other deletion.

## R7. Requests originating at another registry

With `artifactoryRequestsCanRetrieveRemoteArtifacts=false` (default), requests
carrying `X-Artifactory-Originated` or `Origin-Artifactory` search remote caches
but cannot start remote upstream steps. This is about another registry, not two
repositories on this node. Upstream requests carry both markers; recursive host
IDs are refused. Swift, Terraform and Composer retain their documented exceptions.
The marker identity shape is [I] ([origin entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6946397513)); Smart Remote has its
[separate source/loop rules](smart-remote.md).

## R8. Merged metadata and freshness

A merged-document cache is separate from every remote's raw/artifact cache.
`virtualRetrievalCachePeriodSecs` governs only types that wire it into their merge:

| Type | Stored virtual representation |
| --- | --- |
| npm | Merged packument model and remote search-page parts. Search results use live local rows plus those pages; rendering/external overlays remain request-specific. Writes do not invalidate the packument; keys union per request. |
| Classic Helm | Merged index; a member write invalidates containing virtual indexes through nesting. |
| Alpine, RPM, Debian, CRAN | Their index/metadata sets. |
| Composer | Merged p2 document. |
| Chef | Cookbook/version documents; catalog/search remain request-paged. |
| Conan | Merged revision, search and package documents; files retain source/member resolution. |
| Conda | Each remote member's subdir part; local/federated records remain live. |
| Ansible | Per-request; the [published comparison correction](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6991066992) identifies the older ADP-04 cached-as-Chef proposal as superseded. |
| Other virtual types | Per-request merge/first-holder lookup; declaring the field does not create a merged cache. |

Artifactweave defaults this field to 7200 seconds; stored explicit periods remain effective. Zero
rebuilds every request; REST rejects negative periods. Cache identity includes
virtual/document, current effective members, whole-member visibility and public
base URL. Path-scoped visibility gets a per-request merge. When a member read observes a contributor 404, it
removes that cached contribution/merged entry; stale-error fallback still rechecks current
visibility/source policy. Only the format's documented member-write behavior
invalidates a document. This bounded cache is per process; P35 owns multi-node
coordination. The precise closed cache/document choices remain [I]
([cache entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6938349119), [Composer/pub/Gems corrections](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6941939885), [family entries below](#family-evidence)).
Docker's 24-hour digest-location hints and source-backed upstream listing models
are separate caches, not a claim of an R8 virtual merged-document cache.

## R9. Storage API and browsing

Storage file/folder views use stored local/federated/cache members, never a cold
remote listing. File details come from the first readable stored item, with the
virtual key in the response; folder children are a union. `?list`/`?stats` are
supported. `?lastModified`/`?permissions` retain 400; properties select the first
nonempty member property set. HTML folder browsing can include remote live
children. Pattern/property differences from downloads remain [I]
([browse entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6938348451)). An npm/Helm merged document or virtual-owned cleaned POM
is not an ordinary uploaded member artifact.

## R10. A remote member keeps its own policy

Read, blackout, includes/excludes, source/credential identity, retrieval TTL,
offline, no-store, network allowlist and redirect rules remain the remote's.
Read alone retrieves warm artifacts; file-backed cold fill/refresh also requires
Deploy/Cache ([#1587](https://github.com/weaveplatform/artifactweave/issues/1587)).
Metadata has its own existing admission rules. The inferred virtual refusal
shapes remain [I] ([shared entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6893506348)). A virtual does not create a new upstream
credential or make an excluded destination reachable.

Cold stored PyPI publication now preserves the already-authorized upstream
pattern namespace while retaining canonical item/cache identity
([merged #1694](https://github.com/weaveplatform/artifactweave/pull/1694), #1670).
No-store/warm reads retain their behavior. The
[shared publication contract](../research/cache-policy-path.md) keeps source,
permission, revision, archive/checksum and transaction fences; the internal
binding is an implementation choice, not a newly claimed vendor API.
All source-owned and project-sharing/network qualifications stay with their
[remote contracts](../implementation/source-boundary.md); project sources and
cross-peer federation are not silently enabled by virtual membership.

## Family evidence

The matrix's anchored proof distinguishes actual native-client scenarios from
server-only controls. These links identify retained implementation evidence;
final release/P10 qualification remains in #1528.

| Type | Operator guide/recipe | Retained virtual evidence |
| --- | --- | --- |
| `alpine` | [guide](../client-recipes/alpine.md) | [client: apk installs a local and a remote member through a signed virtual repository offline](../../test/acceptance/features/alpine.feature) |
| `ansible` | [guide](ansible.md) | [client: Galaxy publishes through a virtual to its deployment repository and installs over a remote and a nested virtual](../../test/acceptance/features/ansible.feature) |
| `bazelmodules` | [guide](bazel-modules.md) | [client: TestAccBazelClient](../../internal/artifacts/ecosystem/bazelmodules/http/bazelmodules_test.go) |
| `bower` | [guide](../client-recipes/bower.md) | [client: virtual dependency rewrite and art shorthand stay cached with fresh client state](../../test/acceptance/features/bower.feature) |
| `chef` | [guide](../client-recipes/chef.md) | [client: Knife resolves cookbooks through a virtual over a remote and a nested virtual](../../test/acceptance/features/chef.feature) |
| `cocoapods` | [guide](../client-recipes/cocoapods.md) | [client: install pods through a virtual repository over a remote member and a nested local member](../../test/acceptance/features/cocoapods.feature) |
| `composer` | [guide](composer.md) | [client: Composer 2 installs a version only a later virtual member holds](../../test/acceptance/features/composer.feature) |
| `conan` | [guide](../client-recipes/conan.md) | [client: TestAccConanRemoteClient](../../test/server/artifacts/conan/conan_client_remote_test.go), [client: TestAccConanClients](../../test/server/artifacts/conan/conan_client_test.go) |
| `conda` | [guide](conda.md) | [client: micromamba installs through a virtual channel over a remote and a nested virtual](../../test/acceptance/features/conda.feature) |
| `cran` | [guide](../client-recipes/cran.md) | [client: R installs from a remote member through a nested CRAN virtual](../../test/acceptance/features/cran.feature) |
| `debian` | [guide](../client-recipes/debian.md) | [client: apt installs a remote member through a signed virtual repository offline](../../test/acceptance/features/debian.feature) |
| `docker` | [guide](registry-remote.md) | [client: crane pulls local and remote images by tag and digest through one virtual](../../test/acceptance/features/registry_virtual.feature), [client: crane pushes a derived image through the virtual using cached remote layers](../../test/acceptance/features/registry_virtual.feature) |
| `gems` | [guide](../client-recipes/gems.md) | [client: gem push and install through a virtual repository over local and remote members](../../test/acceptance/features/gems.feature) |
| `generic` | [guide](remote-repositories.md) | [client: curl and JFrog CLI fetch a cold file through a virtual and reuse the remote&#x27;s cache through an outage](../../test/acceptance/features/generic_remote.feature), [client: jf rt u through a virtual repository](../../test/acceptance/features/artifacts.feature) |
| `gitlfs` | [guide](../client-recipes/gitlfs.md) | [client: the pinned client pulls through a virtual repository over a local and a remote member](../../test/acceptance/features/gitlfs.feature), [client: the pinned client publishes through a virtual deploy target](../../test/acceptance/features/gitlfs.feature) |
| `go` | [guide](remote-repositories.md#go-virtual-repositories) | [client: A cold Go build resolves local and private remote modules through one virtual](../../test/acceptance/features/go.feature), [client: Publish a Go build through a virtual and resolve mixed dependencies through the same virtual](../../test/acceptance/features/go.feature) |
| `gradle` | [guide](maven-remote-repositories.md) | [client: Gradle resolves the exact bytes from a &lt;class&gt; repository](../../test/acceptance/features/system_recipes.feature), [client: Gradle publishes a module into a &lt;class&gt; repository](../../test/acceptance/features/system_recipes.feature) |
| `helm` | [guide](helm-remote.md) | [client: charts, member selection and publishing resolve with relative URLs &lt;relative&gt; and namespaces &lt;namespaces&gt;](../../test/acceptance/features/helm_virtual.feature) |
| `helmoci` | [guide](registry-remote.md) | [client: Helm pulls local and remote chart versions through a Helm OCI virtual](../../test/acceptance/features/registry_virtual.feature), [client: Helm pushes and pulls a chart through a Helm OCI virtual DDR](../../test/acceptance/features/registry_virtual.feature) |
| `hex` | [guide](../client-recipes/hex.md) | [client: Virtual repository falls through to a signed remote member](../../test/acceptance/features/hex.feature) |
| `huggingfaceml` | [guide](../client-recipes/huggingfaceml.md) | [client: Hub client resolves through a virtual member](../../test/acceptance/features/huggingface_xet.feature), [client: Hub client publishes through &lt;class&gt;](../../test/acceptance/features/huggingface.feature) |
| `ivy` | [guide](maven-remote-repositories.md) | [client: Ivy publishes into a mixed virtual deployment target and resolves with a fresh cache](../../test/acceptance/features/system_recipes.feature) |
| `luarocks` | [guide](../client-recipes/luarocks.md) | [client: virtual publication and mixed install with offline cached member](../../test/acceptance/features/luarocks_virtual.feature) |
| `maven` | [guide](maven-remote-repositories.md) | [client: &lt;maven&gt; deploys release and snapshots through a mixed virtual and resolves merged versions without repository escape](../../test/acceptance/features/maven.feature), [client: Gradle uses an Ivy artifact pattern to resolve a Maven publication through a different virtual layout](../../test/acceptance/features/system_recipes.feature) |
| `nix` | [guide](../client-recipes/nix.md) | [client: Nix verifies a remote cache and substitutes with the upstream offline](../../test/acceptance/features/nix.feature) |
| `npm` | [guide](npm-remote.md) | [client: &lt;npm&gt; installs a cold mixed dependency tree and verifies its virtual lockfile with npm ci](../../test/acceptance/features/npm_virtual.feature), [client: &lt;npm&gt; logs in, publishes, tags, deprecates and unpublishes through one virtual deployment target](../../test/acceptance/features/npm_virtual.feature), [client: &lt;npm&gt; audits both signing members and reports an unsigned virtual member](../../test/acceptance/features/npm_virtual.feature), [client: &lt;npm&gt; installs allowed tarball and GitHub dependencies only through the virtual cache remote](../../test/acceptance/features/npm_virtual.feature), [client: &lt;npm&gt; searches live local and cached remote members through a nested virtual](../../test/acceptance/features/npm_virtual.feature) |
| `nuget` | [guide](nuget-remote.md) | [client: &lt;dotnet&gt; restores a remote parent and its local transitive dependency](../../test/acceptance/features/nuget_virtual.feature), [client: &lt;dotnet&gt; restores local and private remote packages through a virtual v2 feed](../../test/acceptance/features/nuget_virtual.feature), [client: &lt;dotnet&gt; publishes package and symbols through a forced virtual and restores from empty caches](../../test/acceptance/features/nuget_virtual.feature) |
| `oci` | [guide](registry-remote.md) | [client: oras pulls both OCI members through a virtual repository](../../test/acceptance/features/registry_virtual.feature), [client: oras pushes and pulls an artifact through an OCI virtual DDR](../../test/acceptance/features/registry_virtual.feature) |
| `opkg` | [guide](../client-recipes/opkg.md) | [client: opkg installs from a local and a remote member of a virtual feed](../../test/acceptance/features/opkg.feature) |
| `p2` | [guide](../client-recipes/p2.md) | [client: a virtual P2 repository keeps external remote and local Generic members ordered](../../test/acceptance/features/p2.feature) |
| `pub` | [guide](../client-recipes/pub.md) | [client: virtual repository resolves ordered local and remote members](../../test/acceptance/features/pub.feature) |
| `puppet` | [guide](../client-recipes/puppet.md) | [client: Puppet resolves modules through a virtual over a remote and a nested virtual](../../test/acceptance/features/puppet.feature) |
| `pypi` | [guide](pypi-remote.md) | [client: pip and uv install wheel and sdist dependencies from virtual HTML and JSON indexes](../../test/acceptance/features/pypi_virtual.feature), [client: twine and uv publish a wheel and sdist to a virtual deployment target and install back](../../test/acceptance/features/pypi_virtual.feature) |
| `rpm` | [guide](../client-recipes/rpm.md) | [client: dnf installs through a virtual RPM repository backed by a remote and a local member](../../test/acceptance/features/rpm.feature) |
| `sbt` | [guide](maven-remote-repositories.md) | [client: sbt resolves mixed virtual origins, publishes into its deployment target and uses empty client caches](../../test/acceptance/features/sbt.feature) |
| `swift` | [guide](../client-recipes/swift.md) | [client: TestAccSwiftPMVirtualLocalRemoteAndPublish](../../internal/artifacts/ecosystem/swift/http/remote_test.go), [server: a Swift virtual repository resolves a release through a nested virtual member](../../test/acceptance/features/swift.feature) |
| `terraform` | [guide](terraform-remote-modules.md); [providers](terraform-remote-providers.md) | [client: a fresh init resolves rewritten HCL and JSON registry dependencies through one virtual](../../test/acceptance/features/terraform_remote.feature), [client: fresh init installs hosted and private remote providers through a nested virtual &lt;mode&gt;](../../test/acceptance/features/terraform_providers.feature) |

## Inference register

Each [I] above links an existing #216 comment. [The initial P10 entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6893506348)
groups all family tickets, including ADP-03/04, and is refined by the later
family comments linked below. A later correction wins over an earlier proposed
answer. The [P10 closeout comparison update](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6991066992)
corrects ADP-04's earlier cached-as-Chef proposal to describe the implemented
per-request Ansible merge. It also records cache-publication namespaces and
separates lookup, content-acquisition, validation-clock and origin-miss questions.
These are pending comparisons, not claimed live observations or settled modern
vendor responses.

| Subject | Existing #216 entry |
| --- | --- |
| PyPI upstream policy namespace and cache publication | [closeout comparison](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6991066992) |
| Lookup/configuration faults versus tolerated Maven member-content faults | [closeout comparison](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6991066992) |
| Existing-copy validation-clock SQL failures | [closeout comparison](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6991066992) |
| Expired-file origin 404, validation-clock advancement and native API-document boundary | [closeout comparison](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6991066992) |
| Ansible per-request merge: correction to the initial cached-as-Chef proposal | [closeout comparison](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6991066992) |
| Shared order, nesting, statuses, writes, storage and member rules | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6893506348) |
| Package root gates, Composer cache, pub/Gems cache choices | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6941939885) |
| Maven merge/Helm index | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6952456044) |
| Maven selection | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6954880433) |
| Maven POM cleanup | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6947354944) |
| Maven read layouts | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6974481140) |
| npm packuments | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6962897116) |
| npm writes | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6970499841) |
| npm search | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6972717811) |
| npm keys/signatures | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6977107477) |
| npm external dependencies | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6977564885) |
| PyPI read/write | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6955912161) |
| PyPI publication | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6959812942) |
| NuGet V3 | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6962735871) |
| NuGet V2 | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6969559757) |
| NuGet symbols/write/auth/reindex | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6973039901) |
| Helm namespaces/external charts | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6954421021) |
| Docker pull | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6955704582) |
| Docker push | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6960273085) |
| Docker listing | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6969664218) |
| Docker timestamp/referrers/foreign layers | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6972246717) |
| Go reads | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6946764751) |
| Go publication | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6948049781) |
| Terraform modules | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6971263611) |
| Terraform providers | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6974850025) |
| Git LFS/Hub | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6938349464) |
| Gems/LuaRocks/pub | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6939231324) |
| Bower/CocoaPods/Composer/Swift | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6939231698) |
| Alpine/RPM/Opkg | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6941502017) |
| Origin markers/Conan/Bazel/Hex/Nix | [entry](https://github.com/weaveplatform/artifactweave/issues/216#issuecomment-6946397513) |
