# Option 5: Modular monolith, detailed layout

Detailed repository layout for option 5 of [ARCHITECTURE-OPTIONS.md](../ARCHITECTURE-OPTIONS.md):
bounded contexts as modules, ports and adapters inside the larger ones, one
binary, one database. Every current package is mapped; roadmap packages are
marked with the phase that creates them.

Conventions in the trees:

- `← internal/service/item` names where the code lives today.
- `(P17)` marks something a later phase creates.
- A line with neither is new structure for today's code.

## Summary

The tree is organised by business domain first. Each domain is a module
under `internal/<module>/` that owns its model, its tables, its HTTP
surface and its published interface in `internal/<module>/api/`. Modules talk
to each other only through those `api/` packages, synchronously for queries
and authorisation, and asynchronously through events appended to a
transactional outbox in the producer's own transaction. Module boundaries are
drawn by consistency: everything a deploy must change atomically (item, blob
reference, format hooks, package index, build record, registry session) is in
the `artifacts` module, which is itself laid out as a hexagon with one slice
per package type. Shared infrastructure with no business rules lives in
`platform/`; a handful of value types every module uses live in `kernel/`.

Dependency rules:

1. A package under `internal/<module>/` imports only `internal/platform/...`,
   `internal/kernel/...`, `pkg/...`, its own module, and other modules'
   `internal/<other>/api/...`. An `api/` package itself imports only
   `internal/kernel/...`, `internal/platform/outbox` (for the event envelope)
   and the standard library, so `api/` packages can never form a cycle.
2. Inside a module, `internal/<module>/core/...` never imports
   `internal/<module>/adapters/...` or `internal/<module>/ecosystem/*/http`.
3. `internal/gateway/...` imports only `internal/<module>/api/...`,
   `internal/kernel/...` and `internal/platform/httpkit`, and declares no
   migrations.
4. `internal/platform/...` and `internal/kernel/...` import no
   `internal/<module>/...` and no `internal/gateway/...`.
5. Only `internal/app/...` and `cmd/...` import a module's root package
   (`internal/artifacts`, the constructor) or another module's adapters.

Within `artifacts`, one extra rule keeps the ecosystem slices honest: nothing
outside `internal/artifacts/ecosystem/<type>/http` and `internal/app` imports
that `http` package.

## Repository root

```text
artifactweave/
├── cmd/
│   └── artifactweave/          unchanged role: cobra; adds hidden worker-host (P17)
├── internal/                   REORGANISED: platform/ kernel/ <module>/ gateway/ app/ layout/ devtools/
├── pkg/                        SHRUNK: client/<spec>/ and containertest/ only
├── api/                        unchanged layout; oapi-server-*.yaml outputs move to internal/platform/gen
│   └── src/<service>/          adds src/peer (P13), src/evidence (P23), src/apptrust (P25), ...
├── test/
│   ├── acceptance/             unchanged
│   ├── server/                 regrouped: server/<module>/ and server/artifacts/<type>/
│   ├── platform/ oci/ terraform/   unchanged
│   └── peers/                  (P13) multi-instance scenarios for the acceptance-peers job
├── tools/                      unchanged; citestselection ownership keys follow the new paths
├── docs/ deploy/               unchanged; docs/implementation/repo-layout.md rewritten per module
├── Makefile                    generate target writes to internal/platform/gen; build unchanged
├── .golangci.yml               depguard rules mirror the five dependency rules (belt and braces)
├── .testcoverage.yml           per-package floor unchanged; exclusions follow the new gen and devtools paths
└── go.mod                      one module, unchanged
```

What changes at the root and why:

- `pkg/` keeps only what another repository would import: the generated
  clients and the container-test gate. Store, blob, CDN, AQL, layout, repo and
  types become internal because, in this option, each has one owning module or
  is platform infrastructure, and publishing them would freeze module
  internals as a public API.
- `internal/` gains one directory per module. The set grows phase by phase.
- `api/` is unchanged as the source of truth for specifications. Only the
  generated server output path moves.

## Full package tree

Module status at the end of P9c:

| Module | Status at P9c | Created or completed by |
| --- | --- | --- |
| platform, kernel | full | P0 onwards |
| artifacts | full | P3 to P10, P28 |
| access | full for local identity, tokens, permissions, roles, OIDC exchange | P11 projects, P12 LDAP, P21, P22 |
| configuration | partial: YAML descriptor apply, repository bootstrap, system endpoints | P12, P36 |
| notifications | storage skeleton only (webhook tables and store port exist since P2) | P11 |
| retention | storage skeleton only (cleanup and archive tables) | P11, P16 |
| federation | storage skeleton only (instance, peers, outbox, federation, platform, replication tables) | P11, P13, P14, P37 |
| releases | storage skeleton only (bundles, distribution tables) | P15, P24 |
| workers | storage skeleton only (workers table) | P17 |
| evidence, governance, security, operations, estate | absent | P23, P25, P26 and P27, P18 and P38, P40 |
| gateway/onemodel, gateway/console | absent | P26, P39 |

A storage skeleton holds only `core/port/store.go`, `adapters/sql/`,
`adapters/inmem/` and `storetest/`, because P1b and P2 already modelled and
persisted those entities. Its `api/`, `core/` services and HTTP adapters
arrive with the phase that implements the domain.

