# P10 Terraform provider virtual repositories

Issue [#1564](https://github.com/weaveplatform/artifactweave/issues/1564), researched
2026-11-18. **[D]** denotes documentation, **[RN]** release notes, **[J]** public
Jira evidence, **[S]** inspected source, and **[I]** an unsettled inference for
[#216/P19](https://github.com/weaveplatform/artifactweave/issues/216). No trial or
live capture was used. Existing context is [research 31](31-terraform.md),
[decision 0040](decisions/0040-terraform.md), [virtual rules](decisions/0008-virtual-repositories.md)
and [TFM-01 research](p10-terraform-modules.md).

## Current target and source boundary

- **[D/RN]** The target remains latest documented 7.x. JFrog's Terraform page was
  updated 2026-11-05; the [self-managed release notes](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases)
  reach **7.173.21**, released 17 November 2026. Newer detailed bug lists require
  authentication. The 7.125.4 notes establish provider origin-registry support
  for local, virtual and federated repositories alongside network mirrors.
  The older open [RTFACT-31167](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-31167)
  request does not override that released contract.
- **[S]** Rechecked the official [OSS 7.161.26 archive](https://releases.jfrog.io/artifactory/bintray-artifactory/org/artifactory/oss/jfrog-artifactory-oss/7.161.26/jfrog-artifactory-oss-7.161.26-sources.tar.gz):
  SHA-256 `b867422f3137c542b402c38a2ca2584396ef431b691ca70f038ff5cc3768802c`.
  `backend/core/.../ph/PackageRepoServiceImpl.java:319` delegates member assembly
  to the shared virtual resolver; `repo/virtual/VirtualRepositoryConfigResolverImpl.java:60`
  and `VirtualRepositoriesResolver.java:103` establish flattening and effective
  priority/local/cache/remote ordering. Reuse ENG-08's implementation of those rules.
- **[S]** This distribution contains configuration, UI and addon interfaces,
  **not the proprietary Terraform registry/mirror implementation**.
  `base/common/.../addon/terraform/TerraformAddon.java:10` has no-op defaults.
  `web/rest-ui/.../setmeup/GetTerraformConfigSnippetService.java:41` chooses the
  origin-registry snippet for a PROVIDER local/federated repository with
  `primaryKeyPairRef`; without it, the snippet is for a mirror. This establishes
  the primary-key association, not virtual key selection, secondary-key exposure
  or package-verification behavior. Older OSS defaults cannot supersede current
  documented contracts.

## Routes and wire contracts

**[D]** JFrog documents virtual aggregation of local/remote modules and providers,
local MODULE/PROVIDER selection, a GPG pair for local PROVIDER repositories,
and this provider address and mirror base:

| Use | Form |
|---|---|
| Origin address | `<host>/<virtual>-rt-ns-<namespace>/<type>` |
| Mirror base | `https://<host>/artifactory/api/terraform/<virtual>/providers/` |

Source: [Terraform repositories](https://docs.jfrog.com/artifactory/docs/terraform-opentofu-and-terraform-backend-repositories).

**[D]** HashiCorp's [registry protocol](https://developer.hashicorp.com/terraform/internals/provider-registry-protocol)
uses `providers.v1` discovery. Its relative endpoints are
`<namespace>/<type>/versions` and `<namespace>/<type>/<version>/download/<os>/<arch>`.
Versions are unique records with protocols and optional platforms. Package
metadata gives `os`, `arch`, `filename`, `protocols`, `shasum`, `download_url`,
`shasums_url`, `shasums_signature_url` and `signing_keys.gpg_public_keys`.
The signature authenticates the checksum document with an advertised key.

**[I]** The existing global route
`/artifactory/api/terraform/v1/providers/<virtual>-rt-ns-<namespace>/<type>/...`
and scoped route `<virtual>/v1/providers/<namespace>/<type>/...` retain decision
0040's routing. JFrog's [Smart Remote KB](https://jfrog.com/help/r/artifactory-how-to-configure-a-smart-remote-repository-for-terraform-in-jfrog-artifactory)
documents the scoped registry API base, but does not capture the exact discovery
value or every scoped origin-registry operation.

**[D]** The [mirror protocol](https://developer.hashicorp.com/terraform/internals/provider-network-mirror-protocol)
uses no discovery: `<hostname>/<namespace>/<type>/index.json` returns
`{"versions":{"1.0.0":{}}}`, and `<version>.json` returns
`{"archives":{"linux_amd64":{"url":"...","hashes":["h1:..."]}}}`.
Hashes are optional; there are no GPG/signature fields. Relative archive URLs
resolve against the version document. The hostname identifies the original
provider, not an upstream to contact. Metadata uses mirror-host credentials;
archive requests carry no registry credential.

**[S]** Terraform [1.17.1 hash code](https://github.com/hashicorp/terraform/blob/v1.17.1/internal/getproviders/hash.go)
distinguishes `h1:` content hashes from `zh:` ZIP-byte SHA-256 hashes. Preserve the
selected member's hash behavior: local/registry-derived `h1:` and an existing raw
Smart Remote mirror's supported hashes are not interchangeable. The
[remote-provider contract](../operations/terraform-remote-providers.md) already
preserves raw mirror hashes and computes `h1:` for a hashless source; that does not
establish a signer.

## Exact ownership choices still inferred

Neither the current docs, reviewed [known issues](https://docs.jfrog.com/releases/docs/artifactory-known-issues)
and release notes, public Jira nor OSS
exposes Terraform's duplicate-ownership algorithm. Record these three choices
explicitly as **[I]**, rather than as observed Artifactory behavior:

| Operation | Inferred TFM-02 behavior |
|---|---|
| Registry `/versions` | Union eligible versions; a duplicate retains the first effective member's whole record, including protocols and platforms. |
| Registry exact download | Independently choose the first eligible member holding both the requested version and platform; all bundle fields and keys come from that member. |
| Mirror | `index.json` unions versions; `<version>.json` retains the first eligible version owner's complete archive map and hashes, without filling platforms from later members. |

For example, if A has version 1.0.0 for Linux and B has it for Darwin, the registry
listing and mirror version document retain A's platforms; an exact Darwin registry
download can select B. This distinction needs a dedicated complementary-platform
case. Disjoint versions, conflicting bytes/keys/hashes, effective order, nesting
and unreadable/blackout fall-through need separate cases.

**[I]** A selected local/federated member supplies its primary/secondary public
keys; a remote supplies its existing upstream keys. Never combine one member's
ZIP with another's SHA256SUMS, signature, protocol metadata or signing identity.
URL rewriting preserves the member's original ZIP/checksum/signature bytes and
validation rules; the virtual does not sign a replacement checksum document.
Exact secondary-key exposure and unchanged-byte behavior through a JFrog virtual
remain unobserved. The protocol's required consistency does not prove JFrog's
selection algorithm.

## Read policy, source authority and signed delivery

**[D/J]** Metadata and file paths have separate policy evidence.
[Current JFrog examples](https://docs.jfrog.com/artifactory/docs/terraform-opentofu-and-terraform-backend-repositories)
include `**/.terraform/providers/hashicorp/<type>/**` for metadata,
`**/terraform-provider-<type>/**` for archives and
`**/external/github.com/<owner>/terraform-provider-<type>/**` externally.
[RTFACT-30802](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-30802) explains
that remote metadata requires `.terraform/**` in an include allow list;
[RTFACT-31026](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-31026) shows
`.terraform/providers/hashicorp/kubernetes/versions`. These facts do not settle
virtual listing Read paths or grant authority to download any listed file.

**[D/I]** [Virtual repository documentation](https://docs.jfrog.com/artifactory/docs/virtual-repositories)
applies patterns to administrators and explicitly exempts Terraform from
`artifactoryRequestsCanRetrieveRemoteArtifacts` (R7). Apply the virtual's patterns,
then current effective-member Read/pattern/download policy; MODULE members,
unreadable and blacked-out members fall through under TFM-02's R3 contract.
Exact Terraform-specific checks/statuses remain **[I]**. The general
[7.173.8](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases#artifactory-71738-self-managed)
anonymous 401 challenge is not evidence that a refused constituent terminates a
virtual walk; direct local/remote status contracts remain separate.

**[RN/KB]** Retain the remote's own source and credential authority.
[7.161.15 and 7.125.10](https://docs.jfrog.com/releases/docs/artifactory-self-managed-releases)
document external-URL hardening and RTDEV-69690's fix for downloads originating
locally instead of through the configured upstream. The
[Smart Remote KB](https://jfrog.com/help/r/artifactory-how-to-configure-a-smart-remote-repository-for-terraform-in-jfrog-artifactory) requires
matching upstream metadata bases and describes `/external/` to `/transitive/`
rewriting. Rendering a virtual client URL grants neither another upstream origin
nor credential forwarding to it.

**[J]** [RTFACT-29753](https://jfrog.atlassian.net/rest/api/2/issue/RTFACT-29753),
freshly fetched, reproduces provider mirror index/version routes and an absolute
`/api/terraform/<repo>/providers/<archive-path>?sig=<JWT>` link. Its token has HS256,
`iss`, SHA-256 `sum`, `internal_repo`, `iat=1687084562` and `exp=1687084652`:
**90 seconds**. The reported wrong repository name is a defect, not intended
routing; the KB records its fix after 7.63.12.

**[I]** Virtual links retain that lifetime, name the requested virtual and carry
the selected member. Metadata construction revalidates the incoming principal and scope before
signing. Delivery rechecks current membership, signer Read/patterns and exact
content digest. Hosted signed links retain TFM-01's subject/current-Read contract;
remote signed links additionally restore their credential/source claims and
revalidate source/metadata identity. A valid metadata read
does not authorize a later file read. The exact virtual JWT claims, membership
revocation behavior and refusal text remain #216/P19 questions; OSS cannot prove
them. This extends TFM-01's member claim and preserves each remote's existing
provider claim rather than creating new authority.

## Related Terraform fixes and local qualification

The implementation probes found two existing signed-delivery faults within the
Terraform kind. A valid direct remote module/provider capability masked a genuine
source-store failure as an invalid capability (403); a virtual signed module also
masked user/permission-store failures, unlike the direct hosted control (500).
Typed storage faults now retain 500. A remote-issued virtual module capability
could also be replayed after legacy direct-store replacement of its member by a
hosted MODULE with identical bytes: remote source claims now refuse that class
incarnation change. Normal API repository-class mutation remains prohibited.

Virtual capability refusals use the required exact 403 profile for current
policy/Read denial or missing content. That classification is deliberately
narrower than member-discovery pass-over: upstream outages, timeouts and
superseded-source retries retain their error/retry semantics. Metadata creation
refreshes the incoming principal rather than reconstructing only its username,
so a scoped or newly revoked token cannot issue broader hosted capabilities.

A focused virtual-pattern regression also showed optional manifest protocols
reappearing in the exact descriptor after being filtered from its version list.
Virtual descriptors now use the same optional-manifest visibility and include
an admitted manifest in the final metadata fence; direct lookup behavior stays
unchanged.

Local native qualification uses the checksum-verified Terraform 1.17.1
**linux_arm64** client and matching null 3.3.2 archive. The CI lane keeps its pinned
linux_amd64 inputs. Two initial setup failures are retained separately: an old
amd64 client reused in the arm64 runner selected a different platform from the
fixture, and the workspace's long temporary path exceeded the provider plugin's
Unix socket limit. The successful run uses a short disposable-runner
`PLUGIN_UNIX_SOCKET_DIR`, the supported
[go-plugin socket setting](https://github.com/hashicorp/go-plugin/blob/v1.6.0/server.go),
while all Go builds, scenario files and client caches retain their isolated
workspace paths. Neither setup observation establishes a product defect.
