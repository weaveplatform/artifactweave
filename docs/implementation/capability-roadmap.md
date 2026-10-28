# Capability scope and roadmap

Reviewed **2026-10-22**, against merged P8 (`d0925b85`), the documentation audit
and current JFrog package-type guides.
This is a planning baseline. P0–P8 are implemented; the phases below remain planned
unless the [build status](plan.md#build-status) says otherwise.

## Scope rule

The maintainer's direction is that **only capabilities superseded by newer ones may
be excluded**. Every other identified capability has an implementation phase, including
the former "cut from v1" list. Decision [0042](../research/decisions/0042-capability-scope-and-supersession.md)
records this rule. Phase assignment commits the capability to the roadmap; it does not
claim runtime support or a delivery date.

Use these distinct states: implemented with evidence; partially implemented; planned
with a phase; blocked within an owning phase; or superseded with a replacement and
migration evidence. `stub` describes a current response, not a permanent scope choice.

## Coverage baseline

The audit uses the [previous scope inventory](../research/10-scope-and-tiers.md),
[open questions](../research/12-open-questions.md), all decision records,
[`pkg/repo`](../../pkg/repo/repo.go), the [format selector](../../internal/format/formats/formats.go),
the authored/generated APIs, handwritten routes and
[`api/parity-baseline.json`](../../api/parity-baseline.json).

Three kinds of gap must be counted separately:

- An authored schema can still dispatch to a generated 501 handler. Management
  definitions and stored configuration do not prove a workflow works.
- A product-prefix 501 handler can exist without that product's operation-level
  schema. An API absent from both schema and routing normally reaches 404.
- Native package protocols, format/class combinations and client workflows can be
  absent even when repository CRUD and the schema parity check pass.

The initial format audit counted 44 package types accepted by artifactweave. It was
incomplete against Artifactory's current documented catalog: LuaRocks, NVIDIA NIM,
Skills, Agent Plugins and Agent Packages were absent from both the code and the plan.
All five now belong to [P9b](plan.md#p9b-newly-identified-artifactory-formats).
Of the original 44, 29 additional non-generic types still use the generic fallback:
`releasebundles` belongs to P15 and the other 28 are assigned individually to
[P9c](plan.md#build-status), accelerating their former P29–P33 delivery slots.
Generic storage remains intentional; the internal build-info repository is accounted
for by P5, not an extra package protocol. This is an inventory finding, not a promise
that every class is valid for every type: each format phase must verify its supported
local/remote/virtual/federated combinations.

P9c [INV-01](package-format-matrix.md) adds a versioned format/class/client
inventory and unmapped-format CI guard. P20 still owns the wider operation-level
inventory across products, APIs and clients.

## Work already owned by P9–P19

| Capability | Owner and completion boundary |
|---|---|
| Upstream fetch/cache, credentials, metadata rewrite, remote version searches, Smart Remote synchronization/transitive lookup | P9, [epic #612](https://github.com/weaveplatform/artifactweave/issues/612); the [retrieval contract](remote-retrieval-contract.md) maps every field to an owner and tests. Generic remote retrieval, [npm remote retrieval](../operations/npm-remote.md), [Python remote retrieval](../operations/pypi-remote.md), [NuGet v2/v3 and symbol remote retrieval](../operations/nuget-remote.md), [classic Helm remote retrieval](../operations/helm-remote.md), the shared source/transport/security/cache foundation and [cache maintenance](remote-cache-maintenance.md) are implemented. NuGet remote/cache reindex (#700) rebuilds index rows only from verified cached archives. [Maven-family remote retrieval](../operations/maven-remote-repositories.md) is implemented with native Maven evidence; [Terraform module remote retrieval](../operations/terraform-remote-modules.md) and [provider remote retrieval](../operations/terraform-remote-providers.md) are implemented with pinned native cold/offline evidence; [Smart Remote synchronization](smart-remote.md) is implemented with current-wire #216 and external-peer P37 qualification retained; [remote folder browsing](../operations/remote-repositories.md#remote-folder-browsing) is implemented with bounded HTML/Smart listings; [transitive AQL](../operations/transitive-aql.md) is implemented with direct/virtual member bounds and native CLI evidence. [#213 upstream search](../operations/upstream-search.md) discovers authoritative metadata/listings without binary prefetch, including permission-safe differing layouts through #672. The [P9 sign-off ledger](../testing/phase9-signoff.md) records the passing all-15 public conformance run (#638) and exact-head combined-tree exit through #639 after the provider compatibility fix (#701) |
| LuaRocks, NVIDIA NIM, Skills, Agent Plugins and Agent Packages | P9b implements repository management and native protocols for every documented supported class. These five were missing from the original 44-type inventory and are separate from P9c's 28 generic-fallback formats |
| Virtual resolution and metadata merging | P10; native clients resolve local and remote members with permission filtering |
| Projects, webhook delivery, package cleanup, single push/pull replication | P11; project-scoped remote resolution (#487) spans P9's source model and P11's sharing/authorization, with closure in P11 |
| Full descriptor, system configuration, LDAP, anonymous access and configured mail notifications | P12; #303 includes native anonymous reads with an explicit permission grant; mail settings must result in delivery. Proxy obligations include NTLM/domain/ntHost, `redirectedToHosts` credential behavior and broader proxy compatibility; #618 supports HTTP(S) Basic proxies and requires CONNECT for DNS-named HTTP sources |
| Repository and Access Federation, topology | P13/P14 for artifactweave peers; external interoperability is additionally owned by P37/P40 |
| Release bundles v1 and Distribution | P15, including the bundle-delete `archive` flag contract; accepting and ignoring it is not completion. Coordinate archive effects with P16; v2 compatibility and distribution remain explicitly in P24 |
| Cold storage and archive/restore policies | P16; accepting `cold_blob` settings alone does not satisfy it |
| Workers | P17; scheduled/event execution, language/runtime contract and sandbox behavior, not configuration CRUD alone; security/runtime event producers integrate in P27 |
| First delivery sequence: hardening and live comparison | P18/P19; does not imply completion of the newly scheduled P20–P41 capabilities |

## P20 onward: capability ownership

The [phase tasks and exits](plan.md#p20-capability-inventory-and-contract-coverage)
define delivery. The table is the scope checklist; related work stays visible even
when its first implementation is in an earlier phase.

| Capability / gap | Current boundary | Owning phase |
|---|---|---|
| Complete API/client/format inventory; schema and runtime coverage tracking | Existing parity baseline is a schema comparison, not a capability inventory | P20 |
| Browser approval, npm web login, Terraform `login.v1` and PKCE (#262) | Legacy npm and pasted-token Terraform login work | P21 |
| SAML, human OIDC/OAuth, Crowd, HTTP-SSO, session/MFA and trusted-proxy policy | Local identities and CI OIDC exchange work; external human identity workflows remain incomplete | P21 |
| SCIM user/group provisioning, lifecycle, deactivation and session/token revocation | Generated endpoints remain unimplemented; SCIM is useful independently of a web UI | P22 |
| Evidence, attestations, provenance, signature verification and signing-key APIs | Key storage and some package signing exist; full Evidence service does not | P23 |
| Lifecycle, stages, release bundles v2, promotion, distribution and bundle cleanup | Product prefix/selected generated stubs; stages/lifecycle Access resources lack routes | P24 |
| AppTrust applications/versions/package binding, Application Admin roles, Unified Policy, approvals and evaluations | Prefix stub or absent contract; no working governance engine | P25 |
| Xray scanning, SBOMs, vulnerability/license intelligence, policies/watches/violations, reports; Catalog and OneModel GraphQL gateway/subgraphs | Xray prefix stub; Catalog/OneModel contract needs research and implementation | P26 |
| Advanced Security, contextual analysis, Runtime/workload ingestion and Worker events, IDE/Frogbot integration, Curation, PTC/package rerouting and enforcement | No working analysis or admission service; `AFTER_WORKLOAD_STATE_CHANGE[_REQUEST]` producers remain absent | P27 |
| npm ECDSA signatures (#362), audit proxy/API, NuGet upstream vulnerability-resource proxying (P27 security-data integration), Go sumdb and VCS-backed module remotes | Explicit omissions from the first eight-format implementation | P28 |
| Remaining NuGet v2 query/client behavior, Chocolatey/PowerShell/nuget.exe/JFrog acceptance, private-index PyPI XML-RPC search and legacy `/packages/<path>` alias | Bounded v2 and modern native clients work; broader behavior and legacy-route replacement are not proved | P28 |
| NuGet non-normalized mode/migration, SemVer2 and authentication settings; Helm global enforcement; Docker list-overwrite cleanup/digest variants; global excludes and copy timestamps | Present settings/behavior are narrower; P12 owns descriptor plumbing and P36 migration support | P28 |
| npm staged publication, token/profile/legacy-search contracts; provenance and trusted publishing | Missing routes or unverified target behavior; P21/P23 provide identity/evidence foundations | P28 |
| Additional Terraform module source getters, HTTP vanity indirection, checksum-file URLs and Git snapshot semantics | HTTP archives and bounded GitHub snapshots are #631; precise additional contract and native qualification remain [#668](https://github.com/weaveplatform/artifactweave/issues/668) | P28 |
| Terraform state-version outputs and registry details/latest-download/search/listing; TFE cloud/execution/runs/configuration/variable/project/tag contracts | State storage/local execution work; broader API applicability and execution-service dependencies require explicit research | P28 |
| Docker port-per-repository routing; remaining search/AQL/build-info gaps, Builds Diff, archive-content search, build artifact archives, Maven/license search indexes | Some explicit refusals, missing client coverage and unimplemented management operations | P28 |
| Alpine, Debian, RPM, Opkg | Generic fallback; ecosystem metadata/protocols absent | P9c |
| Cargo, Composer, Gems, Pub, Swift, Hex, Bower | Generic fallback; ecosystem metadata/protocols absent | P9c |
| Conan, Bazel modules, P2, JetBrains plugins, AI editor extensions | Generic fallback; ecosystem metadata/protocols absent | P9c |
| CocoaPods | Native CDN metadata and HTTP/Git source archives, recursive pinned submodules, cold/offline cache and L/R/V/one-node F client proof; [client guide](../testing/cocoapods.md) | P9c #771 |
| Conda, CRAN, Hugging Face ML, Machine Learning and ML roles/actions | Generic fallback; ecosystem metadata/protocols and model-service authorization absent | P9c |
| Ansible, Chef, Puppet, Vagrant, VCS, Git LFS, Nix | Generic fallback; ecosystem metadata/protocols absent | P9c |
| MySQL and database portability | SQLite/PostgreSQL implemented; MySQL unimplemented | P34 |
| High availability, shared sessions, distributed scheduling, compute-hosting references, rolling upgrades and failover | Single-process deployment contract; storage/CDN deployment references already exist | P35 |
| Artifactory/Access import/export and bootstrap migration, XML descriptor POST, scheduled backup and restore, checksum migration | Manual coordinated backup guidance; import/export and vendor bootstrap compatibility remain incomplete | P36 |
| Multi-push/event replication, checksum replication, newly added types, cross-vendor/Edge/Access Federation and separate cold-instance interoperability | P11/P13–P16 own initial replication/peer/archive work; broader behavior remains unimplemented | P37 |
| AWS IAM mapping, Vault/secret backends, log shipping/cloud logs, support bundles, tasks/info/debug/lock APIs, queue administration | Stored or generated shapes do not supply operational behavior | P38 |
| Standalone signed-URL/key APIs, application metrics, CA/root-certificate and encryption-key administration | CDN redirects and existing key rotation work; these separate APIs remain gaps | P38 |
| Azure hierarchical-namespace rename optimization; external KMS master-key references | Explicit deferred storage/integration work; requires measured provider and failure-path evidence | P38 |
| Web console: artifact browsing/search, configuration, identity, governance and operational workflows | No general UI; P21 supplies only login/approval foundations | P39 |
| Mission Control/estate management, MyJFrog/account APIs, JFConnect registration/entitlement, SaaS bridges and service pairing | Most absent from schema or routing; some compatibility sinks already work | P40 |
| Pipelines, Integration Service, user/plugin extension capability, migration and supported CI/CD integration contracts | Retirement or a possible Workers replacement does not itself prove equivalent behavior | P40 |
| Expanded compatibility, migration, security, performance and disaster-recovery qualification | P18/P19 cover the earlier sequence only | P41 |

### Repository type and class checklist

This table accounts for the original 44 types accepted by
[`AllowedPackageTypes`](../../pkg/repo/packagetypes.go). **L/R/V/F mean configuration
acceptance** for local/remote/virtual/federated repositories; they do not claim native
protocol support, upstream resolution or peer convergence. P20 and each format phase
revalidate this source-union matrix against the target contract. A class the target
itself rejects is a compatibility rule, not a missing product feature.

| Public type(s) | Accepted classes | Native behavior owner |
|---|---|---|
| `docker`, `oci`, `helmoci`, `go`, `helm`, `maven`, `gradle`, `ivy`, `sbt`, `npm`, `nuget`, `pypi`, `terraform` | L/R/V/F | Local protocols P6–P8; remote P9, virtual P10, federation P13, remaining gaps P28 |
| `terraformbackend` | L | P8 state service; remaining applicable APIs P28 |
| `generic` | L/R/V/F | P3 storage and P9 HTTP remote retrieval implemented; virtual fetching P10, federation P13 |
| `releasebundles` | L/F | P15; v2 workflows P24 |
| `alpine` | L/R/V/F | P9c |
| `debian` | L/R/V/F | P9c |
| `rpm` | L/R/V/F | P9c |
| `opkg` | L/R/V/F | P9c |
| `cargo` | L/R/F | P9c; verify the documented federation workaround separately |
| `composer` | L/R/V/F | P9c |
| `gems` | L/R/V/F | P9c |
| `pub` | L/R/V/F | P9c |
| `swift` | L/R/V/F | P9c |
| `hex` | L/R/V | P9c |
| `bower` | L/R/V/F | P9c |
| `conan` | L/R/V/F | P9c |
| `cocoapods` | L/R/V/F | P9c |
| `bazelmodules` | L/R/V/F | P9c |
| `p2` | R/V | P9c |
| `jetbrainsplugins` | R | P9c |
| `aieditorextensions` | R | P9c |
| `conda` | L/R/V/F | P9c |
| `cran` | L/R/V/F | P9c |
| `huggingfaceml` | L/R/V/F | P9c |
| `machinelearning` | L/F | P9c |
| `ansible` | L/R/V/F | P9c |
| `chef` | L/R/V/F | P9c |
| `puppet` | L/R/V/F | P9c |
| `vagrant` | L/F | P9c |
| `vcs` | R | P9c |
| `gitlfs` | L/R/V/F | P9c |
| `nix` | L/R/V/F | P9c |

P9b registers the five documented types below for their target classes. LuaRocks,
Skills, Agent Plugins and Agent Packages now have the native paths listed below.
NVIDIA NIM resolves documented NGC metadata/model URLs through its remote and
uses the separate Docker remote for images; [#693](https://github.com/weaveplatform/artifactweave/issues/693)
retains the final merged-head qualification evidence.
The class matrix and three repository JSON keys remain subject to target-release
verification. P9b owns native behavior in federated repositories;
P13 owns peer convergence. The [P9b contract report](../research/33-phase9b-formats.md)
records exact native prefixes, provisional repository JSON keys, the SaaS/self-managed
version split and the P19 captures required before claiming vendor parity.
The [P9b qualification ledger](../testing/phase9b-signoff.md) maps each class
to its native client, controlled fault evidence and final gate.

| New type | Documented class target | Native status and handoff |
|---|---|---|
| [LuaRocks](https://docs.jfrog.com/artifactory/docs/luarocks-repositories) | L/R/V/F | Local, remote, virtual and one-node federated native paths implemented; peer convergence P13 |
| [NVIDIA NIM](https://docs.jfrog.com/artifactory/docs/nvidia-nim-repositories) | R | Documented NGC metadata/model reads and paired Docker pull implemented; exact Artifactory runtime wire remains inferred pending #216 |
| [Skills](https://docs.jfrog.com/artifactory/docs/skills-repositories) | L | Publish, discovery, search and CLI install/update implemented; real scanning P26 |
| [Agent Plugins](https://docs.jfrog.com/artifactory/docs/agent-plugins-repositories) | L | ZIP index, marketplaces and CLI publish/install/update implemented; signed Evidence P23 |
| [Agent Packages](https://docs.jfrog.com/artifactory/docs/agent-packages-repositories) | L/F | APM v1 local and one-node federated native paths implemented; peer convergence P13 |

### Existing issues and client blockers

| Issue | Disposition |
|---|---|
| #213 | P9 upstream version search exit |
| #672 | P9 permission-safe differing-layout discovery prerequisite for #213; canonical authorization and layout/source/credential mutation fences, with no binary prefetch |
| #487 | Design in P9; complete project sharing and security proof in P11 |
| #262 | P21 shared browser approval/native login exit |
| #303 | P12 anonymous-access configuration and native acceptance |
| #362 | P28 npm signing and `npm audit signatures` proof |
| #523 | P28 client-compatibility owner; retry when the upstream extractor fix is released, without waiting unnecessarily for that phase |
| #216 | P19 first live comparison; P20 onward adds new capture work to its owning phases and P41 verifies the expanded result |

Applicability research is bounded work, not permission to erase a row. For example,
P28 must distinguish Artifactory's Terraform state/output contract from a complete
Terraform Cloud execution platform, and identify the execution-service design required
by any retained runs/cloud capability. Its result and any receiving phase must be
recorded before closure; local `remote`-backend execution is not proof of remote execution.

Architecture alternatives are decisions rather than user features: P20 owns the
OpenAPI 3.1 migration decision and generator/client/parity proof; P35 owns the internal
RPC/protobuf decision if multi-node consumers require it. Neither can indefinitely hide
a missing public contract behind an undecided implementation technology.

Pinned-client limitations stay visible: informational Gradle, JFrog Poetry install
rewriting, Twine retry behavior, and untested legacy NuGet tools are P28 acceptance
work. Where a client intentionally refuses a feature, the exit proves the supported
workflow and records the client constraint; it does not pretend the server can change
the client. A tested, bounded adapter or migration can satisfy a superseded protocol's
use case only with explicit evidence.

## Supersession register

Deprecation is a research input, not automatic permission to drop a feature. These
entries identify the narrow interface being replaced, not an entire product family.
P20 validates this register against the then-current sources and consumers.

| Older interface | Replacement and evidence | Planned treatment |
|---|---|---|
| Creation of legacy API keys | Reference/identity tokens; [JFrog API key guidance](https://docs.jfrog.com/user-management/docs/api-key) explicitly names the replacement | Keep existing token support; P21/P36 prove authentication and migration paths, without restoring a separate permanent-key issuer |
| Docker Registry HTTP API V1 | Registry V2/OCI; [Docker engine deprecations](https://docs.docker.com/engine/deprecated/#interacting-with-v1-registries) records V1 client removal | V2/OCI remains the runtime target; P28 records old-client/image migration requirements |
| Legacy npm PGP registry signatures | ECDSA registry signatures; [npm's replacement guidance](https://docs.npmjs.com/about-registry-signatures/) | P28 implements ECDSA and native verification; do not create a parallel obsolete signing mechanism |
| Legacy Hugging Face storage layout | Current Machine Learning layout; [JFrog migration guidance](https://docs.jfrog.com/artifactory/docs/hugging-face-repositories) | P9c #789 owns the format migration; P36 owns broader estate migration. Retire only the replaced layout |
| NuGet `GetUpdates` functions; old generic-repository Terraform state backend | Search/current NuGet protocol and the dedicated Terraform remote backend respectively; [JFrog deprecations](https://docs.jfrog.com/releases/docs/artifactory-deprecations) | P28 proves migration; NuGet v2 and Terraform state hosting remain in scope |
| Helm 2 client generation | Helm 3 migration; [Helm's guidance](https://helm.sh/blog/helm-2-becomes-unsupported/) | Qualify supported Helm generations in P28/P41; classic Helm repositories remain supported alongside OCI |
| Raw security XML descriptor and old Access context | Security REST/Access bootstrap and `/access`; [JFrog release notes](https://docs.jfrog.com/releases/do/docs/artifactory-self-managed-releases) | P12/P21/P36 complete the replacement/migration. Full system XML descriptor POST is a different capability and remains P36 |
| RLM product trajectory | [JFrog recommends AppTrust](https://docs.jfrog.com/releases/do/docs/release-lifecycle-management-deprecation-end-of-life); existing RLM support continues toward its stated EOL | P23–P25 share evidence, release and governance foundations. Lifecycle and release bundles v2 remain explicitly in scope as requested, including compatibility and migration |
| Pipelines and other retired estate services without a proven complete replacement | [Pipelines EOL guidance](https://docs.jfrog.com/releases/docs/pipeline-deprecation-end-of-life) asks customers to select another CI/CD solution | P40 must name and prove the replacement/migration or implement the missing capability; retirement alone does not close the row |

SAML is not superseded merely because OIDC exists. SCIM provisioning is not replaced
by authentication. A package format is not superseded merely because another format
can store similar files. PyPI.org removing an endpoint does not establish that a
private package index's corresponding capability has disappeared.

## Hosted and proprietary service boundaries

P40 must resolve whether each operation acts on artifactweave's own estate, an
authorized external JFrog account, or a compatibility representation. Document the
actual service, credentials, entitlement and observable effect. Never return a fake
success for registration, billing, licence issuance, network provisioning or an
external service action. An integration unavailable to local tests needs controlled
acceptance evidence and remains blocked until it exists.

The same rule applies to P26/P27 intelligence and scanning: select explicit engines
and permitted data sources, define update/freshness and disconnected behavior, and
test analysis outcomes. A matching API envelope is not equivalent to scanning.

## Planning and closure controls

- Split each phase into research/contracts, model/migrations, service/API work,
  client/provider acceptance and documentation/sign-off PRs. Each additional format
  gets its own implementation PR sequence; a generic upload is not its exit proof.
- P20 records every discovered operation and format/class combination under a stable
  capability identifier, with source, current response, owner, dependency and test.
  New upstream surface must acquire an owner before a parity exception is accepted.
- P20 adds the automated unmapped-gap check. Thereafter the phase exit requires that
  the operation inventory, runtime matrix and docs agree, including explicit failure
  semantics while dependent phases remain incomplete.
- P41 checks the whole expanded inventory, including migrations for superseded
  interfaces. No capability may disappear through an unowned "later", a green schema
  parity run, a successful repository CRUD test or a product rename.