```text
internal/
├── app/                                   composition root            ← internal/app, internal/config
│   ├── config/                            strict YAML, flag > env > file; one Settings field per module
│   │                                                                   ← internal/config
│   └── (files listed under "Composition root")
├── layout/                                dependency-rule test         ← internal/layout
├── devtools/                              libraries behind tools/ commands; not part of the server
│   ├── openapibundle/                                                  ← internal/openapibundle
│   ├── stubgen/                                                        ← internal/stubgen
│   └── specoracle/                                                     ← internal/specoracle
│
├── kernel/                                value types every module uses; no behaviour beyond parsing
│   ├── kernel.go                          RepoKey, RepoPath, PackageType, ProjectKey
│   ├── principal.go                       Principal (who is calling)   ← pkg/types/principal.go, internal/auth/principal.go
│   ├── checksum.go                        Checksums, Algorithm
│   ├── version.go                         version comparators          ← pkg/types/version.go
│   ├── page.go                            Page, cursors                ← pkg/store (Page)
│   └── wire/                              JFrog JSON structs (users, groups, permission targets)
│                                                                       ← pkg/types/wire
│
├── platform/                              infrastructure with no business rules
│   ├── buildinfo/                                                      ← internal/buildinfo
│   ├── sql/                               one database, many module schemas
│   │   ├── dialect/                       Dialect, Queryer, unit of work, migrator
│   │   │                                                               ← internal/sqlstore/sqlcommon (dialect.go, unitofwork.go, migrate.go, store.go)
│   │   ├── sqlite/                        driver open, pragmas, IMMEDIATE tx   ← internal/sqlstore/sqlite
│   │   ├── postgres/                      pgx open                     ← internal/sqlstore/postgres
│   │   ├── mysql/                         (P34)
│   │   ├── sqltest/                       harness: every module's store suite on every dialect
│   │   │                                                               ← internal/sqlstore/sqltest
│   │   └── migrations/                    platform-owned tables only: outbox, work, leases, settings
│   ├── tx/                                Tx(ctx, fn), AfterCommit, Inspect(ctx); the context-carried unit of work
│   │                                                                   ← pkg/store (Tx, AfterCommit)
│   ├── outbox/                            Append(ctx, Event) joins the caller's tx; ordered sequence
│   │                                                                   ← pkg/store/peers.go (OutboxStore), generalised (P11)
│   ├── events/                            Bus: Subscribe(topic, Handler); dispatch from outbox via work
│   │                                                                   (P11)
│   ├── work/                              persistent queue, leases, periodic work; fencing for HA
│   │                                                                   ← internal/service/work, pkg/store/work.go
│   ├── blob/                              blob.Store port and backends  ← pkg/blob
│   │   ├── fs/ s3/ gcs/ azure/                                         ← pkg/blob/{fs,s3,gcs,azure}
│   │   ├── mirror/                                                     ← pkg/blob/mirror
│   │   ├── session/                       upload sessions               ← pkg/blob/session
│   │   ├── cold/                          cold-tier wrapper and restore staging (P16)
│   │   └── blobtest/                      contract suite                ← pkg/blob/blobtest
│   ├── cdn/                               signed URL providers          ← pkg/cdn
│   │   └── cdntest/                                                    ← pkg/cdn/cdntest
│   ├── crypt/                             keyring, sealed columns, rotation ← internal/crypt
│   ├── kms/                               external master-key references (P38)
│   ├── archive/                           bounded zip/tar walker         ← internal/archive
│   ├── instance/                          instance identity, server ID   ← internal/app/instance.go
│   ├── httpkit/                           chi router kit, middleware, errors, base URL
│   │   ├── apierror/                                                   ← internal/server/apierror
│   │   ├── baseurl/                                                    ← internal/server/baseurl
│   │   ├── middleware/                    recovery, request ID, trace, body limits
│   │   │                                                               ← internal/server/{middleware.go,trace.go,context.go}
│   │   └── strict/                        helpers to compose partial strict-server implementations
│   ├── gen/                               generated strict servers, one package per spec
│   │   ├── access/ artifactory/ distribution/ event/ grid/ pfed/ worker/   ← internal/server/gen/*
│   │   ├── peer/                          (P13)
│   │   └── evidence/ lifecycle/ apptrust/ xray/ catalog/ curation/ ...    (P23 to P27)
│   ├── observe/                           metrics, health registry, OTel  (P18)
│   └── testkit/
│       └── fault/                         failing blob store, vanishing client ← internal/testfault
│
├── artifacts/                             THE BIG MODULE, laid out as a hexagon
│   ├── module.go                          New(Deps) (*Module, error); API(); Routes(); Start(); Close()
│   ├── api/                               PUBLISHED to other modules
│   │   ├── repo/                          repository configuration model, validation ← pkg/repo
│   │   ├── repositories.go                Repositories: Get, List, Create, Update, Delete, SetProject
│   │   ├── items.go                       Items: Stat, Open, Deploy, Copy, Move, Delete, SetProperties
│   │   ├── packages.go                    Packages: Find, Versions, Files
│   │   ├── builds.go                      Builds: Get, Search, Promote
│   │   ├── search.go                      Search: AQL(ctx, query) streaming rows
│   │   ├── layouts.go                     Layouts: Get, Put (for descriptor and P12)
│   │   ├── hooks.go                       Gate: BeforeDownload, BeforeDeploy, Admit
│   │   ├── events.go                      ItemDeployed, ItemDeleted, ItemMoved, ItemCopied, PropertiesChanged,
│   │   │                                  BuildPublished, BuildPromoted, RepositoryChanged
│   │   └── settings.go                    Settings (npm, maven, nuget, pypi, helm, download, search limits)
│   ├── core/                              no import of adapters/ or ecosystem/*/http
│   │   ├── model/                         Item, Folder, Package, PackageVersion, Build, CacheEntry
│   │   │                                                               ← pkg/types/{item,build,remotecache,replica,source,gitlfs_lock}.go
│   │   ├── port/                          driven ports of this module
│   │   │   ├── store.go                   RepoStore ItemStore BlobIndex ReplicaIndex UploadStore TrashStore
│   │   │   │                              PackageStore BuildStore RemoteCacheStore GitLFSLockStore
│   │   │   │                              ← pkg/store/{content,build,remotecache,gitlfs_locks,properties,aqlsource,layout_sources,normalise}.go
│   │   │   ├── upstream.go                Upstream: Fetch, Head, List (implemented by adapters/upstream)
│   │   │   └── signer.go                  Signer for Debian, RPM, Helm provenance (backed by access/api Keys)
│   │   ├── format/                        the format contract: Handler, Env, Inspector, Admitter, WriteGuard,
│   │   │                                  Resolver, Annotator, Reindexer, Worker, Classifier, RejectionError
│   │   │                                                               ← internal/format (format.go, sidecar.go, snapshots.go)
│   │   ├── formattest/                    the contract suite every slice runs ← internal/format/formattest
│   │   ├── item/                          deploy, checksum deploy, folders, delete, transfer, cache, zap, explode
│   │   │                                                               ← internal/service/item
│   │   ├── pkgindex/                      package index: Link, VersionOf, Prune, Sync, Reconcile
│   │   │                                                               ← internal/service/pkgindex
│   │   ├── trash/                                                      ← internal/service/trash
│   │   ├── gc/                                                         ← internal/service/gc
│   │   ├── multipart/                                                  ← internal/service/multipart
│   │   ├── registry/                      Docker upload sessions, manifests, promote, tag retention
│   │   │                                                               ← internal/service/registry
│   │   ├── build/                         build info, promotion, retention ← internal/service/build
│   │   ├── terraformstate/                workspaces, states, locks       ← internal/service/terraformstate
│   │   ├── usage/                         storage summary                 ← internal/service/usage
│   │   ├── repos/                         repository operations behind Repositories ← internal/repos
│   │   ├── layout/                        layout pattern engine           ← pkg/layout
│   │   ├── aql/                           parser, AST, resolve, reference evaluator ← pkg/aql (aql,criteria,domains,eval,glob,nest,parse,paths,repos,result,transitive)
│   │   │   └── aqltest/                   shared AQL cases               ← pkg/aql/aqltest
│   │   ├── search/                        GET searches, layout and version searches, upstream search
│   │   │                                                               ← internal/server/searches_*.go (logic part)
│   │   ├── remote/                        retrieval decision table, policy, singleflight, negative cache,
│   │   │   │                              offline tripping, cache maintenance
│   │   │   │                              ← internal/remote (fetch,policy,flight,circuit,rules,destination,discovery)
│   │   │   ├── smart/                     Smart Remote sync, statistics, discovery ← internal/remote/smart_*.go
│   │   │   └── source/                    authorized read boundary shared by adapters ← internal/source
│   │   ├── virtual/                       ordered resolution, default deployment repo, merge cache (P10)
│   │   └── reindexauth/                   credential recheck for queued reindex ← internal/reindexauth
│   ├── ecosystem/                         one slice per package type; format at the top, protocol in http/
│   │   ├── registry.go                    Formats(): package type → format.Handler ← internal/format/formats
│   │   ├── generic/                       deploy policy every format embeds ← internal/format/generic
│   │   ├── npm/                           (detailed below)
│   │   ├── maven/                         also gradle, ivy, sbt          ← internal/format/maven, internal/server/maven.go
│   │   ├── docker/                        also oci, helmoci; http/ holds /v2 ← internal/format/docker, internal/server/docker
│   │   ├── golang/                        package type "go"               ← internal/format/golang, internal/server/golang
│   │   ├── pypi/ nuget/ helm/ terraform/ terraformbackend/
│   │   ├── agentpackages/ agentplugins/ aieditorextensions/ alpine/ bazelmodules/ bower/ cargo/
│   │   ├── cocoapods/ composer/ conan/ conda/ cran/ debian/ gems/ gitlfs/ hex/ huggingface/
│   │   ├── jetbrainsplugins/ luarocks/ nimmodel/ opkg/ p2/ pub/ rpm/ skills/ swift/ vcs/
│   │   ├── machinelearning/               (P9c, #780)
│   │   ├── ansible/ chef/ puppet/ vagrant/ nix/   (P9c, #782 to #788)
│   │   └── releasebundles/                storage rules for the releasebundles repository type (P15);
│   │                                      written only through api.Items by the releases module
│   └── adapters/
│       ├── http/                          management REST owned by this module
│       │   ├── strict.go                  ArtifactoryAPI: the artifactory-spec operations this module owns
│       │   ├── items.go storage.go storageinfo.go trash.go transfer.go multipart.go
│       │   ├── redirect.go reindex.go metadata_stats.go source.go source_listing.go smart_status.go
│       │   ├── repositories.go builds.go aql.go aql_actions.go aql_builds.go aql_transitive.go
│       │   ├── aql_transitive_peer.go searches*.go
│       │   └── protocols.go               mounts every ecosystem/<type>/http under its native prefix
│       ├── sql/                           this module's tables
│       │   ├── store.go content*.go blobs.go replicas.go builds.go build_search.go remotecache.go
│       │   │   gitlfs_locks.go repository_headers.go       ← internal/sqlstore/sqlcommon (artifact files)
│       │   ├── aqlsql/                    AQL to SQL per dialect          ← pkg/aql/{sql.go,joinsql.go,join.go}
│       │   └── migrations/{sqlite,postgres,mysql}/   artifacts schema stream
│       ├── inmem/                         reference store for this module's ports ← pkg/store/inmem (artifact files)
│       ├── storetest/                     contract suite for this module's ports ← pkg/store/storetest (artifact files)
│       └── upstream/                      outbound HTTP: client, pool, TLS, credentials, headers, Docker Hub auth
│           │                              ← internal/remote (client,pool,credentials,security,url,protocol,listing*)
│           └── fakeupstream/              controlled upstream for tests  ← internal/fakeupstream
│
├── access/                                users, tokens, permissions, roles, projects, external identity
│   ├── module.go
│   ├── api/
│   │   ├── authn.go                       Authenticator: Basic, Bearer, APIKey, Anonymous → kernel.Principal
│   │   ├── authz.go                       Authorizer: For(ctx, kernel.Principal) (Decider, error)
│   │   │                                  Decider: Allows, AllowsFolder, IsRepoVisible, Holds, IsAdmin, Groups
│   │   ├── identity.go                    Users, Groups (read and admin)
│   │   ├── tokens.go                      Tokens: Issue, Revoke, Introspect
│   │   ├── keys.go                        Keys: signing key pairs, GPG keys, certificates
│   │   ├── projects.go                    Projects, Environments, Membership (P11)
│   │   ├── hooks.go                       TokenGate: BeforeCreateToken (P17)
│   │   ├── events.go                      UserCreated, UserDeactivated, GroupChanged, PermissionChanged,
│   │   │                                  TokenRevoked (P11, P22 consumers: federation, sessions)
│   │   └── settings.go
│   ├── core/
│   │   ├── model/                         User, Group, Permission, Role, Token, Key, Project
│   │   │                                  ← pkg/types/{permission,permconv,roles,scope,token,keys,userconv,project}.go
│   │   ├── port/store.go                  UserStore GroupStore PermissionStore TokenStore KeyStore ProjectStore
│   │   │                                  ← pkg/store/identity.go, pkg/store/operations.go (ProjectStore)
│   │   ├── authn/                         Authenticator, password, lockout, suspension, seed ← internal/auth
│   │   ├── token/                         JWT, keys                         ← internal/auth/token
│   │   ├── authz/                         Decider, pattern, repo patterns    ← internal/authz
│   │   ├── identity/                                                       ← internal/service/identity
│   │   ├── permission/                                                     ← internal/service/permission
│   │   ├── roles/                                                          ← internal/service/roles
│   │   ├── oidc/                          workload OIDC exchange           ← internal/service/oidc, internal/auth/idtoken
│   │   ├── keymaterial/                                                    ← internal/keymaterial
│   │   ├── project/                       (P11)
│   │   ├── session/                       browser sessions, CSRF, approvals, PKCE (P21)
│   │   ├── federated/                     external identity linking, group mapping, MFA policy (P21)
│   │   └── scim/                          reconciliation, conflict rules, deprovisioning (P22)
│   └── adapters/
│       ├── http/                          access spec and artifactory-spec security operations
│       │   ├── strict_access.go strict_security.go
│       │   ├── authn_middleware.go        sets kernel.Principal on the request context ← internal/server/auth.go
│       │   ├── access_identity.go access.go keypairs.go oidc.go permissions.go roles.go security.go tokens.go
│       │   ├── scim/                      (P22)
│       │   └── login/                     browser login and native approval endpoints (P21)
│       ├── sql/                           identity*.go reseal.go, migrations/{sqlite,postgres,mysql}/
│       ├── inmem/ storetest/
│       ├── ldap/                          (P12)
│       ├── saml/ oidcidp/ crowd/ httpsso/ (P21)
│       └── oidc/oidctest/                                                  ← internal/auth/idtoken/idtokentest
│
├── configuration/                         system configuration and descriptors; coordinator of other modules
│   ├── module.go
│   ├── api/
│   │   ├── config.go                      System: Get, Patch; Descriptor: Apply, Export (P12)
│   │   └── events.go                      ConfigurationChanged
│   ├── core/
│   │   ├── descriptor/                    YAML apply, XML export (P12)   ← internal/descriptor
│   │   ├── system/                        base URL, web server, mail, proxies, system message (P12)
│   │   ├── backup/                        scheduled backups (P12, P36)
│   │   ├── transfer/                      Artifactory import and export, bootstrap migration (P36)
│   │   └── port/store.go                  ConfigStore SettingsStore      ← pkg/store/operations.go (ConfigStore)
│   └── adapters/
│       ├── http/                          descriptor.go system.go reverseproxy.go ← internal/server
│       └── sql/ inmem/ storetest/
│
├── notifications/                         (P11; storage skeleton at P9c) small, flat inside
│   ├── module.go
│   ├── api/notifications.go               Webhooks admin; events.go: DeliveryFailed
│   ├── catalogue.go                       JFrog event names ↔ module events
│   ├── criteria.go delivery.go signing.go retry.go troubleshoot.go
│   ├── store.go                           WebhookStore                   ← pkg/store/operations.go (WebhookStore)
│   └── adapters/{http,sql,inmem,storetest}/
│
├── retention/                             (P11, P16; storage skeleton at P9c)
│   ├── module.go
│   ├── api/retention.go                   CleanupPolicies, ArchivePolicies, Restores
│   ├── core/{cleanup,archive,restore,port}/   ← pkg/store/operations.go (CleanupStore), enterprise.go (ArchiveStore)
│   └── adapters/{http,sql,inmem,storetest}/
│
├── federation/                            (P11, P13, P14, P37; storage skeleton at P9c)
│   ├── module.go
│   ├── api/federation.go                  Peers, FederatedRepos, Replications, Topology; events.go
│   ├── core/
│   │   ├── peer/ pairing/                 identities, keys, signed requests (P13)
│   │   ├── sync/                          outbox sender, acknowledgements, idempotent apply, full sync (P13)
│   │   ├── federated/                     member management, conflict rule, lag, replay (P13)
│   │   ├── replication/                   single push and pull (P11), multi-push and event (P37)
│   │   ├── accessfed/                     users, groups, permissions over peers (P14)
│   │   ├── topology/                      pfed, grid (P14)
│   │   └── port/                          ← pkg/store/peers.go (InstanceStore PeerStore FederationStore PlatformStore),
│   │                                        pkg/store/operations.go (ReplicationStore), pkg/types/{peer,federation,platform,replica}.go
│   └── adapters/
│       ├── peer/                          /artifactweave/peer/v1 server and client (P13)
│       ├── http/                          pfed, grid, federation, replication endpoints
│       └── sql/ inmem/ storetest/
│
├── releases/                              (P15, P24; storage skeleton at P9c)
│   ├── module.go
│   ├── api/releases.go                    Bundles, Lifecycle, Distribution; events.go: BundleCreated, Promoted, Distributed
│   ├── core/{bundlev1,bundlev2,lifecycle,distribution,signing,port}/   ← pkg/store/enterprise.go (BundleStore DistributionStore),
│   │                                                                       pkg/types/distribution.go
│   └── adapters/{http,sql,inmem,storetest}/
│
├── workers/                               (P17; storage skeleton at P9c) detailed below
├── evidence/                              (P23) api/ core/ adapters/{http,sql}/
├── governance/                            (P25) api/ core/{application,policy,controlpoint,approval,audit}/ adapters/
├── security/                              (P26, P27) api/ core/{index,graph,sbom,analysis,watch,catalog,curation}/
│                                          adapters/{http,sql,engine,feed,graphql}/
├── operations/                            (P18, P38) flat: health.go metrics.go support.go queueadmin.go
│                                          integrations/{vault,iam,logship}/ adapters/http/
├── estate/                                (P40) flat: missioncontrol.go myjfrog.go jfconnect.go bridges.go adapters/http/
│
└── gateway/                               driving adapters that own no data
    ├── onemodel/                          (P26) GraphQL schema stitching; resolvers call api/ packages
    └── console/                           (P39) web console and static assets over the public APIs

pkg/
├── client/
│   ├── access/ artifactory/ distribution/ event/ grid/ pfed/ worker/   ← pkg/client/*
│   └── peer/ evidence/ ...                (P13, P23 onwards)
└── containertest/                                                      ← pkg/containertest
```

All 49 package types and where they live:

| Package type | Slice | Notes |
| --- | --- | --- |
| agentpackages, agentplugins, aieditorextensions, alpine, bazelmodules, bower, cargo, cocoapods, composer, conan, conda, cran, debian, gems, gitlfs, helm, hex, jetbrainsplugins, luarocks, npm, nuget, opkg, p2, pub, pypi, rpm, skills, swift, terraform, terraformbackend, vcs | `ecosystem/<type>/` | one slice each |
| go | `ecosystem/golang/` | `go` is a keyword, so the package is `golang` |
| maven, gradle, ivy, sbt | `ecosystem/maven/` | one format, four layouts |
| docker, oci, helmoci | `ecosystem/docker/` | one registry protocol; helmoci adds chart rules in `docker/helmoci.go` |
| huggingfaceml | `ecosystem/huggingface/` | |
| nimmodel | `ecosystem/nimmodel/` | remote only; pairs with a docker remote |
| machinelearning, ansible, chef, puppet, vagrant, nix | `ecosystem/<type>/` | created by P9c tickets |
| generic | `ecosystem/generic/` | embedded by every other format |
| releasebundles | `ecosystem/releasebundles/` | storage rules only; the releases module writes through `api.Items` |

Where today's 65 files in the `internal/server` root go:

| Files | New home |
| --- | --- |
| server.go, handwritten.go, protocols.go, stubs.go | `internal/app/router.go` (composition of partial strict servers and protocol mounts), `artifacts/adapters/http/protocols.go` |
| middleware.go, context.go, trace.go | `platform/httpkit/middleware/` |
| auth.go | `access/adapters/http/authn_middleware.go` |
| access_identity.go, access.go, keypairs.go, oidc.go, permissions.go, roles.go, security.go, tokens.go | `access/adapters/http/` |
| descriptor.go, system.go, reverseproxy.go | `configuration/adapters/http/` |
| items.go, storage.go, storageinfo.go, trash.go, transfer.go, multipart.go, redirect.go, reindex.go, metadata_stats.go, source.go, source_listing.go, smart_status.go, repositories.go, builds.go | `artifacts/adapters/http/` |
| aql.go, aql_actions.go, aql_builds.go, aql_transitive.go, aql_transitive_peer.go | `artifacts/adapters/http/` (handlers) and `artifacts/core/aql` (logic) |
| searches.go, searches_builds.go, searches_layout.go, searches_versions.go, searches_upstream.go, searches_upstream_maven.go, searches_upstream_routes.go | `artifacts/adapters/http/searches*.go` and `artifacts/core/search/` |
| maven.go | `artifacts/ecosystem/maven/http/` |
| npm_auth.go, npm_operations.go | `artifacts/ecosystem/npm/http/login.go` (calls `access/api.Tokens`), `npm/http/operations.go` |
| pypi_routes.go | `artifacts/ecosystem/pypi/http/routes.go` |
| docker_rest.go | `artifacts/ecosystem/docker/http/rest.go` |
| agentplugins.go, alpine.go, opkg.go, rpm.go | `artifacts/ecosystem/<type>/http/` |
| cran.go, cran_upload.go | `artifacts/ecosystem/cran/http/` |
| debian.go, debian_operations.go, debian_snapshot.go, debian_virtual.go | `artifacts/ecosystem/debian/http/` |
| p2.go, p2_external.go | `artifacts/ecosystem/p2/http/` |
| vcs_download.go, vcs_nested.go, vcs_routes.go | `artifacts/ecosystem/vcs/http/` (joins today's `internal/server/vcs`) |

## Deep dives

### npm ecosystem

```text
internal/artifacts/ecosystem/npm/
├── npm.go                 Format type: embeds generic.Handler; Deployed, Deleting, Deleted ← internal/format/npm/npm.go
├── policy.go              Admit: name rules, overwrite and unpublish policy             ← format/npm/policy.go
├── tarball.go             Inspect: bounded read of package.json from the tarball         ← format/npm/tarball.go
├── metadata.go            packument model, dist-tags, deprecations                      ← format/npm/metadata.go
├── index.go               pkgindex.Link and Reconcile for npm versions                  ← format/npm/index.go
├── latest.go              latest-tag selection                                          ← format/npm/latest.go
├── search.go              search scoring over the package index                         ← format/npm/search.go
├── work.go                format.Worker: queued reindex and strict-layout conversion    ← format/npm/work.go
├── remote.go              upstream packument and tarball rules, integrity checks
│                          (uses core/port.Upstream; no HTTP server code)               ← internal/server/npm/remote_*.go (rule part)
├── npm_test.go            unit tests for the format
├── formattest_test.go     formattest.Run(t, npm.Format{}, fixture)
├── testdata/              tarballs, packuments, malformed cases
└── http/                  native protocol adapter; only app/ and artifacts/adapters/http/protocols.go import it
    ├── npm.go             Routes(r chi.Router, deps): /api/npm/{repo}/...                ← internal/server/npm/npm.go
    ├── packument.go       GET packument, abbreviated packument                          ← server/npm/packument.go
    ├── publish.go         PUT publish: decodes body, calls api.Items.Deploy             ← server/npm/publish.go
    ├── body.go            streaming publish body decoder                                ← server/npm/body.go
    ├── disttags.go        dist-tag endpoints                                            ← server/npm/disttags.go
    ├── update.go          deprecate, unpublish                                          ← server/npm/update.go
    ├── search.go          /-/v1/search                                                  ← server/npm/search.go
    ├── user.go            legacy login and logout, whoami                                ← server/npm/user.go
    ├── login.go           /api/npm/auth; issues tokens through access/api.Tokens        ← internal/server/npm_auth.go
    ├── operations.go      reindex and conversion endpoints                              ← internal/server/npm_operations.go
    ├── npmrc.go           .npmrc snippet                                                ← server/npm/npmrc.go
    ├── remote.go          remote GET routing to core/remote and npm.remote rules        ← server/npm/remote.go, remote_dist.go,
    │                                                                                       remote_integrity.go, remote_validate.go
    └── *_test.go          handler unit tests with a fake artifacts core

test/server/artifacts/npm/              server-assembly suites                         ← test/server/npm
├── matrix_npm_test.go matrix_npm_auth_test.go matrix_npm_lifecycle_test.go matrix_types_test.go
├── status_npm_test.go status_npm_auth_test.go status_npm_lifecycle_test.go
├── npm_auth_test.go npm_operations_test.go fixture_test.go matrix_fixture_test.go status_fixture_test.go
test/acceptance/features/npm.feature npm_remote.feature npm_search_auth.feature npm_overwrites_and_settings.feature
test/acceptance/clients/node.Dockerfile node-npm12.Dockerfile
```

The format and its protocol sit in one directory. The `http/` subpackage is
the hexagon's edge running through the slice: `npm` (core side) never imports
`npm/http`, and `npm/http` reaches the core only through `artifacts/api` and
the `npm` package's exported rules.

### Deploy write path

```text
internal/artifacts/core/item/
├── item.go            Service, Target, Deploy, Upload, DeployCommitted, DeployByChecksum, MakeFolder,
│                      EnsureRoot, Delete, Purge, Tree                                  ← service/item/item.go
├── format.go          Env construction, Annotate, Resolve, Reindex, ReindexPath           ← service/item/format.go
├── gate.go            runs the registered api.Gate chain (BeforeDeploy, BeforeDownload)  (P17)
├── events.go          builds api.ItemDeployed and friends; outbox.Append inside the tx   (P11)
├── sidecar.go         checksum sidecars                                                  ← service/item/sidecar.go
├── archive.go         Explode, ArchiveEntry, ArchiveFiles                                ← service/item/archive.go
├── cache.go           Cache: remote cache publication with generation checks             ← service/item/cache.go
├── cache_cleanup.go   persistent unused-cache cleanup registration                       ← service/item/cache_cleanup.go
├── zap.go smart.go    metadata zap, Smart Remote property state                          ← service/item/{zap,smart}.go
├── remove.go restore.go                                                                  ← service/item/{remove,restore}.go
├── transfer.go transfer_work.go   copy and move, queued transfers                        ← service/item/transfer*.go
├── work.go            Enqueuer                                                          ← service/item/work.go
└── *_test.go
internal/artifacts/core/pkgindex/    index.go reconcile.go                               ← service/pkgindex
internal/artifacts/core/build/       build.go document.go promote.go retention.go        ← service/build
internal/artifacts/core/registry/    registry.go manifests.go indexes.go deletes.go platforms.go promote.go
                                     retention.go twins.go testdata/                      ← service/registry
internal/artifacts/core/trash/ gc/ multipart/                                             ← service/{trash,gc,multipart}
internal/platform/tx/                tx.go (Tx, AfterCommit), tx_test.go                  ← pkg/store/store.go (Tx, AfterCommit)
internal/platform/sql/dialect/       unitofwork.go (unit, inTx, runTx), dialect.go, migrate.go ← sqlcommon
internal/platform/outbox/            outbox.go (Append, Next, Ack), outbox_test.go        (P11)
```

The whole deploy stays inside `artifacts`; only the outbox append and the
gate chain reach outside, and both are interfaces owned elsewhere.

```go
// Deploy stores the content first, outside the transaction, so no blob write
// holds the database's write lock. Everything that must agree with the item
// happens in one transaction, including the events other modules hear.
func (s *Service) Deploy(ctx context.Context, d Deploy) (model.Item, error) {
	h := s.formats.For(d.Repo.PackageType)
	info, err := s.blobs.Put(ctx, d.Body) // platform/blob
	if err != nil {
		return model.Item{}, err
	}
	w := format.Write{Op: format.OpDeploy, Path: d.Path, Blob: info}
	env := s.env()
	if insp, ok := h.(format.Inspector); ok {
		if env.Inspection, err = insp.Inspect(ctx, env, d.Repo, w); err != nil {
			return model.Item{}, err
		}
	}
	if err := s.gates.BeforeDeploy(ctx, api.DeployRequest{Repo: d.Repo.Key, Path: d.Path, By: d.By}); err != nil {
		return model.Item{}, err // a Worker or Curation refusal (P17, P27)
	}
	var it model.Item
	err = s.tx.Tx(ctx, func(ctx context.Context) error {
		if a, ok := h.(format.Admitter); ok {
			if w, err = a.Admit(ctx, env, d.Repo, w); err != nil {
				return err
			}
		}
		if it, err = s.items.Put(ctx, d.Repo.Key, w); err != nil {
			return err
		}
		if err := h.Deployed(ctx, env, d.Repo, it); err != nil { // npm index.go → pkgindex.Link
			return err
		}
		return s.outbox.Append(ctx, api.ItemDeployed{Repo: d.Repo.Key, Path: it.Path, Sha256: it.Sha256, By: d.By})
	})
	return it, err
}
```

### Access and identity

```text
internal/access/
├── module.go                    New(Deps) builds Authenticator, Authorizer, services; Routes(); Start() (token GC, seed)
├── api/                         authn.go authz.go identity.go tokens.go keys.go projects.go(P11) hooks.go(P17)
│                                events.go settings.go
├── core/
│   ├── authn/                   authenticator.go password.go policy.go suspension.go deferred.go download.go
│   │                            current.go current_registry.go seed.go token.go principal.go ← internal/auth
│   ├── token/                   jwt.go keys.go token.go                                       ← internal/auth/token
│   ├── authz/                   authz.go (Decider, New(ctx, Sources, Subject)) pattern.go repopatterns.go ← internal/authz
│   ├── oidc/                    exchange.go match.go oidc.go pattern.go idtoken.go             ← service/oidc, auth/idtoken
│   ├── identity/                access.go identity.go                                          ← service/identity
│   ├── permission/              permission.go                                                  ← service/permission
│   ├── roles/                   roles.go (built-in environments merged with stored)            ← service/roles
│   ├── keymaterial/             keymaterial.go                                                 ← internal/keymaterial
│   ├── project/                 project.go membership.go quota.go environments.go             (P11)
│   ├── ldap/                    bind, group sync policy                                        (P12)
│   ├── session/                 session.go csrf.go approval.go pkce.go                        (P21)
│   ├── federated/               linking.go mapping.go mfa.go recovery.go                      (P21)
│   ├── scim/                    users.go groups.go filter.go patch.go reconcile.go             (P22)
│   └── port/store.go            UserStore GroupStore PermissionStore TokenStore KeyStore ProjectStore
└── adapters/
    ├── http/                    authn_middleware.go strict_access.go strict_security.go access_identity.go
    │                            access.go keypairs.go oidc.go permissions.go roles.go security.go tokens.go
    ├── sql/                     identity.go identity_keys.go reseal.go migrations/       ← sqlcommon identity files
    ├── inmem/ storetest/        identity parts                                           ← pkg/store/{inmem,storetest} identity files
    ├── ldap/ saml/ oidcidp/ crowd/ httpsso/                                              (P12, P21)
    └── oidc/oidctest/                                                                    ← internal/auth/idtoken/idtokentest
```

How a protocol handler gets identity and permission:

1. `app/router.go` installs `access/adapters/http.AuthnMiddleware` before every
   route. It calls `access/api.Authenticator` and puts a `kernel.Principal` in
   the request context.
2. A handler such as `artifacts/ecosystem/npm/http/publish.go` reads the
   principal with `kernel.PrincipalFrom(ctx)` and calls `artifacts/api.Items`.
3. `artifacts/core/item` asks `access/api.Authorizer.For(ctx, principal)` for a
   `Decider` once per operation and calls `Allows(ActionDeploy, repo, path)`.
   Artifacts imports `access/api` only; access never imports artifacts' core.
4. Project attach (P11) runs the other way: `access/core/project` calls
   `artifacts/api.Repositories.SetProject`. Both directions go through `api/`
   packages, so no package cycle forms.

### Storage and persistence

Each module owns its store ports, its SQL implementation, its in-memory
reference implementation and its contract suite. The platform owns the
dialect layer, the unit of work and the migrator.

```text
internal/platform/sql/
├── dialect/            dialect.go (Dialect) unitofwork.go (Queryer, runTx, afterCommit) migrate.go (per-module streams)
├── sqlite/ postgres/   open + driver settings                         ← internal/sqlstore/{sqlite,postgres}
├── mysql/              (P34)
├── sqltest/            sqltest.go: RunModule(t, open, suite) on each dialect; rotation.go remotecache.go
│                       repository_headers.go                         ← internal/sqlstore/sqltest
└── migrations/         platform stream: outbox, work_queue, leases, settings ← parts of 0003_operations.sql, 0004_peers.sql

internal/platform/tx/tx.go          type Runner interface { Tx(ctx, fn) error; AfterCommit(ctx, fn) }
internal/platform/work/             work.go queue.go lease.go          ← internal/service/work, pkg/store/work.go
internal/platform/blob/             blob.go (Store, Info, Upload, ranged reads, presign) ← pkg/blob
├── fs/      fs.go layout.go uploads.go ...                            ← pkg/blob/fs (5 files)
├── s3/ gcs/ azure/                                                     ← pkg/blob/{s3,gcs,azure}
├── mirror/  mirror.go read.go repair.go status.go upload.go          ← pkg/blob/mirror
├── session/                                                            ← pkg/blob/session
├── cold/    (P16)
└── blobtest/blobtest.go                                                ← pkg/blob/blobtest
internal/platform/cdn/              cloudfront, cloud CDN, Front Door signers  ← pkg/cdn

internal/artifacts/core/port/store.go       content, packages, builds, remote cache, Git LFS locks
internal/artifacts/adapters/sql/
├── store.go content.go content_items.go content_index.go blobs.go replicas.go builds.go build_search.go
│   remotecache.go gitlfs_locks.go repository_headers.go       ← internal/sqlstore/sqlcommon (same names)
├── aqlsql/ sql.go joinsql.go join.go                           ← pkg/aql
└── migrations/{sqlite,postgres,mysql}/
    0001_content.sql 0006_blob_replicas.sql 0007_blob_states.sql 0012_builds.sql 0013_property_values.sql
    0014_item_properties_modified.sql 0015_remote_cache.sql 0016_repository_header_secrets.sql 0017_gitlfs_locks.sql
    (renumbered within the artifacts stream)
internal/artifacts/adapters/inmem/          content.go content_items.go content_index.go blobs.go replicas.go builds.go
                                            build_search.go remotecache.go gitlfs_locks.go page.go ← pkg/store/inmem
internal/artifacts/adapters/storetest/      content.go builds.go properties.go remotecache.go replicas.go search.go
                                            gitlfs_locks.go layout_sources.go core.go ← pkg/store/storetest
internal/access/adapters/sql/migrations/    0002_identity.sql 0008_certificate_keys.sql 0009_user_lockout.sql
                                            0010_token_purpose.sql 0011_token_identity.sql
internal/configuration/adapters/sql/        operations_config.go  migrations: configs table ← sqlcommon/operations_config.go
internal/notifications/adapters/sql/        operations_webhooks.go                         ← sqlcommon/operations_webhooks.go
internal/retention/adapters/sql/            operations_cleanup.go, enterprise archive parts ← sqlcommon
internal/federation/adapters/sql/           peers.go, replication parts of operations.go   ← sqlcommon/peers.go
internal/releases/adapters/sql/             enterprise bundle and distribution parts       ← sqlcommon/enterprise.go
internal/workers/adapters/sql/              enterprise workers part                         ← sqlcommon/enterprise.go
```

Rules for tables:

- One migration stream per module, recorded in a `schema_migrations` table
  keyed by module name. The migrator runs platform first, then modules in the
  order `app/` registers them.
- No foreign key crosses a module boundary. Artifacts stores a user name, not
  a foreign key to access's users table.
- `platform/sql/sqltest.RunModule` runs any module's store suite against
  every dialect, so a module adds one test file to get the SQLite, PostgreSQL
  and, from P34, MySQL runs.

### Remote and virtual repositories

```text
internal/artifacts/core/remote/              retrieval decision and cache policy (no HTTP client code)
├── fetch.go          the decision table: cache, revalidate, fetch, negative cache    ← remote/fetch.go
├── policy.go rules.go destination.go discovery.go                                    ← remote/{policy,rules,destination,discovery}.go
├── flight.go         singleflight per path                                           ← remote/flight.go
├── circuit.go        assumed-offline tripping                                         ← remote/circuit.go
├── module_location.go                                                                 ← remote/module_location.go
├── smart/            smart.go smart_aql.go smart_discovery.go smart_peer.go smart_properties.go smart_queue.go
│                     smart_request.go smart_statistics.go smart_status.go smart_trace.go ← remote/smart_*.go
└── source/           source.go gitlfs_action.go (authorized read boundary)            ← internal/source
internal/artifacts/adapters/upstream/        implements core/port.Upstream
├── client.go pool.go credentials.go security.go url.go protocol.go                    ← remote/{client,pool,credentials,security,url,protocol}.go
├── listing.go listing_html.go listing_mapped.go listing_smart.go                     ← remote/listing*.go
├── registry_challenge.go registry_token.go                                            ← remote/registry_{challenge,token}.go
├── testdata/                                                                          ← remote/testdata
└── fakeupstream/     proxy.go record.go sync.go tls.go upstream.go README.md         ← internal/fakeupstream
internal/artifacts/ecosystem/<type>/remote.go    per-format upstream rules
├── golang/remote.go golang/remote_zip.go                                              ← remote/{golang,golang_zip}.go
├── maven/remote.go maven/remote_eager.go                                              ← remote/{maven,maven_eager}.go
├── docker/remote.go remote_children.go remote_content.go remote_descriptors.go remote_pages.go
│                                                                                      ← remote/registry*.go
└── nuget/remote_empty_feed.go                                                         ← remote/nuget_empty_feed.go
internal/artifacts/core/virtual/             (P10)
├── resolve.go        ordered member resolution, defaultDeploymentRepo, hideUnauthorizedResources
├── merge.go          Merger interface a format implements for metadata merging
├── cache.go          virtualRetrievalCachePeriodSecs
└── resolve_test.go
internal/artifacts/ecosystem/<type>/virtual.go  (P10) per-format Merger: npm packuments, maven-metadata, PyPI simple
                                                indexes, Go list, Helm index, NuGet registrations
```

### Workers (P17), a roadmap domain end to end

```text
internal/workers/
├── module.go               New(Deps{Items artifactsapi.Items, Authz accessapi.Authorizer, Bus events.Bus, ...})
├── api/
│   ├── workers.go          Workers: List, Get, Put, Delete, Execute, Test (used by gateway/console, operations)
│   └── events.go           ExecutionFailed, WorkerChanged
├── core/
│   ├── catalogue.go        action catalogue: BEFORE_DOWNLOAD AFTER_DOWNLOAD BEFORE_UPLOAD AFTER_CREATE
│   │                       AFTER_BUILD_INFO_SAVE BEFORE_CREATE_TOKEN SCHEDULED_EVENT GENERIC_EVENT ...
│   ├── binding.go          which workers run for which repositories and filters
│   ├── gate.go             implements artifactsapi.Gate and accessapi.TokenGate: runs BEFORE_* workers synchronously
│   ├── subscribe.go        subscribes to artifacts and access events for AFTER_* actions
│   ├── execute.go          one execution: payload, limits, result mapping
│   ├── history.go          execution history and rerun
│   ├── limits.go           64 MiB, 1 s CPU, 5 s per call, 20 s overall, depth 100, 100 KiB payload
│   └── port/               store.go (WorkerStore ← pkg/store/enterprise.go), sandbox.go (Sandbox interface)
└── adapters/
    ├── http/               strict_worker.go: worker spec v1 and v2 endpoints, test, execute, rerun
    ├── sandbox/            implements core/port.Sandbox
    │   ├── pool.go         helper process pool, one execution per helper
    │   ├── protocol.go     parent ↔ helper framing over stdin and stdout
    │   ├── transpile.go    esbuild TypeScript to JS
    │   ├── host/           the worker-host side: goja runtime, platform client, allow-listed outbound HTTP
    │   └── sandbox_test.go looping and memory-limit cases
    ├── sql/ inmem/ storetest/
cmd/artifactweave/main.go   hidden `worker-host` subcommand calls workers/adapters/sandbox/host.Main
api/src/worker/             paths-workers.yaml gains the v2 endpoints from the jf worker plugin source
internal/platform/gen/worker/   generated strict server
```

How the core offers the hook without importing workers:

```go
// internal/artifacts/api/hooks.go
// Gate is consulted before an operation that a policy may refuse. Every
// registered gate must allow; the first refusal wins and is returned to the
// client with its status. Gates run outside the item transaction, so a slow
// Worker never holds the database's write lock.
type Gate interface {
	BeforeDownload(ctx context.Context, r DownloadRequest) error
	BeforeDeploy(ctx context.Context, r DeployRequest) error
}

// internal/app/modules.go (excerpt)
artifacts.RegisterGates(workersMod.Gate(), securityMod.CurationGate(), governanceMod.Gate())
```

### Webhooks and events (P11, P13)

```text
internal/platform/outbox/
├── outbox.go         Event{Seq, Topic, Key, Payload, At}; Append(ctx, e) joins the caller's tx via platform/tx
├── read.go           Next(ctx, consumer, n), Ack(ctx, consumer, seq): per-consumer cursors
├── retention.go      prune events every consumer has acknowledged
└── outboxtest/       contract suite (ordering, rollback drops the event, cursor independence)
internal/platform/events/
├── bus.go            Subscribe(topic, consumer, Handler); dispatch loop driven by platform/work
├── idempotency.go    consumer-side dedupe by (consumer, Seq)
└── replay.go         replay from a sequence for operators (P38 queue administration)
internal/<module>/api/events.go    typed events with Topic() and Encode(); e.g. artifacts.ItemDeployed
internal/notifications/
├── catalogue.go      maps module events to JFrog event names (deployed, deleted, moved, copied,
│                     properties added, build uploaded, release bundle created, ...)
├── subscribe.go      one Bus subscription per catalogue topic
├── criteria.go delivery.go signing.go retry.go   delivery log and retries in its own tables
└── adapters/http/    event spec endpoints, test, troubleshooting
internal/federation/core/sync/
├── sender.go         consumer "federation:<peer>": reads the same outbox, signs, sends to the peer
├── ack.go            peer acknowledgements advance the cursor
└── apply.go          idempotent apply of peer events through artifacts/api.Items
```

One outbox serves every consumer. Webhooks, Workers AFTER actions, federation,
replication, evidence association and security scan triggers are all
consumers with their own cursors. A rolled-back write appends no event.

### Composition root

```text
internal/app/
├── run.go            Run(ctx, cfg, log): open platform, build modules, serve, drain, close ← app/app.go
├── platform.go       open SQL, migrate, blob stores, mirrors, CDN, keyring, work queue, outbox ← app/{store,blob,mirrors}.go
├── modules.go        construct modules in dependency order, register gates and subscriptions
├── router.go         chi router: middleware, partial strict servers composed per spec, protocol mounts
│                                                     ← internal/server/{server.go,handwritten.go,protocols.go,stubs.go}
├── bootstrap.go      bootstrap descriptor through configuration/api ← app/descriptor.go
├── seed.go           built-in admin through access/api               ← app/seed.go
├── runtime.go        holds modules for close and for tests            ← app/runtime.go
├── serverlog.go
├── config/           config.go blob.go helm.go maven.go npm.go nuget.go pypi.go ← internal/config
└── *_test.go         lifecycle tests; remote and registry fault tests move to the artifacts module
                      (conan_remote_test.go, maven_remote_test.go, registry_remote_test.go, ...)
```

Per-module background work moves out of `app`: `app/gc.go` becomes
`artifacts/module.go`'s `Start` registering GC with `platform/work`, and
`app/remote.go` becomes the artifacts module's wiring of remote sources.

Wiring at P9c:

```go
func buildModules(p *platform, cfg config.Config, log *slog.Logger) (*modules, error) {
	acc, err := access.New(access.Deps{
		Tx: p.tx, SQL: p.sql, Keyring: p.keyring, Work: p.work, Outbox: p.outbox,
		Settings: cfg.Access, Log: log,
	})
	if err != nil {
		return nil, fmt.Errorf("access: %w", err)
	}
	art, err := artifacts.New(artifacts.Deps{
		Tx: p.tx, SQL: p.sql, Blobs: p.blobs, ColdBlobs: p.cold, CDN: p.cdn,
		Work: p.work, Outbox: p.outbox, Authz: acc.API().Authorizer, Keys: acc.API().Keys,
		Settings: cfg.Artifacts, Log: log,
	})
	if err != nil {
		return nil, fmt.Errorf("artifacts: %w", err)
	}
	conf, err := configuration.New(configuration.Deps{
		Tx: p.tx, SQL: p.sql, Repositories: art.API().Repositories, Layouts: art.API().Layouts,
		Security: acc.API().Identity, Log: log,
	})
	if err != nil {
		return nil, fmt.Errorf("configuration: %w", err)
	}
	return &modules{access: acc, artifacts: art, configuration: conf}, nil
}
```

At P41 the same function constructs thirteen modules in this order:
access, artifacts, configuration, notifications, retention, federation,
releases, workers, evidence, governance, security, operations, estate, then
the two gateways. It then registers gates (workers, security, governance on
artifacts; workers on access) and event subscriptions. The function stays
linear and explicit; at roughly 200 lines it is the strongest argument for a
wiring aid, which is the next decision.

### Generated code and API specifications

| Spec | Fragments | Generated server | Implemented by | Client |
| --- | --- | --- | --- | --- |
| artifactory | `api/src/artifactory/` | `internal/platform/gen/artifactory` | `artifacts/adapters/http` (storage, repositories, builds, searches, packagetypes, trash, archive search), `access/adapters/http` (security), `configuration/adapters/http` (system, admin), `federation/adapters/http` (federation, replication, P11 and P13), `retention/adapters/http` (cleanup, archive, P11 and P16), `releases/adapters/http` (releasebundles, P15) | `pkg/client/artifactory` |
| access | `api/src/access/` | `platform/gen/access` | `access/adapters/http`; `federation/adapters/http` for paths-federation (P14) | `pkg/client/access` |
| event | `api/src/event/` | `platform/gen/event` | `notifications/adapters/http` (P11) | `pkg/client/event` |
| distribution | `api/src/distribution/` | `platform/gen/distribution` | `releases/adapters/http` (P15) | `pkg/client/distribution` |
| grid, pfed | `api/src/grid/`, `api/src/pfed/` | `platform/gen/{grid,pfed}` | `federation/adapters/http` (P14) | `pkg/client/{grid,pfed}` |
| worker | `api/src/worker/` | `platform/gen/worker` | `workers/adapters/http` (P17) | `pkg/client/worker` |
| peer | `api/src/peer/` (P13) | `platform/gen/peer` | `federation/adapters/peer` | `pkg/client/peer` |
| evidence, lifecycle, apptrust, xray, catalog, curation | (P23 to P27) | `platform/gen/<spec>` | the owning module | `pkg/client/<spec>` |

One generated strict-server interface spans several modules, so `app/router.go`
composes it by embedding. Each module's partial implementation is embedded at
depth one; the generated stubs that answer 501 are embedded at depth two, so
any operation a module implements shadows its stub, and two modules claiming
the same operation is a compile error.

```go
// internal/app/router.go (excerpt)
type artifactoryServer struct {
	*artifactshttp.ArtifactoryAPI
	*accesshttp.SecurityAPI
	*configurationhttp.SystemAPI
	stubs // depth two: struct{ artifactory.Unimplemented }
}

type stubs struct{ gen.Unimplemented }
```

`internal/devtools/stubgen` keeps generating the `Unimplemented` type;
`internal/devtools/specoracle` and `tools/specoracle` are unchanged in role.
`api/oapi-server-<spec>.yaml` changes only its `output` path, for example
`output: internal/platform/gen/artifactory/server.gen.go`.

### Tests and enforcement

| Kind | Location |
| --- | --- |
| Unit tests | beside the code, in every package |
| Store contract suites | `internal/<module>/adapters/storetest/`, run by the module's `adapters/inmem` and `adapters/sql` tests through `platform/sql/sqltest.RunModule` |
| Port contract suites | `platform/blob/blobtest`, `platform/cdn/cdntest`, `platform/outbox/outboxtest`, `artifacts/core/formattest`, `artifacts/core/aql/aqltest` |
| Module tests | `internal/<module>/module_test.go`: the module built with inmem stores and fake `api` dependencies |
| Server-assembly suites | `test/server/artifacts/<type>/` per ecosystem, `test/server/<module>/` per module, `test/server/support/` shared |
| Acceptance | `test/acceptance/` unchanged |
| Multi-instance | `test/peers/` (P13) |
| Platform and Terraform | `test/platform/`, `test/terraform/`, `test/oci/` unchanged |

The dependency-rule test:

```go
// internal/layout/layout_test.go
var modules = []string{
	"artifacts", "access", "configuration", "notifications", "retention", "federation",
	"releases", "workers", "evidence", "governance", "security", "operations", "estate",
}

// allowed reports whether a file under from may import to. The rules are the
// five in the layout document; an unlisted top-level directory under internal
// fails the test so a new module must be added here on purpose.
func allowed(from, to string) bool {
	switch {
	case under(from, "internal/platform"), under(from, "internal/kernel"):
		return !underAny(to, moduleRoots()...) && !under(to, "internal/gateway")
	case under(from, "internal/gateway"):
		return isAPI(to) || under(to, "internal/kernel") || under(to, "internal/platform/httpkit")
	case isAPI(from):
		return under(to, "internal/kernel") || to == mod+"/internal/platform/outbox"
	case moduleOf(from) != "":
		m := moduleOf(from)
		if under(from, "internal/"+m+"/core") && under(to, "internal/"+m+"/adapters") {
			return false
		}
		if isEcosystemHTTP(to) && !under(from, "internal/artifacts/adapters/http") && ecosystemOf(to) != ecosystemOf(from+"/http") {
			return false
		}
		return moduleOf(to) == m || isAPI(to) || under(to, "internal/platform") ||
			under(to, "internal/kernel") || under(to, "pkg")
	}
	return true // app, cmd, devtools, layout
}
```

Coverage implications:

- About 190 packages at P9c, rising to about 330 at P41. Most new packages
  are module `core/` subpackages with real logic and good test surfaces.
- Packages likely to struggle with a 90% floor: thin `api/` packages (mostly
  interfaces and DTOs; exclude interface-only files or keep one small
  constructor test each), `ecosystem/<type>/http` slices for types with tiny
  protocols (nimmodel, agentpackages, skills), storage-skeleton modules before
  their phase, and `adapters/sql/migrations` embed packages.
- The per-module storetest split removes today's single 7,300-line suite in
  favour of thirteen smaller ones; the shared harness keeps the dialect matrix
  in one place.

## Walkthroughs

### An npm publish request

1. `internal/app/router.go`: chi router built at start-up; `artifacts/adapters/http/protocols.go` mounted `ecosystem/npm/http.Routes` under `/artifactory/api/npm/{repoKey}`.
2. `internal/platform/httpkit/middleware/`: request ID, trace, body limit.
3. `internal/access/adapters/http/authn_middleware.go`: Bearer or Basic credentials to `kernel.Principal` through `access/core/authn`.
4. `internal/platform/httpkit/baseurl/`: resolves the public base URL for links in the response.
5. `internal/artifacts/ecosystem/npm/http/publish.go`: decodes the publish document with `body.go`, extracts the tarball stream, builds an `api.DeployRequest`.
6. `internal/artifacts/api/items.go`: `Items.Deploy` (implemented by `artifacts/core/item`).
7. `internal/artifacts/core/item/item.go`: asks `access/api.Authorizer.For(ctx, principal)` for a `Decider` and checks deploy and overwrite rights.
8. `internal/platform/blob/` (configured backend): tarball written outside the transaction.
9. `internal/artifacts/ecosystem/npm/tarball.go`: `Inspect` reads `package.json` with `platform/archive`.
10. `internal/artifacts/core/item/gate.go`: `BeforeDeploy` gate chain (empty until P17).
11. `internal/platform/tx/tx.go`: `Tx` opens the unit of work through `platform/sql/dialect/unitofwork.go`.
12. `internal/artifacts/ecosystem/npm/policy.go`: `Admit` checks name, version and overwrite policy.
13. `internal/artifacts/adapters/sql/content_items.go`: item row and blob reference.
14. `internal/artifacts/ecosystem/npm/npm.go` then `index.go`: `Deployed` calls `core/pkgindex.Link` and updates dist-tags in `metadata.go`.
15. `internal/platform/outbox/outbox.go`: `Append(ItemDeployed)` in the same transaction (P11).
16. Commit; `AfterCommit` signals `platform/events` dispatch.
17. `internal/artifacts/ecosystem/npm/http/publish.go`: writes the npm success body.
18. Later, asynchronously: `notifications/subscribe.go` delivers the `deployed` webhook, `federation/core/sync/sender.go` ships the event to peers, `workers/core/subscribe.go` runs AFTER_CREATE workers.

### A BEFORE_DOWNLOAD worker blocks a download (P17)

1. `internal/artifacts/ecosystem/npm/http/packument.go` (or `artifacts/adapters/http/items.go` for a generic path): download request.
2. `internal/artifacts/core/item/format.go`: `Resolve` maps the request to an item; read permission checked through `access/api.Decider`.
3. `internal/artifacts/core/item/gate.go`: calls each registered `api.Gate.BeforeDownload`.
4. `internal/workers/core/gate.go`: finds the bindings for this repository in `binding.go`.
5. `internal/workers/core/execute.go`: builds the payload and limits from `limits.go`.
6. `internal/workers/adapters/sandbox/pool.go`: takes a healthy helper process.
7. `internal/workers/adapters/sandbox/host/` (in the helper, `artifactweave worker-host`): goja runs the transpiled worker; it returns `STOP`.
8. `internal/workers/core/execute.go`: maps `STOP` to `api.ErrRefused{Status: 403, Message: ...}`; `history.go` records the execution.
9. `internal/artifacts/core/item/gate.go`: returns the refusal; no blob is opened.
10. `internal/platform/httpkit/apierror/`: renders the refusal in the protocol's error shape.

### Adding a new package type

For a hypothetical `zigpkg` with local and remote classes:

1. `internal/artifacts/api/repo/packagetypes.go`: add `zigpkg`, its display name and its allowed classes.
2. `internal/artifacts/ecosystem/zigpkg/zigpkg.go`: `Format` embedding `generic.Handler`; `Deployed`, `Deleting`, `Deleted`.
3. `internal/artifacts/ecosystem/zigpkg/inspect.go`, `policy.go`, `index.go`: `Inspect`, `Admit`, package index.
4. `internal/artifacts/ecosystem/zigpkg/remote.go`: upstream index and archive rules over `core/port.Upstream`.
5. `internal/artifacts/ecosystem/zigpkg/zigpkg_test.go`, `formattest_test.go`, `testdata/`.
6. `internal/artifacts/ecosystem/zigpkg/http/zigpkg.go`, `index.go`, `publish.go`, `remote.go`, and their tests.
7. `internal/artifacts/ecosystem/registry.go`: register the format.
8. `internal/artifacts/adapters/http/protocols.go`: mount `zigpkg/http.Routes`.
9. `test/server/artifacts/zigpkg/`: server-assembly suite.
10. `test/acceptance/features/zigpkg.feature`, `zigpkg_steps_test.go`, `test/acceptance/clients/zig.Dockerfile`.
11. `docs/implementation/package-format-inventory.json` and the regenerated matrix; `tools/citestselection/test_ownership.json`.

No other module changes. Nothing outside `internal/artifacts`, the tests and the inventory is touched.

### Adding a new business domain: Evidence (P23)

1. `api/src/evidence/info.yaml`, `paths-evidence.yaml`, `components-common.yaml`: the contract.
2. `api/oapi-server-evidence.yaml` (output `internal/platform/gen/evidence`) and `api/oapi-client-evidence.yaml` (output `pkg/client/evidence`); `make generate`.
3. `internal/evidence/module.go`: `New(Deps{Tx, SQL, Outbox, Bus, Authz accessapi.Authorizer, Keys accessapi.Keys, Items artifactsapi.Items, Releases releasesapi.Bundles})`.
4. `internal/evidence/api/evidence.go`: `Evidence` (Attach, List, Verify) for governance and the OneModel gateway; `api/events.go`: `EvidenceAttached`.
5. `internal/evidence/core/model/`: Evidence, Subject, Signer.
6. `internal/evidence/core/attest.go`, `verify.go` (DSSE), `signers.go`, `subject.go`, `policy.go` (retention, deletion, copy and promotion rules).
7. `internal/evidence/core/subscribe.go`: subscribes to `artifacts.ItemCopied`, `ItemDeleted`, `releases.Promoted` to apply the evidence policy.
8. `internal/evidence/core/port/store.go`, `adapters/sql/` with `migrations/{sqlite,postgres,mysql}/0001_evidence.sql`, `adapters/inmem/`, `adapters/storetest/`.
9. `internal/evidence/adapters/http/strict_evidence.go`: implements the generated interface.
10. `internal/app/modules.go`: construct the module; `internal/app/router.go`: mount its strict server.
11. `internal/layout/layout_test.go`: add `evidence` to `modules`.
12. `internal/gateway/onemodel/evidence.go` (when P26 lands): the evidence subgraph resolver over `evidence/api`.
13. `test/server/evidence/`, `test/acceptance/features/evidence.feature`, inventory and ownership files.

No existing module's `core/` changes. `artifacts` gains nothing; evidence
hears about copies and deletes through events.

## Package mapping

Pattern rows cover the uniform cases; exception rows follow them.

| Today's package | New location | Notes |
| --- | --- | --- |
| `cmd/artifactweave` | `cmd/artifactweave` | adds hidden `worker-host` subcommand (P17) |
| `api` | `api` | only `oapi-server-*.yaml` output paths change |
| `internal/app` | `internal/app` | split into run, platform, modules, router, bootstrap, seed; gc and remote wiring move into the artifacts module; remote fault tests move to artifacts |
| `internal/archive` | `internal/platform/archive` | shared by artifacts, security (P26) |
| `internal/auth` | `internal/access/core/authn` | request middleware part moves to `access/adapters/http` |
| `internal/auth/idtoken` | `internal/access/core/oidc` | joins the exchange service |
| `internal/auth/idtoken/idtokentest` | `internal/access/adapters/oidc/oidctest` | |
| `internal/auth/token` | `internal/access/core/token` | |
| `internal/authz` | `internal/access/core/authz` | published as `access/api.Authorizer` and `Decider` |
| `internal/buildinfo` | `internal/platform/buildinfo` | |
| `internal/config` | `internal/app/config` | one Settings field per module; module settings types live in each `api/settings.go` |
| `internal/crypt` | `internal/platform/crypt` | |
| `internal/descriptor` | `internal/configuration/core/descriptor` | calls artifacts and access through `api/` |
| `internal/fakeupstream` | `internal/artifacts/adapters/upstream/fakeupstream` | |
| `internal/format` (format.go, sidecar.go, snapshots.go) | `internal/artifacts/core/format` | the contract |
| `internal/format/<ecosystem>` (all other format packages) | `internal/artifacts/ecosystem/<type>` | top of the slice |
| `internal/format/formats` | `internal/artifacts/ecosystem/registry.go` | one file, no longer a package |
| `internal/format/formattest` | `internal/artifacts/core/formattest` | |
| `internal/format/generic` | `internal/artifacts/ecosystem/generic` | |
| `internal/format/docker` | `internal/artifacts/ecosystem/docker` | also oci and helmoci |
| `internal/format/golang` | `internal/artifacts/ecosystem/golang` | package type `go` |
| `internal/format/terraform`, `terraform/terraformtest` | `internal/artifacts/ecosystem/terraform`, `terraform/terraformtest` | |
| `internal/format/terraformbackend` | `internal/artifacts/ecosystem/terraformbackend` | state service stays in `core/terraformstate` |
| `internal/format/gitlfs` | `internal/artifacts/ecosystem/gitlfs` | |
| `internal/format/huggingface` | `internal/artifacts/ecosystem/huggingface` | package type `huggingfaceml` |
| `internal/keymaterial` | `internal/access/core/keymaterial` | |
| `internal/layout` | `internal/layout` | rules rewritten as above |
| `internal/openapibundle` | `internal/devtools/openapibundle` | |
| `internal/reindexauth` | `internal/artifacts/core/reindexauth` | uses `access/api` |
| `internal/remote` (generic decision files) | `internal/artifacts/core/remote` | fetch, policy, flight, circuit, rules, destination, discovery, module_location |
| `internal/remote` (smart_*.go) | `internal/artifacts/core/remote/smart` | |
| `internal/remote` (transport files) | `internal/artifacts/adapters/upstream` | client, pool, credentials, security, url, protocol, listing*, registry_challenge, registry_token |
| `internal/remote` (format files) | `internal/artifacts/ecosystem/{golang,maven,docker,nuget}/remote*.go` | golang*, maven*, registry_{children,content,descriptors,pages}, registry, nuget_empty_feed |
| `internal/repos` | `internal/artifacts/core/repos` | published as `api.Repositories` |
| `internal/server` (root, 65 files) | see the server-root table | spread over app, platform/httpkit, artifacts, access, configuration |
| `internal/server/<protocol>` (all other protocol packages) | `internal/artifacts/ecosystem/<type>/http` | bottom of the slice |
| `internal/server/apierror` | `internal/platform/httpkit/apierror` | |
| `internal/server/baseurl` | `internal/platform/httpkit/baseurl` | |
| `internal/server/gen/<spec>` (7) | `internal/platform/gen/<spec>` | |
| `internal/server/pkgroute` | `internal/artifacts/adapters/http/pkgroute` | router kit for native protocols |
| `internal/server/docker` | `internal/artifacts/ecosystem/docker/http` | /v2, tokens, subdomain routing |
| `internal/server/golang` | `internal/artifacts/ecosystem/golang/http` | |
| `internal/server/vcs` | `internal/artifacts/ecosystem/vcs/http` | joined by vcs_*.go from the server root |
| `internal/server/nimmodel`, `jetbrainsplugins` | `internal/artifacts/ecosystem/{nimmodel,jetbrainsplugins}` with `http/` | slices with protocol and little or no format code |
| `internal/service/build` | `internal/artifacts/core/build` | |
| `internal/service/gc` | `internal/artifacts/core/gc` | |
| `internal/service/identity` | `internal/access/core/identity` | |
| `internal/service/item` | `internal/artifacts/core/item` | gains gate.go and events.go |
| `internal/service/multipart` | `internal/artifacts/core/multipart` | |
| `internal/service/oidc` | `internal/access/core/oidc` | |
| `internal/service/permission` | `internal/access/core/permission` | |
| `internal/service/pkgindex` | `internal/artifacts/core/pkgindex` | |
| `internal/service/registry` | `internal/artifacts/core/registry` | |
| `internal/service/roles` | `internal/access/core/roles` | |
| `internal/service/terraformstate` | `internal/artifacts/core/terraformstate` | |
| `internal/service/trash` | `internal/artifacts/core/trash` | |
| `internal/service/usage` | `internal/artifacts/core/usage` | |
| `internal/service/work` | `internal/platform/work` | |
| `internal/source` | `internal/artifacts/core/remote/source` | |
| `internal/specoracle` | `internal/devtools/specoracle` | |
| `internal/sqlstore/sqlcommon` | `internal/platform/sql/dialect` (dialect, unitofwork, migrate, store) and each module's `adapters/sql` (entity files) | split by table owner |
| `internal/sqlstore/sqlite`, `postgres` | `internal/platform/sql/{sqlite,postgres}` | migrations split per module stream |
| `internal/sqlstore/sqltest` | `internal/platform/sql/sqltest` | `RunModule` harness |
| `internal/stubgen` | `internal/devtools/stubgen` | |
| `internal/testfault` | `internal/platform/testkit/fault` | |
| `pkg/aql` | `internal/artifacts/core/aql` and `internal/artifacts/adapters/sql/aqlsql` | language versus dialect compiler |
| `pkg/aql/aqltest` | `internal/artifacts/core/aql/aqltest` | |
| `pkg/blob` and backends, `mirror`, `session`, `blobtest` | `internal/platform/blob/...` | |
| `pkg/cdn`, `pkg/cdn/cdntest` | `internal/platform/cdn`, `cdntest` | |
| `pkg/client/<spec>` (7) | `pkg/client/<spec>` | unchanged |
| `pkg/containertest` | `pkg/containertest` | unchanged |
| `pkg/layout` | `internal/artifacts/core/layout` | published through `api.Layouts` |
| `pkg/repo` | `internal/artifacts/api/repo` | other modules read repository configuration |
| `pkg/store` | split: `platform/tx` (Tx, AfterCommit), `platform/work`, `platform/outbox`, `kernel/page.go`, each module's `core/port/store.go` | the single `Store` aggregate disappears |
| `pkg/store/inmem` | each module's `adapters/inmem` | |
| `pkg/store/storetest` | each module's `adapters/storetest` | |
| `pkg/types` | `kernel` (principal, version) and each module's `core/model` | by entity owner, as listed in the tree |
| `pkg/types/wire` | `internal/kernel/wire` | |
| `test/acceptance` | `test/acceptance` | unchanged |
| `test/platform` | `test/platform` | unchanged |
| `test/server/<ecosystem>` (all ecosystem suites) | `test/server/artifacts/<ecosystem>` | |
| `test/server/support`, `shared`, `agentcommon`, `registryfixture` | `test/server/support/...` | one shared support tree |
| `tools/*` | `tools/*` | unchanged; ownership and selection keys updated |

## Numbers

Estimates, from the inventory and the roadmap.

| Measure | P9c | P41 |
| --- | --- | --- |
| Go packages | about 190 | about 330 |
| Modules | 4 full plus 5 storage skeletons | 13 plus 2 gateways |
| Dependency rules | 5, plus the ecosystem-http rule | same |
| Interfaces a new package type implements | `format.Handler` (embedding `generic`), optionally `Inspector`, `Admitter`, `Resolver`, `Reindexer`, `virtual.Merger` | same |
| Files touched to add a package type | about 15 new, 3 edited | same |
| Files touched to add a domain | about 25 new, 4 edited (`app/modules.go`, `app/router.go`, layout test, inventory) | same |
| Largest package | `artifacts/adapters/http`, about 12k lines (handlers from today's server root) | same, unless split by family |
| Largest module | `artifacts`, about 120k hand-written lines | about 60% of hand-written code at P9c, falling to about 40% at P41 |

## Placement choices and friction

Placement choices settled in this file:

- **Public `pkg/` shrinks to clients and container-test gating.** Store,
  blob, CDN, AQL, layout and types become internal and owned by one module
  or the platform. The alternative, keeping them public, would publish module
  internals and fight rule 1.
- **Blob and CDN live in platform, not artifacts.** Retention's cold tier
  (P16), evidence payloads (P23) and security reports (P26) store content too.
- **The unit of work is platform-level and context-carried.** A module's
  store joins whatever transaction is on the context, which keeps today's
  `Tx(ctx, fn)` model and lets `configuration` wrap several modules' `api`
  calls in one transaction for descriptor apply.
- **Repository configuration is published as `artifacts/api/repo`.** Every
  module that touches repositories reads it; only artifacts writes it.
- **Per-format remote and virtual rules live in the ecosystem slice**
  (`remote.go`, `virtual.go`), and transport lives in `adapters/upstream`.
- **Storage-skeleton modules exist at P9c** for domains P1b and P2 already
  persisted, so today's working stores have an owner.
- **One migration stream per module, no cross-module foreign keys.**
- **Generated strict servers are composed by embedding**, with stubs one level
  deeper so ownership conflicts fail to compile.

Where this layout rubs against the system:

- **`internal/artifacts` is most of the code.** It holds 49 slices,
  storage, remote, virtual, builds and search. Its internal hexagon and the
  ecosystem-http rule are the only structure inside it, and
  `artifacts/adapters/http` inherits most of today's 24k-line server root.
- **Descriptor apply is a cross-module transaction.**
  `configuration/core/descriptor` calls `artifacts/api`, `access/api`,
  `federation/api` and `retention/api` inside one `platform/tx.Tx`. This is
  the one sanctioned exception to "modules write only their own tables in
  their own transactions", and it couples configuration to every module it
  configures.
- **Gates multiply.** `artifacts/api/hooks.go` and `access/api/hooks.go`
  both exist by P17, and P27's admission and P25's control points add
  variants. Several gate interfaces with different shapes are likely unless
  `kernel` gains a common decision type.
- **The storetest split loses one property.** A cross-module behaviour, such
  as a project attach changing both an access row and an artifacts row, has
  no single contract suite. It is covered only by `test/server` suites.
- **Events add operational surface.** Cursor lag, replay and dead letters
  need `operations` tooling (P38) before they are needed in incidents, and
  consumers must be idempotent by `Seq`.
- **Boundaries for evidence, releases, governance and security are drawn
  before P23 to P27 research.** The tree commits only to their existence and
  their `api/` seams; their internals are indicative.
- **`npm/http/login.go` crosses into access.** Native protocols that issue
  credentials (npm login, Docker token, Terraform `login.v1`) live in
  artifacts slices but call `access/api.Tokens`. This is legitimate but makes
  every such slice depend on access.
