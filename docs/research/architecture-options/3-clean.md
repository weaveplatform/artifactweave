# Option 3: Clean architecture, detailed layout

Companion to [ARCHITECTURE-OPTIONS.md](../ARCHITECTURE-OPTIONS.md), option 3.
Greenfield stance: no existing decision record, gate or import rule is treated
as a constraint. The roadmap in [plan.md](../docs/implementation/plan.md)
(P10 to P41) is scope. Tree annotations: `← today/path` names the origin of
code that exists now, `(P17)` marks a roadmap addition, a line with neither is
new structure for today's code.

## Summary

Four concentric rings with dependencies pointing inward only. Ring 1,
`internal/entity`, holds pure domain types and pure rules with no I/O. Ring 2,
`internal/usecase`, holds one package per operation family; each package
declares, in its own `port.go`, the interfaces it consumes, and nothing else
may define them for it. Ring 3, `internal/adapter`, converts between use cases
and the outside world: HTTP controllers and protocol handlers drive use cases,
while repositories (SQL) and gateways (blob stores, CDNs, upstreams, keyring,
sandbox, identity providers) implement the ports use cases declared. Ring 4,
`internal/infrastructure`, is frameworks and drivers: the chi router assembly,
the `http.Server` lifecycle, database driver opening, generated strict
servers, telemetry. `internal/app` is the composition root. The organising
idea is the ring a package sits in, not the subject it is about: npm's rules
and governance's rules both sit in ring 2, and the rings say nothing about
how they relate.

Dependency rules

1. `internal/entity/...` imports no `internal/usecase`, `internal/adapter`,
   `internal/infrastructure` or `internal/app` package, and no I/O standard
   library package (`net/http`, `database/sql`, `os`, `net`).
2. `internal/usecase/...` imports `internal/entity/...` and other
   `internal/usecase` packages only when the imported package is one of the
   shared contract packages `internal/usecase/format`,
   `internal/usecase/unitofwork` or `internal/usecase/event`. One use case
   never imports another use case.
3. `internal/adapter/...` imports `internal/entity` and `internal/usecase`
   packages; it never imports `internal/infrastructure` or `internal/app`.
4. `internal/infrastructure/...` imports `internal/adapter`,
   `internal/usecase` and `internal/entity`; it never imports `internal/app`.
5. Only `internal/app` and `cmd/artifactweave` import every ring.
6. `pkg/...` imports nothing under `internal/` or `cmd/`.

## Repository root

```text
artifactweave/
  cmd/
    artifactweave/              unchanged role; gains a hidden worker-host subcommand (P17)
  internal/
    entity/                     NEW ring 1 (absorbs pkg/types, pkg/repo, pkg/layout, AQL language)
    usecase/                    NEW ring 2 (absorbs internal/service, internal/format, policy halves of remote, auth, authz)
    adapter/                    NEW ring 3 (absorbs internal/server handlers, sqlstore, pkg/blob, pkg/cdn, transport half of remote)
    infrastructure/             NEW ring 4 (router assembly, server lifecycle, gen/, db open, otel)
    app/                        composition root (unchanged role, larger)
    layout/                     import-rule test, rules rewritten for rings
    devtool/                    spec and CI tooling backends (openapibundle, stubgen, specoracle)
  pkg/                          SHRINKS: only generated clients and containertest stay public
    client/
    containertest/
  api/                          unchanged: fragments in api/src, bundled specs, oapi configs (output paths change)
  test/                         unchanged shape: acceptance, server, platform, oci, terraform
  tools/                        unchanged commands; imports move to internal/devtool
  docs/                         repo-layout.md rewritten around rings
  deploy/                       unchanged
  Makefile                      generate targets point at new gen/ output paths
  .golangci.yml                 depguard rules mirror the ring test
  .testcoverage.yml             exclusions updated for infrastructure/gen and adapter/http/stub
  go.mod                        unchanged module path
```

`pkg/` shrinks because ring 1 is private in this layout: entities are the
inside of the system, and a public entity package would let other
repositories depend on the innermost ring. The generated clients stay public
because tests and the planned own client import them.

## Full package tree

```text
internal/
  entity/                                   RING 1: pure domain, no I/O
    item/                                   items, folders, checksums, properties  ← pkg/types/item.go, keys.go
    repo/                                   repository configuration model, package types, class matrix,
                                            defaults, validation, views              ← pkg/repo
    layout/                                 repository layout pattern engine         ← pkg/layout
    version/                                version comparators per ecosystem        ← pkg/types/version.go
    pkg/                                    package, version and version-file records ← pkg/types (package index types)
    build/                                  build info, promotions                   ← pkg/types/build.go
    identity/                               users, groups, tokens, scopes, principal ← pkg/types/principal.go token.go scope.go userconv.go
    permission/                             grants, actions, roles, environments     ← pkg/types/permission.go permconv.go roles.go matrix.go
    authz/                                  the permission decision as pure functions ← internal/authz (authz.go pattern.go repopatterns.go)
    aql/                                    AQL grammar, AST, reference evaluator     ← pkg/aql (all but sql.go, joinsql.go)
      aqltest/                              shared AQL corpus and expected rows      ← pkg/aql/aqltest
    remotecache/                            cache records, source identity           ← pkg/types/remotecache.go source.go
    operations/                             settings, leases, config records         ← pkg/types/operations.go config.go
    work/                                   work item record                         ← pkg/types (WorkItem)
    gitlfs/                                 lock records                             ← pkg/types/gitlfs_lock.go
    peer/                                   peers, outbox records, federation        ← pkg/types/peer.go federation.go platform.go
    enterprise/                             bundles, distribution, archive, workers  ← pkg/types/distribution.go archive.go
    replica/                                blob replica records                     ← pkg/types/replica.go
    event/                                  the event catalogue: ItemDeployed, ItemDeleted, ... (P11)
    project/                                projects, membership, quotas             ← pkg/types/project.go (P11 grows it)
    webhook/                                webhook subscriptions, criteria          ← pkg/types/webhook.go (P11)
    cleanup/                                cleanup policy, run report (P11)
    replication/                            replication config, status (P11, P37)
    release/                                release bundle v1 and v2, stages (P15, P24)
    worker/                                 worker definition, action catalogue, execution (P17)
    evidence/                               evidence, subject, DSSE envelope verification (P23)
    application/                            AppTrust applications, versions (P25)
    policy/                                 Unified Policy rules, evaluation records (P25)
    finding/                                vulnerability, licence, SBOM component (P26)
    curation/                               admission policy, exception (P27)
    estate/                                 estate and account records (P40)
    wire/                                   JFrog JSON shapes                       ← pkg/types/wire

  usecase/                                  RING 2: one package per operation family
    unitofwork/                             shared contract: UnitOfWork, AfterCommit
    event/                                  shared contract: Appender (outbox write in the caller's tx) (P11)
    format/                                 shared contract: Handler, Env, Inspector, Admitter, WriteGuard,
                                            Resolver, Annotator, Reindexer, Worker   ← internal/format (format.go sidecar.go snapshots.go)
      formats/                              picks a Handler per package type        ← internal/format/formats
      formattest/                           contract suite every format runs        ← internal/format/formattest
      generic/                              default deploy policy                   ← internal/format/generic
      <ecosystem>/                          one package per format, list below      ← internal/format/<ecosystem>
    deploy/                                 Deploy, DeployByChecksum, DeployCommitted, Upload, RecordChecksum,
                                            RecordSidecar                            ← internal/service/item (item.go sidecar.go format.go)
    mkfolder/                               MakeFolder, EnsureRoot                   ← internal/service/item/item.go
    download/                               Resolve, ranged read, redirect decision  ← internal/service/item (Resolve), internal/server/redirect.go
    remove/                                 Delete, Purge                            ← internal/service/item/remove.go
    restore/                                trash restore                            ← internal/service/item/restore.go
    trash/                                  list, empty, retention purge             ← internal/service/trash
    transfer/                               copy and move                            ← internal/service/item (transfer.go transfer_work.go)
    explode/                                archive explode, entry, files            ← internal/service/item/archive.go
    annotate/                               properties set and delete                ← internal/service/item (Annotate)
    reindex/                                Reindex, ReindexPath, queued reindex auth ← internal/service/item (Reindex), internal/reindexauth
    cache/                                  Cache, cache cleanup                     ← internal/service/item (cache.go cache_cleanup.go)
    zap/                                    metadata-only zap                        ← internal/service/item/zap.go
    gc/                                     garbage collection                       ← internal/service/gc
    multipart/                              multipart upload assembly                ← internal/service/multipart
    index/                                  package index Link, VersionOf, Prune, Sync, Reconcile ← internal/service/pkgindex
    registry/                               Docker upload sessions, manifests, promote, tag retention ← internal/service/registry
    terraformstate/                         workspaces, states, locks                ← internal/service/terraformstate
    usage/                                  storage usage                            ← internal/service/usage
    storageinfo/                            storage summary                          ← internal/server/storageinfo.go metadata_stats.go
    buildpublish/                           build publish, append, rename, delete    ← internal/service/build (build.go document.go)
    buildpromote/                           build promotion                          ← internal/service/build/promote.go
    buildretention/                         build retention                          ← internal/service/build/retention.go
    aqlquery/                               AQL find over items, builds, promotions  ← internal/server/aql.go aql_builds.go
    aqlaction/                              items.delete, properties.update          ← internal/server/aql_actions.go
    search/                                 GET searches (quick, gavc, prop, checksum, dates, layout) ← internal/server/searches.go searches_layout.go
    versionsearch/                          versions and latest version, remote=1    ← internal/server/searches_versions.go searches_upstream*.go
    transitive/                             transitive AQL                           ← internal/server/aql_transitive.go aql_transitive_peer.go
    repository/                             repository create, update, delete, list  ← internal/repos, internal/server/repositories.go
    descriptor/                             YAML descriptor apply, bootstrap         ← internal/descriptor
    system/                                 ping, version, configuration read        ← internal/server/system.go
    fetch/                                  remote retrieval decision table, source permits ← internal/remote (policy, rules, fetch, discovery), internal/source
    smartsync/                              Smart Remote synchronisation             ← internal/remote/smart*.go
    remotebrowse/                           remote listings, Smart Folder Info       ← internal/remote/listing*.go, internal/server/source_listing.go
    resolvevirtual/                         ordered member resolution (P10)
    mergemetadata/                          per-format metadata merging driver (P10)
    authenticate/                           basic, token, API key, OIDC id-token, lockout, suspension ← internal/auth, internal/auth/idtoken
    token/                                  issue, refresh, revoke, verify           ← internal/auth/token
    authorize/                              builds a Decider for a principal         ← internal/authz (New, Sources)
    oidcexchange/                           workload OIDC token exchange             ← internal/service/oidc
    user/                                   users                                    ← internal/service/identity
    group/                                  groups                                   ← internal/service/identity
    permission/                             permission targets v1, v2, Access v2     ← internal/service/permission
    role/                                   roles and environments                   ← internal/service/roles
    keypair/                                key pairs, certificates                  ← internal/keymaterial
    seed/                                   bootstrap admin and defaults             ← internal/auth/seed.go
    keyrotate/                              master key rotation, reseal              ← internal/crypt (rotation), sqlcommon/reseal.go
    project/                                projects, attach, share, quotas (P11)
    environment/                            project environments (P11)
    webhookadmin/                           webhook CRUD, test, troubleshooting (P11)
    webhookdeliver/                         delivery, signing, retries, delivery log (P11)
    cleanuppolicy/                          cleanup policy CRUD and enablement (P11)
    cleanuprun/                             scheduled and dry runs, reports (P11)
    replicate/                              single push and pull replication (P11)
    multipush/                              multi-push and event replication (P37)
    checksumreplicate/                      checksum replication (P37)
    descriptorexport/                       XML descriptor GET, export (P12)
    systemconfig/                           mail, proxies, backups, messages (P12)
    ldapsync/                               LDAP bind and group sync (P12)
    pair/                                   peer pairing and trust (P13)
    federate/                               outbound federated sync (P13)
    federationapply/                        inbound idempotent apply, full sync (P13)
    accessfederate/                         users, groups, permissions across peers (P14)
    topology/                               pfed membership, grid health (P14)
    releasebundle/                          release bundles v1 create, sign (P15)
    distribute/                             distribution rules, jobs, edge targets (P15)
    archivepolicy/                          archive policies (P16)
    archiverun/                             archive runs to the cold tier (P16)
    archiverestore/                         restore jobs and executions (P16)
    workeradmin/                            worker CRUD, enable, test (P17)
    runworker/                              hook execution, the download and deploy gates (P17)
    workerhistory/                          execution history, rerun (P17)
    health/                                 health and readiness (P18)
    metrics/                                Prometheus metrics (P18)
    login/                                  browser sessions, native approval, PKCE (P21)
    externalauth/                           SAML, human OIDC, Crowd, HTTP-SSO (P21)
    mfa/                                    MFA policy (P21)
    provision/                              SCIM users and groups, reconciliation (P22)
    deprovision/                            deactivation, revocation propagation (P22)
    attest/                                 evidence upload and association (P23)
    verifyevidence/                         DSSE verification, trusted signers (P23)
    evidencequery/                          evidence retrieval and filtering (P23)
    signerkey/                              signing-key publication and rotation (P23)
    releasev2/                              immutable release bundles v2 (P24)
    promoterelease/                         lifecycle promotion, records (P24)
    lifecycle/                              stages and transitions (P24)
    application/                            AppTrust applications and versions (P25)
    evaluatepolicy/                         Unified Policy evaluation (P25)
    approve/                                approvals and waivers (P25)
    scan/                                   indexing and scan jobs (P26)
    sbom/                                   SBOM import and export (P26)
    watch/                                  watches, violations, ignore rules (P26)
    catalog/                                Catalog labels and licences (P26)
    curate/                                 Curation admission decisions (P27)
    contextualanalysis/                     applicability and secrets analysis (P27)
    runtimeingest/                          runtime workload events (P27)
    backup/                                 scheduled backups (P36)
    importexport/                           Artifactory and Access import and export (P36)
    supportbundle/                          support bundles (P38)
    integration/                            Vault, IAM, log shipping (P38)
    queueadmin/                             persistent work retry, unpark, cancel (P38)
    estate/                                 Mission Control, MyJFrog, JFConnect (P40)
    bridge/                                 SaaS bridges (P40)

  adapter/                                  RING 3
    http/
      middleware/                           authentication, base URL, tracing hooks ← internal/server/middleware.go auth.go context.go
      apierror/                             error envelopes                         ← internal/server/apierror
      baseurl/                              base-URL resolver                        ← internal/server/baseurl
      controller/                           management API controllers implementing generated interfaces
        artifactory.go                      aggregate implementing gen/artifactory.StrictServerInterface
        storage/ search/ builds/ repositories/ system/ security/ remote/
        access/                             implements gen/access                   ← internal/server/access*.go tokens.go oidc.go ...
        webhook/                            implements gen/event (P11)
        distribution/                       implements gen/distribution (P15)
        topology/                           implements gen/grid and gen/pfed (P14)
        worker/                             implements gen/worker (P17)
        evidence/ lifecycle/ apptrust/ xray/ curation/ estate/ (P23 to P40)
      protocol/                             native package protocols, one per family
        pkgroute/                           router kit for native protocols          ← internal/server/pkgroute
        <ecosystem>/                        see list below                           ← internal/server/<protocol> and root files
      stub/                                 generated 501 stubs                     ← internal/server/stubs.go
      scim/                                 SCIM endpoints (P22)
      session/                              browser session and CSRF handling (P21)
      graphql/                              OneModel gateway resolvers (P26)
      web/                                  web console handlers and assets (P39)
    cli/                                    migrate, config apply, keys rotate presenters
    peer/                                   peer protocol server and client (P13)
    eventloop/                              reads the outbox, dispatches to consuming use cases (P11)
    scheduler/                              periodic triggers: gc, cleanup, archive, replication (P11)
    repository/                             implements use-case storage ports
      repository.go                         full Repository union, used only by repositorytest and app
      sqlcommon/                            dialect, unit of work, migrator, all tables ← internal/sqlstore/sqlcommon
      sqlite/                               SQLite driver glue, migrations/*.sql      ← internal/sqlstore/sqlite
      postgres/                             PostgreSQL driver glue, migrations/*.sql  ← internal/sqlstore/postgres
      mysql/                                MySQL dialect and migrations (P34)
      inmem/                                reference in-memory repository           ← pkg/store/inmem
      aqlsql/                               AQL compiler per dialect                  ← pkg/aql/sql.go joinsql.go
      repositorytest/                       contract suite over Repository           ← pkg/store/storetest, internal/sqlstore/sqltest
    gateway/
      blob/                                 blob.Store contract for backends          ← pkg/blob
        fs/ s3/ gcs/ azure/ mirror/ session/  backends                               ← pkg/blob/*
        cold/                               cold tier wiring (P16)
        blobtest/                           backend contract suite                    ← pkg/blob/blobtest
      cdn/                                  CloudFront, Cloud CDN, Front Door signers ← pkg/cdn
        cdntest/                                                                     ← pkg/cdn/cdntest
      upstream/                             outbound HTTP to remotes, per-format wire readers ← internal/remote (transport half)
        fakeupstream/                       controlled upstream fixtures              ← internal/fakeupstream
      keyring/                              master keys, sealed values                ← internal/crypt
      archive/                              bounded zip and tar walker                ← internal/archive
      sandbox/                              esbuild transpile, goja helper pool (P17)
      idp/                                  LDAP, SAML, OIDC, Crowd clients (P12, P21)
      vault/ kms/ iam/ logship/             external integrations (P38)
      scanner/                              analysis engines (P26, P27)
      feed/                                 vulnerability and licence intelligence (P26)
      lease/                                node leases and fencing (P35)
    testfault/                              failing fakes for handler tests           ← internal/testfault

  infrastructure/                           RING 4
    router/                                 chi mux assembly, middleware order, protocol mounts ← internal/server/server.go protocols.go handwritten.go
    httpserver/                             http.Server lifecycle, drain              ← internal/app/app.go (serve)
    database/                               open driver, run migrations              ← internal/app/store.go
    gen/                                    generated strict servers                 ← internal/server/gen
      artifactory/ access/ event/ distribution/ grid/ pfed/ worker/
      peer/ (P13) evidence/ (P23) lifecycle/ (P24) apptrust/ (P25) xray/ (P26)
    telemetry/                              OpenTelemetry and Prometheus exporters (P18)
    config/                                 strict YAML, flag > env > file           ← internal/config
    buildinfo/                              version metadata                         ← internal/buildinfo
    workerhost/                             helper-process loop for the sandbox (P17)
    trace/                                  request tracing                          ← internal/server/trace.go

  app/                                      composition root                         ← internal/app
  layout/                                   ring rules test                          ← internal/layout
  devtool/
    openapibundle/                                                                    ← internal/openapibundle
    stubgen/                                                                          ← internal/stubgen
    specoracle/                                                                       ← internal/specoracle

pkg/
  client/                                   generated Go clients                     ← pkg/client
    access/ artifactory/ distribution/ event/ grid/ pfed/ worker/
    peer/ (P13) evidence/ (P23) lifecycle/ (P24) apptrust/ (P25) xray/ (P26)
  containertest/                                                                     ← pkg/containertest
```

### Format packages, `internal/usecase/format/<ecosystem>/`

All 49 package types, with the package that holds each:

| Package type | Package | Notes |
| --- | --- | --- |
| agentpackages | `format/agentpackages` | ← internal/format/agentpackages |
| agentplugins | `format/agentplugins` | ← internal/format/agentplugins |
| aieditorextensions | `format/aieditorextensions` | ← internal/format/aieditorextensions |
| alpine | `format/alpine` | ← internal/format/alpine |
| ansible | `format/ansible` | (P9c #782) |
| bazelmodules | `format/bazelmodules` | ← internal/format/bazelmodules |
| bower | `format/bower` | ← internal/format/bower |
| cargo | `format/cargo` | ← internal/format/cargo |
| chef | `format/chef` | (P9c) |
| cocoapods | `format/cocoapods` | ← internal/format/cocoapods |
| composer | `format/composer` | ← internal/format/composer |
| conan | `format/conan` | ← internal/format/conan |
| conda | `format/conda` | ← internal/format/conda |
| cran | `format/cran` | ← internal/format/cran |
| debian | `format/debian` | ← internal/format/debian |
| docker | `format/docker` | ← internal/format/docker; also holds oci and helmoci rules |
| oci | `format/docker` | shares docker |
| helmoci | `format/docker` | shares docker; chart media types |
| gems | `format/gems` | ← internal/format/gems |
| generic | `format/generic` | ← internal/format/generic |
| gitlfs | `format/gitlfs` | ← internal/format/gitlfs |
| go | `format/golang` | ← internal/format/golang |
| gradle | `format/maven` | shares maven; Gradle layout |
| ivy | `format/maven` | shares maven; Ivy layout |
| sbt | `format/maven` | shares maven; SBT layout |
| maven | `format/maven` | ← internal/format/maven |
| helm | `format/helm` | ← internal/format/helm |
| hex | `format/hex` | ← internal/format/hex |
| huggingfaceml | `format/huggingface` | ← internal/format/huggingface |
| machinelearning | `format/machinelearning` | (P9c #789) |
| jetbrainsplugins | `format/jetbrainsplugins` | (P9c) protocol-only today |
| luarocks | `format/luarocks` | ← internal/format/luarocks |
| nimmodel | `format/nimmodel` | (P9b) protocol-only today |
| nix | `format/nix` | (P9c) |
| npm | `format/npm` | ← internal/format/npm |
| nuget | `format/nuget` | ← internal/format/nuget |
| opkg | `format/opkg` | ← internal/format/opkg |
| p2 | `format/p2` | ← internal/format/p2 |
| pub | `format/pub` | ← internal/format/pub |
| puppet | `format/puppet` | (P9c) |
| pypi | `format/pypi` | ← internal/format/pypi |
| releasebundles | `format/releasebundles` | (P15) |
| rpm | `format/rpm` | ← internal/format/rpm |
| skills | `format/skills` | ← internal/format/skills |
| swift | `format/swift` | ← internal/format/swift |
| terraform | `format/terraform` | ← internal/format/terraform, terraformtest beneath it |
| terraformbackend | `format/terraformbackend` | ← internal/format/terraformbackend |
| vagrant | `format/vagrant` | (P9c) |
| vcs | `format/vcs` | (P9c) protocol-only today |

### Protocol packages, `internal/adapter/http/protocol/<ecosystem>/`

One package per native protocol family. Today's 27 protocol packages move
across unchanged in name; the per-format files in today's `internal/server`
root join them:

```text
protocol/
  agentpackages/      ← internal/server/agentpackages
  agentplugins/       ← internal/server/agentplugins.go
  aieditorextensions/ ← internal/server/aieditorextensions
  alpine/             ← internal/server/alpine.go
  bazelmodules/       ← internal/server/bazelmodules
  bower/              ← internal/server/bower
  cargo/              ← internal/server/cargo
  cocoapods/          ← internal/server/cocoapods
  composer/           ← internal/server/composer
  conan/              ← internal/server/conan
  conda/              ← internal/server/conda
  cran/               ← internal/server/cran.go cran_upload.go
  debian/             ← internal/server/debian.go debian_operations.go debian_snapshot.go debian_virtual.go
  docker/             ← internal/server/docker, docker_rest.go (also OCI and Helm OCI)
  gems/               ← internal/server/gems
  gitlfs/             ← internal/server/gitlfs
  golang/             ← internal/server/golang
  helm/               ← internal/server/helm
  hex/                ← internal/server/hex
  huggingface/        ← internal/server/huggingface
  jetbrainsplugins/   ← internal/server/jetbrainsplugins
  luarocks/           ← internal/server/luarocks
  maven/              ← internal/server/maven.go (Maven, Gradle, Ivy, SBT)
  nimmodel/           ← internal/server/nimmodel
  npm/                ← internal/server/npm, npm_auth.go npm_operations.go
  nuget/              ← internal/server/nuget
  opkg/               ← internal/server/opkg.go
  p2/                 ← internal/server/p2.go p2_external.go
  pub/                ← internal/server/pub
  pypi/               ← internal/server/pypi, pypi_routes.go
  rpm/                ← internal/server/rpm.go
  skills/             ← internal/server/skills
  swift/              ← internal/server/swift
  terraform/          ← internal/server/terraform
  terraformbackend/   ← internal/server/terraformbackend
  vcs/                ← internal/server/vcs, vcs_download.go vcs_nested.go vcs_routes.go
  ansible/ chef/ puppet/ vagrant/ nix/ machinelearning/  (P9c)
```

### Where today's 65 `internal/server` root files go

| Family | Today's files | New location |
| --- | --- | --- |
| Framework | server.go protocols.go handwritten.go | `infrastructure/router/` |
| Framework | middleware.go auth.go context.go | `adapter/http/middleware/` |
| Framework | trace.go | `infrastructure/trace/` |
| Framework | stubs.go | `adapter/http/stub/` |
| Storage | items.go storage.go transfer.go trash.go multipart.go redirect.go source.go | `adapter/http/controller/storage/` |
| Storage summary | storageinfo.go metadata_stats.go | `adapter/http/controller/storage/` (use case `storageinfo`) |
| Remote | source_listing.go smart_status.go reverseproxy.go | `adapter/http/controller/remote/` |
| AQL | aql.go aql_actions.go aql_builds.go aql_transitive.go aql_transitive_peer.go | `adapter/http/controller/search/` |
| Searches | searches.go searches_builds.go searches_layout.go searches_versions.go searches_upstream.go searches_upstream_maven.go searches_upstream_routes.go | `adapter/http/controller/search/` |
| Builds | builds.go | `adapter/http/controller/builds/` |
| Repositories | repositories.go reindex.go descriptor.go | `adapter/http/controller/repositories/` |
| System | system.go | `adapter/http/controller/system/` |
| Access | access_identity.go access.go tokens.go oidc.go roles.go permissions.go | `adapter/http/controller/access/` |
| Security | security.go keypairs.go | `adapter/http/controller/security/` |
| Formats | agentplugins.go alpine.go cran.go cran_upload.go debian.go debian_operations.go debian_snapshot.go debian_virtual.go docker_rest.go maven.go npm_auth.go npm_operations.go opkg.go p2.go p2_external.go pypi_routes.go rpm.go vcs_download.go vcs_nested.go vcs_routes.go | `adapter/http/protocol/<ecosystem>/` as listed above |

## Deep dives

### npm ecosystem

```text
internal/usecase/format/npm/                       ring 2: npm's rules
  npm.go             Handler: Deployed, Deleting, Deleted; registers Inspector and Admitter  ← format/npm/npm.go
  tarball.go         Inspector: reads package.json from the tarball via the ArchiveReader port ← format/npm/tarball.go
  policy.go          Admitter and WriteGuard: overwrite, scope and strict-layout rules       ← format/npm/policy.go
  index.go           links tarballs to package versions through format.Env index writer       ← format/npm/index.go
  metadata.go        packument model and version documents                                   ← format/npm/metadata.go
  latest.go          dist-tag latest computation                                             ← format/npm/latest.go
  search.go          search scoring over the index                                           ← format/npm/search.go
  work.go            format.Worker: strict-layout conversion, reindex                        ← format/npm/work.go
  remote.go          fetch.MetadataRewriter: packument rewriting for remotes                 ← internal/remote (npm parts), server/npm/remote_*.go rules
  merge.go           mergemetadata.Merger: virtual packument merge (P10)
  port.go            ArchiveReader, PackumentStore: the ports npm itself consumes
  npm_test.go index_test.go policy_test.go tarball_test.go work_test.go
  contract_test.go   runs format/formattest.Run with the npm fixture
  testdata/          tarballs, packuments

internal/adapter/http/protocol/npm/                ring 3: npm's wire protocol
  routes.go          mounts under /artifactory/api/npm/{repo}                                ← server/npm/npm.go
  body.go            request body limits and decoding                                        ← server/npm/body.go
  packument.go       GET packument, abbreviated and full                                     ← server/npm/packument.go
  publish.go         PUT publish; builds deploy.Input per attachment                         ← server/npm/publish.go
  update.go          deprecate, unpublish, owner changes                                     ← server/npm/update.go
  disttags.go        dist-tag endpoints                                                      ← server/npm/disttags.go
  search.go          /-/v1/search                                                            ← server/npm/search.go
  user.go            legacy login and logout                                                 ← server/npm/user.go, server/npm_auth.go
  npmrc.go           api/npm/auth and .npmrc                                                 ← server/npm/npmrc.go
  operations.go      reindex and conversion triggers                                         ← server/npm_operations.go
  remote.go          remote GET path: calls usecase/fetch                                    ← server/npm/remote.go
  remote_dist.go     tarball streaming for remote reads                                      ← server/npm/remote_dist.go
  remote_integrity.go integrity header checks on delivery                                    ← server/npm/remote_integrity.go
  remote_validate.go  admission of upstream bodies before caching                            ← server/npm/remote_validate.go
  presenter.go       maps use-case errors to npm JSON errors
  *_test.go          handler tests with use-case fakes (narrow ports)

test/server/npm/                                   server-assembly suites                     ← test/server/npm
test/acceptance/features/npm.feature npm_remote.feature npm_search_auth.feature npm_overwrites_and_settings.feature
test/acceptance/npm_steps_test.go
test/acceptance/clients/node.Dockerfile node-npm12.Dockerfile
```

npm's remote behaviour splits three ways. The decision table (cache hit,
revalidate, offline) is `usecase/fetch`. The packument rewriting rule is
`usecase/format/npm/remote.go`. The HTTP transport and upstream body reading
is `adapter/gateway/upstream`. Today these live together in `internal/remote`
and `internal/server/npm`.

### Deploy write path

```text
internal/usecase/unitofwork/
  unitofwork.go      UnitOfWork { Do(ctx, fn func(ctx) error) error; AfterCommit(ctx, fn func()) }

internal/usecase/deploy/
  port.go            BlobWriter, ItemWriter, ItemReader, RepoReader, Clock, WorkEnqueuer,
                     DeployGate (P17, P27), event.Appender (P11)
  deploy.go          Interactor.Deploy                                ← service/item/item.go Deploy
  checksum.go        Interactor.DeployByChecksum                       ← service/item/item.go DeployByChecksum
  committed.go       Interactor.DeployCommitted (upload sessions)      ← service/item/item.go DeployCommitted
  upload.go          Interactor.Upload (blob only, before a tx)        ← service/item/item.go Upload
  sidecar.go         RecordChecksum, RecordSidecar, Sidecars           ← service/item/sidecar.go, item.go
  env.go             builds format.Env (Derive, Underive, Remove, Enqueue, Move) from its ports ← service/item/format.go
  errors.go          OverwriteDeniedError, ErrExists, SidecarError     ← service/item/item.go
  deploy_test.go checksum_test.go sidecar_test.go env_test.go

internal/usecase/remove/      port.go remove.go remove_test.go        ← service/item/remove.go
internal/usecase/restore/     port.go restore.go restore_test.go      ← service/item/restore.go
internal/usecase/transfer/    port.go transfer.go work.go *_test.go   ← service/item/transfer.go transfer_work.go
internal/usecase/explode/     port.go explode.go entry.go *_test.go   ← service/item/archive.go
internal/usecase/annotate/    port.go annotate.go annotate_test.go    ← service/item/item.go Annotate
internal/usecase/reindex/     port.go reindex.go authorize.go *_test.go ← service/item Reindex, internal/reindexauth
internal/usecase/cache/       port.go cache.go cleanup.go smart.go *_test.go ← service/item/cache.go cache_cleanup.go smart.go
internal/usecase/zap/         port.go zap.go zap_test.go              ← service/item/zap.go
internal/usecase/index/       port.go link.go reconcile.go *_test.go  ← service/pkgindex
internal/usecase/buildpublish/ port.go publish.go document.go *_test.go ← service/build/build.go document.go
internal/usecase/registry/    port.go sessions.go manifests.go indexes.go platforms.go deletes.go
                              promote.go retention.go twins.go *_test.go testdata/ ← service/registry
internal/usecase/trash/       port.go trash.go trash_test.go          ← service/trash
internal/usecase/gc/          port.go gc.go gc_test.go                ← service/gc
internal/usecase/multipart/   port.go multipart.go *_test.go          ← service/multipart

internal/adapter/repository/sqlcommon/
  unitofwork.go      implements unitofwork.UnitOfWork with ctx-carried tx  ← sqlcommon/unitofwork.go
  content_items.go   satisfies deploy.ItemWriter, remove.ItemDeleter, ...  ← sqlcommon/content_items.go
  content_index.go   satisfies index.PackageWriter                          ← sqlcommon/content_index.go
  outbox.go          satisfies event.Appender in the caller's tx (P11)
```

Seven use cases call format hooks (deploy, remove, restore, transfer, annotate,
reindex, cache). That is why `usecase/format` is a shared contract package
rather than a port each declares: five copies of `Env` would drift.

```go
// internal/usecase/deploy/deploy.go
package deploy

func (uc *Interactor) Deploy(ctx context.Context, in Input) (item.Item, error) {
	cfg, err := uc.repos.Repo(ctx, in.RepoKey)
	if err != nil {
		return item.Item{}, err
	}
	h := uc.formats(cfg.PackageType)
	info, err := uc.blobs.Put(ctx, in.Body) // content first, outside the tx
	if err != nil {
		return item.Item{}, fmt.Errorf("store content: %w", err)
	}
	insp, err := format.Inspect(ctx, h, uc.env(cfg), cfg, in.Path, info)
	if err != nil {
		return item.Item{}, err
	}
	if err := uc.gate.BeforeDeploy(ctx, cfg, in.Path, in.By); err != nil { // P17, P27
		return item.Item{}, err
	}
	var it item.Item
	err = uc.tx.Do(ctx, func(ctx context.Context) error {
		if err := format.Admit(ctx, h, uc.env(cfg), cfg, format.Write{Path: in.Path}); err != nil {
			return err
		}
		if it, err = uc.items.Put(ctx, item.New(cfg.Key, in.Path, info, in.Properties, in.By)); err != nil {
			return err
		}
		env := uc.env(cfg)
		env.Inspection = insp
		if err := h.Deployed(ctx, env, cfg, it); err != nil { // index, derive, enqueue
			return err
		}
		return uc.events.Append(ctx, event.ItemDeployed{Repo: cfg.Key, Path: in.Path, By: in.By}) // P11
	})
	return it, err
}
```

### Access and identity

```text
internal/entity/identity/      principal.go token.go scope.go user.go group.go convert.go
internal/entity/permission/    grants.go actions.go roles.go environments.go convert.go
internal/entity/authz/         decide.go (Allows, AllowsFolder, Holds, Grantable, IsRepoVisible as pure
                               functions over a Subject and rules) pattern.go repopatterns.go ← internal/authz

internal/usecase/authenticate/
  port.go            UserReader, TokenVerifier, PasswordPolicy, LockoutWriter, IDTokenVerifier
  authenticator.go   Basic, Bearer, API key, anonymous                  ← auth/authenticator.go
  current.go         current principal recheck                          ← auth/current.go current_registry.go
  password.go policy.go suspension.go deferred.go download.go           ← auth/*.go
  idtoken.go         id-token verification against JWKS                 ← auth/idtoken
internal/usecase/token/        port.go issue.go verify.go keys.go jwt.go  ← auth/token
internal/usecase/authorize/
  port.go            GrantSource (permissions, groups, roles, repos)
  authorize.go       Decider(ctx, principal) (*authz.Decider, error)    ← authz.New + Sources
internal/usecase/oidcexchange/ port.go exchange.go match.go pattern.go   ← service/oidc
internal/usecase/user/ group/ permission/ role/ keypair/ seed/ keyrotate/
internal/usecase/project/ environment/          (P11)
internal/usecase/ldapsync/                      (P12)
internal/usecase/login/ externalauth/ mfa/      (P21)
internal/usecase/provision/ deprovision/        (P22)

internal/adapter/http/middleware/authn.go   calls authenticate.Interactor, stores Principal in ctx
internal/adapter/http/middleware/authz.go   calls authorize.Interactor once per request, stores Decider in ctx
internal/adapter/http/controller/access/    implements gen/access: users, groups, tokens, roles, projects
internal/adapter/http/controller/security/  legacy security API, key pairs
internal/adapter/http/scim/                 (P22)
internal/adapter/http/session/              (P21)
internal/adapter/gateway/idp/ldap/ saml/ oidc/ crowd/   (P12, P21)
internal/adapter/gateway/keyring/           sealed columns, master keys  ← internal/crypt
```

A protocol handler gets the principal and Decider from the request context,
which the middleware filled. Use cases that must authorise themselves (queued
reindex, federated apply) declare an `Authorizer` port and receive
`authorize.Interactor` through wiring.

### Storage and persistence

```text
internal/adapter/repository/
  repository.go          Repository: the union of every storage port, for repositorytest and app only
                         ← pkg/store/store.go (Store, Tx, AfterCommit)
  sqlcommon/             ← internal/sqlstore/sqlcommon (24 files)
    dialect.go migrate.go unitofwork.go store.go
    content.go content_items.go content_index.go blobs.go replicas.go repository_headers.go
    identity.go identity_keys.go builds.go build_search.go gitlfs_locks.go remotecache.go
    operations.go operations_cleanup.go operations_config.go operations_webhooks.go
    peers.go enterprise.go work.go reseal.go
    outbox.go (P11) projects.go (P11) workers.go worker_executions.go (P17)
    evidence.go (P23) releases.go (P24) policy.go (P25) findings.go (P26)
  sqlite/                sqlite.go, migrations/0001_content.sql ... 0017_gitlfs_locks.sql
  postgres/              postgres.go, migrations/0001_content.sql ... 0017_gitlfs_locks.sql
  mysql/                 mysql.go, migrations/ (P34)
  inmem/                 ← pkg/store/inmem (17 files: blobs.go build_search.go builds.go content.go ...)
  aqlsql/                sql.go joinsql.go: compiles entity/aql.Query to a dialect ← pkg/aql
  repositorytest/        ← pkg/store/storetest (15 files) + internal/sqlstore/sqltest (4 files)
    core.go content.go identity.go builds.go search.go properties.go peers.go enterprise.go
    operations.go remotecache.go replicas.go work.go gitlfs_locks.go layout_sources.go harness.go

internal/adapter/gateway/blob/
  blob.go                Store contract every backend satisfies ← pkg/blob
  fs/ s3/ gcs/ azure/    backends ← pkg/blob/*
  mirror/                mirror.go read.go repair.go status.go upload.go ← pkg/blob/mirror
  session/               upload sessions in the uploads table ← pkg/blob/session
  cold/                  cold tier selection (P16)
  blobtest/              ← pkg/blob/blobtest
internal/adapter/gateway/cdn/  cloudfront.go cloudcdn.go frontdoor.go signer.go, cdntest/ ← pkg/cdn
```

There is no central store interface for use cases. `pkg/store`'s 34
interfaces dissolve into ports: `deploy/port.go` declares the four item
methods it uses, `gc/port.go` the blob-index methods it uses, and so on. The
SQL store is one concrete type that satisfies all of them. `repository.go`
keeps a union interface only so the contract suite and the in-memory store
have one target. Tables and migrations are owned by the repository ring as a
whole; no use case owns its tables.

### Remote and virtual repositories

```text
internal/usecase/fetch/                  retrieval decisions                 ← internal/remote, internal/source
  port.go          Upstream (Get, Head, List), CacheWriter, CacheReader, Permitter
  source.go        Resource, Request, Permit, Result, Representation, Stream ← source/source.go
  gitlfs.go        Git LFS action resolution                                ← source/gitlfs_action.go
  policy.go rules.go          retrieval decision table, negative caching     ← remote/policy.go rules.go
  fetch.go discovery.go       HEAD-before-GET, assumed-offline, discovery    ← remote/fetch.go discovery.go
  maven.go maven_eager.go     Maven retrieval decisions                      ← remote/maven.go maven_eager.go
  golang.go module_location.go  Go module resolution                         ← remote/golang.go module_location.go
  registry.go                 registry pull decisions                        ← remote/registry.go
internal/usecase/smartsync/   smart.go smart_discovery.go smart_properties.go smart_queue.go smart_statistics.go
                              smart_status.go smart_trace.go               ← remote/smart*.go
internal/usecase/transitive/  smart_aql.go transitive.go                   ← remote/smart_aql.go, server/aql_transitive*.go
internal/usecase/remotebrowse/ listing.go listing_mapped.go listing_smart.go ← remote/listing*.go
internal/usecase/resolvevirtual/ port.go resolve.go deploy_target.go (P10)
internal/usecase/mergemetadata/  port.go merge.go cache.go (P10); Merger implemented per format

internal/adapter/gateway/upstream/      transport and wire reading         ← internal/remote (transport half)
  client.go pool.go circuit.go flight.go credentials.go destination.go security.go url.go
  registry_challenge.go registry_token.go registry_content.go registry_descriptors.go
  registry_pages.go registry_children.go protocol.go golang_zip.go nuget_empty_feed.go
  listing_html.go smart_peer.go smart_request.go
  fakeupstream/                                                        ← internal/fakeupstream
```

### Workers (P17), a roadmap domain end to end

```text
internal/entity/worker/           worker.go action.go execution.go limits.go (64 MiB, 1 s CPU, 5 s call, 20 s total)
internal/usecase/workeradmin/     port.go admin.go test.go           CRUD, enable, test endpoint
internal/usecase/runworker/
  port.go          Catalogue (bindings by action), Sandbox (Execute), ExecutionLog, HTTPAllowList
  gate.go          DownloadGate and DeployGate implementations: run BEFORE_* workers, map STOP to entity errors
  after.go         AFTER_CREATE and other AFTER_* handlers fed by the event loop
  run.go           execute one worker with limits, record execution
internal/usecase/workerhistory/   port.go history.go rerun.go
internal/usecase/download/port.go DownloadGate { BeforeDownload(ctx, Request) error }
internal/usecase/deploy/port.go   DeployGate   { BeforeDeploy(ctx, repo, path, by) error }
internal/adapter/gateway/sandbox/
  transpile.go     esbuild TypeScript to JavaScript
  pool.go          helper process pool, one execution per helper
  protocol.go      framing between server and helper
  sandbox_test.go  limit and crash tests
internal/infrastructure/workerhost/ host.go goja.go   the helper-process loop, no host access
cmd/artifactweave/main.go           hidden worker-host subcommand calls workerhost.Run
internal/adapter/http/controller/worker/  implements gen/worker (v1 and v2 endpoints)
internal/adapter/repository/sqlcommon/workers.go worker_executions.go + migrations
api/src/worker/paths-workers.yaml   bundled into api/worker.openapi.yaml
```

The core offers the hook through ports it declares. `download` declares
`DownloadGate`; `runworker` has a method set that satisfies it without
importing `download`; `app` assigns one to the other. This is where clean
architecture's per-consumer interfaces work best.

### Webhooks and events (P11, P13)

```text
internal/entity/event/          catalogue.go (exact JFrog event names), item.go build.go release.go ...
internal/usecase/event/         appender.go: Appender { Append(ctx, event.Event) error } (shared contract)
internal/adapter/repository/sqlcommon/outbox.go   writes the outbox row in the caller's transaction
internal/adapter/eventloop/
  loop.go          reads the outbox in order via the work queue, acknowledges per consumer
  registry.go      Consumer { Kinds() []string; Handle(ctx, event.Event) error }
internal/usecase/webhookdeliver/ port.go deliver.go sign.go retry.go log.go   Consumer for webhooks
internal/usecase/federate/       port.go federate.go                        Consumer for peer sync
internal/usecase/runworker/after.go                                          Consumer for AFTER_* workers
internal/usecase/scan/           Consumer for deploy-triggered scans (P26)
internal/app/events.go           which use case consumes which event kinds
```

Producers append through the shared `event.Appender` port inside their own
transaction. Consumers are ordinary use cases registered in `app/events.go`.
Nothing in the tree shows which domain reacts to which event; the reader has
to open `app/events.go`.

### Composition root

```text
internal/app/
  app.go         Run, run, serve: lifecycle around infrastructure/httpserver  ← app/app.go
  runtime.go     the runtime struct: repository, blobs, queue, background loops ← app/runtime.go
  store.go       open repository via infrastructure/database                    ← app/store.go
  blob.go        build blob gateways, mirror, cold tier                          ← app/blob.go
  mirrors.go     mirror repair loop                                              ← app/mirrors.go
  remote.go      upstream gateway, fetch, smartsync                              ← app/remote.go
  gc.go          gc use case and its schedule                                    ← app/gc.go
  instance.go    instance identity                                               ← app/instance.go
  descriptor.go  bootstrap descriptor apply                                      ← app/descriptor.go
  seed.go        seed use case                                                   ← app/seed.go
  usecases.go    constructs every interactor with its ports
  controllers.go constructs controllers and protocol handlers, hands them to the router
  events.go      outbox consumers (P11)
  workers.go     sandbox pool and gates (P17)
```

```go
// internal/app/usecases.go (P9c shape)
func newUseCases(rt *runtime) *useCases {
	formats := formats.For
	return &useCases{
		Deploy: deploy.New(deploy.Ports{
			Tx: rt.repo, Items: rt.repo, Repos: rt.repo, Blobs: rt.blobs,
			Formats: formats, Work: rt.work, Clock: rt.now,
			Gate: deploy.AllowAll{}, Events: event.Discard{},
		}),
		Download: download.New(download.Ports{
			Items: rt.repo, Blobs: rt.blobs, Signer: rt.cdn, Gate: download.AllowAll{},
		}),
		Remove:    remove.New(remove.Ports{Tx: rt.repo, Items: rt.repo, Trash: rt.repo, Formats: formats}),
		Authorize: authorize.New(authorize.Ports{Grants: rt.repo}),
		Fetch:     fetch.New(fetch.Ports{Upstream: rt.upstream, Cache: rt.repo, Permits: rt.repo}),
		// about forty more at P9c
	}
}
```

At P41 the same function constructs about 110 interactors. Gates and
appenders stop being no-ops: `Gate: gates.Chain(rt.workers.DownloadGate,
rt.curation.Gate, rt.policy.Gate)` and `Events: rt.repo` (the outbox). The
file is long but flat; every port assignment is visible.

### Generated code and API specifications

| Spec | Fragments | Strict server output | Implemented by | Client output |
| --- | --- | --- | --- | --- |
| artifactory | `api/src/artifactory/*.yaml` | `internal/infrastructure/gen/artifactory` | `adapter/http/controller/artifactory.go` (aggregate over storage, search, builds, repositories, system, security, remote) | `pkg/client/artifactory` |
| access | `api/src/access/*.yaml` | `internal/infrastructure/gen/access` | `adapter/http/controller/access` | `pkg/client/access` |
| event | `api/src/event/*.yaml` | `internal/infrastructure/gen/event` | `adapter/http/controller/webhook` (P11) | `pkg/client/event` |
| distribution | `api/src/distribution/*.yaml` | `internal/infrastructure/gen/distribution` | `adapter/http/controller/distribution` (P15) | `pkg/client/distribution` |
| grid, pfed | `api/src/grid`, `api/src/pfed` | `internal/infrastructure/gen/grid`, `gen/pfed` | `adapter/http/controller/topology` (P14) | `pkg/client/grid`, `pkg/client/pfed` |
| worker | `api/src/worker/*.yaml` | `internal/infrastructure/gen/worker` | `adapter/http/controller/worker` (P17) | `pkg/client/worker` |
| peer | `api/src/peer/*.yaml` (P13) | `internal/infrastructure/gen/peer` | `adapter/peer` | `pkg/client/peer` |
| evidence, lifecycle, apptrust, xray | `api/src/<spec>` (P23 to P26) | `internal/infrastructure/gen/<spec>` | `adapter/http/controller/<spec>` | `pkg/client/<spec>` |

`api/oapi-server-*.yaml` change their `output` from
`internal/server/gen/<spec>/server.gen.go` to
`internal/infrastructure/gen/<spec>/server.gen.go`. The generated interfaces
sit in ring 4, and ring-3 controllers implement them, which inverts the usual
direction: ring 3 imports ring 4 types to satisfy them. The layout accepts
that by allowing `adapter/http/controller` to import `infrastructure/gen` as
the one exception to rule 3. `devtool/stubgen` writes
`adapter/http/stub/stubs.gen.go`. `devtool/specoracle` and
`devtool/openapibundle` back `tools/specoracle` and `tools/openapi-bundle`.

### Tests and enforcement

```text
Unit tests             beside code in every ring; use cases use tiny fakes of their own ports
Contract suites        usecase/format/formattest (formats), adapter/repository/repositorytest (SQL, inmem),
                       adapter/gateway/blob/blobtest, adapter/gateway/cdn/cdntest, entity/aql/aqltest
Server-assembly        test/server/<family>, support/, shared/, registryfixture/, agentcommon/ (unchanged)
Acceptance             test/acceptance (godog features, steps, clients/*.Dockerfile)
Platform, Terraform    test/platform, test/terraform (unchanged)
```

```go
// internal/layout/layout_test.go
var forbidden = map[string][]string{
	"internal/entity/": {
		module + "/internal/usecase/", module + "/internal/adapter/",
		module + "/internal/infrastructure/", module + "/internal/app",
		"net/http", "database/sql", "os", "net",
	},
	"internal/usecase/": {
		module + "/internal/adapter/", module + "/internal/infrastructure/", module + "/internal/app",
	},
	"internal/adapter/": {module + "/internal/infrastructure/", module + "/internal/app"},
	"internal/infrastructure/": {module + "/internal/app"},
	"pkg/":                     {module + "/internal/", module + "/cmd/"},
}

// allowed lists the exceptions: controllers implement generated interfaces,
// and use cases may share three contract packages.
var allowed = map[string][]string{
	"internal/adapter/http/controller/": {module + "/internal/infrastructure/gen/"},
}

// sharedUseCase holds the only usecase packages another use case may import.
var sharedUseCase = []string{"format", "unitofwork", "event"}
```

Coverage: about 230 packages at P9c and about 400 at P41. Thin use cases
(one interactor, one port file) are easy to cover. Packages likely to
struggle with a 90% floor are `adapter/http/controller` (the aggregate
implementing a very large generated interface), `infrastructure/router`
(mostly mounting), `adapter/eventloop` (concurrency paths) and the small
gateway packages for external integrations, whose failure paths need
controlled fakes.

## Walkthroughs

### An npm publish request

1. `infrastructure/router/router.go` mounts `protocol/npm` under `/artifactory/api/npm/{repo}`.
2. `adapter/http/middleware/authn.go` calls `usecase/authenticate` and stores the principal.
3. `adapter/http/middleware/authz.go` calls `usecase/authorize` and stores the Decider.
4. `adapter/http/protocol/npm/publish.go` decodes the body with `body.go` and checks Deploy with the Decider.
5. `publish.go` calls `usecase/deploy.Interactor.Deploy` once per attachment.
6. `usecase/deploy/deploy.go` writes content through its `BlobWriter` port, satisfied by `adapter/gateway/blob/s3`.
7. `usecase/format/npm/tarball.go` inspects package.json through the `ArchiveReader` port, satisfied by `adapter/gateway/archive`.
8. `usecase/deploy/deploy.go` opens `UnitOfWork.Do`, satisfied by `adapter/repository/sqlcommon/unitofwork.go`.
9. `usecase/format/npm/policy.go` admits the write.
10. `adapter/repository/sqlcommon/content_items.go` stores the item.
11. `usecase/format/npm/npm.go` `Deployed` links the version via `format.Env`, backed by `sqlcommon/content_index.go`, derives the packument and enqueues work.
12. `adapter/repository/sqlcommon/outbox.go` appends `ItemDeployed` in the same transaction (P11).
13. The transaction commits; `AfterCommit` wakes the work queue.
14. `adapter/http/protocol/npm/presenter.go` writes the npm response or maps errors with `adapter/http/apierror`.

### A BEFORE_DOWNLOAD worker blocks a download (P17)

1. `protocol/npm/packument.go` or a tarball route receives the GET.
2. The middleware supplies principal and Decider as above.
3. The handler calls `usecase/download.Interactor.Download`.
4. `usecase/download/download.go` resolves the item through its `ItemReader` port.
5. It calls `DownloadGate.BeforeDownload`, the port it declared.
6. `usecase/runworker/gate.go` satisfies the port and loads bindings through its `Catalogue` port (`sqlcommon/workers.go`).
7. `runworker/run.go` calls its `Sandbox` port, satisfied by `adapter/gateway/sandbox/pool.go`.
8. A helper process running `infrastructure/workerhost` executes the transpiled worker under the limits in `entity/worker/limits.go`.
9. The worker returns STOP; `runworker` records the execution through `ExecutionLog` and returns `worker.ErrBlocked`.
10. `download` returns the error unchanged; `protocol/npm/presenter.go` maps it to 403 with the worker's message.

### Adding a new package type

For a hypothetical `zigpkg` with local and remote classes:

1. `internal/entity/repo/packagetypes.go`: add the type, display name and class matrix entry.
2. `internal/usecase/format/zigpkg/zigpkg.go`: `Handler`, plus `Inspector` and `Admitter`.
3. `internal/usecase/format/zigpkg/index.go metadata.go port.go remote.go`: index rules, metadata, its own ports, the remote metadata rewriter.
4. `internal/usecase/format/zigpkg/*_test.go`, `contract_test.go`, `testdata/`.
5. `internal/usecase/format/formats/formats.go`: register the handler.
6. `internal/adapter/gateway/upstream/zigpkg.go`: upstream wire reading, if the protocol needs more than plain GET.
7. `internal/adapter/http/protocol/zigpkg/routes.go index.go publish.go download.go presenter.go *_test.go`.
8. `internal/infrastructure/router/protocols.go`: mount the routes.
9. `internal/app/controllers.go`: construct the protocol handler with its use cases.
10. `test/server/zigpkg/*_test.go`, `test/acceptance/features/zigpkg.feature`, `test/acceptance/zigpkg_steps_test.go`, `test/acceptance/clients/zigpkg.Dockerfile`.
11. `docs/implementation/package-format-inventory.json` and `tools/citestselection/test_ownership.json`.

Files touched in four rings plus tests: about 20 new and 5 edited.

### Adding a new business domain: Evidence (P23)

1. `internal/entity/evidence/evidence.go subject.go signer.go dsse.go`: pure model and DSSE verification.
2. `internal/entity/event/evidence.go`: `EvidenceAttached`, `EvidenceDeleted`.
3. `internal/usecase/attest/port.go attest.go *_test.go`.
4. `internal/usecase/verifyevidence/port.go verify.go *_test.go`.
5. `internal/usecase/evidencequery/port.go query.go *_test.go`.
6. `internal/usecase/signerkey/port.go publish.go rotate.go *_test.go`.
7. `internal/adapter/repository/sqlcommon/evidence.go`, `inmem/evidence.go`, `repositorytest/evidence.go`, and the `Repository` union in `repository.go`.
8. `internal/adapter/repository/sqlite/migrations/0018_evidence.sql`, the same in `postgres/migrations` and `mysql/migrations`.
9. `api/src/evidence/*.yaml`, `api/oapi-server-evidence.yaml`, `api/oapi-client-evidence.yaml`; generate `internal/infrastructure/gen/evidence` and `pkg/client/evidence`.
10. `internal/adapter/http/controller/evidence/*.go`, implementing `gen/evidence`.
11. `internal/infrastructure/router/router.go`: mount the evidence spec.
12. `internal/usecase/replicate/port.go`, `transfer/port.go`, `remove/port.go`: add evidence handling to copy, promotion, replication and subject deletion.
13. `internal/app/usecases.go controllers.go events.go`: wire the four interactors and their consumers.
14. `test/acceptance/features/evidence.feature` and steps.

## Package mapping

| Today's package | New location | Notes |
| --- | --- | --- |
| `api` | `api` | unchanged; parity test stays |
| `cmd/artifactweave` | `cmd/artifactweave` | gains worker-host subcommand (P17) |
| `internal/app` | `internal/app` | gains usecases.go, controllers.go, events.go |
| `internal/archive` | `internal/adapter/gateway/archive` | behind formats' ArchiveReader port |
| `internal/auth` | `internal/usecase/authenticate`, `internal/usecase/seed` | |
| `internal/auth/idtoken` | `internal/usecase/authenticate/idtoken.go` | JWKS fetch moves to `adapter/gateway/idp/oidc` |
| `internal/auth/idtoken/idtokentest` | `internal/adapter/gateway/idp/oidc/oidctest` | |
| `internal/auth/token` | `internal/usecase/token` | |
| `internal/authz` | `internal/entity/authz`, `internal/usecase/authorize` | decision pure, loading in use case |
| `internal/buildinfo` | `internal/infrastructure/buildinfo` | |
| `internal/config` | `internal/infrastructure/config` | |
| `internal/crypt` | `internal/adapter/gateway/keyring`, `internal/usecase/keyrotate` | |
| `internal/descriptor` | `internal/usecase/descriptor` | parsing stays inside the use case |
| `internal/fakeupstream` | `internal/adapter/gateway/upstream/fakeupstream` | |
| `internal/format` (root) | `internal/usecase/format` | shared contract package |
| `internal/format/formats` | `internal/usecase/format/formats` | |
| `internal/format/formattest` | `internal/usecase/format/formattest` | imports a repository harness through a port |
| `internal/format/generic` | `internal/usecase/format/generic` | |
| `internal/format/docker` | `internal/usecase/format/docker` | also oci, helmoci |
| `internal/format/terraform` | `internal/usecase/format/terraform` | |
| `internal/format/terraform/terraformtest` | `internal/usecase/format/terraform/terraformtest` | |
| `internal/format/terraformbackend` | `internal/usecase/format/terraformbackend` | |
| `internal/format/gitlfs` | `internal/usecase/format/gitlfs` | |
| `internal/format/<ecosystem>` (other 28) | `internal/usecase/format/<ecosystem>` | per-format remote rules join as remote.go |
| `internal/keymaterial` | `internal/usecase/keypair` | |
| `internal/layout` | `internal/layout` | ring rules |
| `internal/openapibundle` | `internal/devtool/openapibundle` | |
| `internal/reindexauth` | `internal/usecase/reindex/authorize.go` | |
| `internal/remote` | `internal/usecase/fetch`, `smartsync`, `transitive`, `remotebrowse`, `internal/adapter/gateway/upstream` | split by policy and transport |
| `internal/repos` | `internal/usecase/repository` | |
| `internal/server` (root, 65 files) | `infrastructure/router`, `adapter/http/middleware`, `adapter/http/controller/*`, `adapter/http/protocol/*` | see the root-file table |
| `internal/server/apierror` | `internal/adapter/http/apierror` | |
| `internal/server/baseurl` | `internal/adapter/http/baseurl` | |
| `internal/server/gen/<spec>` (7) | `internal/infrastructure/gen/<spec>` | |
| `internal/server/pkgroute` | `internal/adapter/http/protocol/pkgroute` | |
| `internal/server/docker` | `internal/adapter/http/protocol/docker` | with docker_rest.go |
| `internal/server/golang` | `internal/adapter/http/protocol/golang` | |
| `internal/server/vcs` | `internal/adapter/http/protocol/vcs` | with vcs_*.go root files |
| `internal/server/<protocol>` (other 24) | `internal/adapter/http/protocol/<protocol>` | |
| `internal/service/build` | `internal/usecase/buildpublish`, `buildpromote`, `buildretention` | |
| `internal/service/gc` | `internal/usecase/gc` | |
| `internal/service/identity` | `internal/usecase/user`, `internal/usecase/group` | |
| `internal/service/item` | `internal/usecase/deploy`, `mkfolder`, `download`, `remove`, `restore`, `transfer`, `explode`, `annotate`, `reindex`, `cache`, `zap` | 13 files into 11 packages |
| `internal/service/multipart` | `internal/usecase/multipart` | |
| `internal/service/oidc` | `internal/usecase/oidcexchange` | |
| `internal/service/permission` | `internal/usecase/permission` | |
| `internal/service/pkgindex` | `internal/usecase/index` | |
| `internal/service/registry` | `internal/usecase/registry` | |
| `internal/service/roles` | `internal/usecase/role` | |
| `internal/service/terraformstate` | `internal/usecase/terraformstate` | |
| `internal/service/trash` | `internal/usecase/trash` | |
| `internal/service/usage` | `internal/usecase/usage` | platform-specific files stay build-tagged |
| `internal/service/work` | `internal/adapter/scheduler/work` | queue runner is a driving adapter; Enqueue is a port |
| `internal/source` | `internal/usecase/fetch` | |
| `internal/specoracle` | `internal/devtool/specoracle` | |
| `internal/sqlstore/postgres` | `internal/adapter/repository/postgres` | |
| `internal/sqlstore/sqlcommon` | `internal/adapter/repository/sqlcommon` | |
| `internal/sqlstore/sqlite` | `internal/adapter/repository/sqlite` | |
| `internal/sqlstore/sqltest` | `internal/adapter/repository/repositorytest` | merged with storetest |
| `internal/stubgen` | `internal/devtool/stubgen` | |
| `internal/testfault` | `internal/adapter/testfault` | |
| `pkg/aql` | `internal/entity/aql`, `internal/adapter/repository/aqlsql` | language and compiler split |
| `pkg/aql/aqltest` | `internal/entity/aql/aqltest` | |
| `pkg/blob` | `internal/adapter/gateway/blob` | no longer public |
| `pkg/blob/azure`, `fs`, `gcs`, `s3`, `mirror`, `session` | `internal/adapter/gateway/blob/<backend>` | |
| `pkg/blob/blobtest` | `internal/adapter/gateway/blob/blobtest` | |
| `pkg/cdn` | `internal/adapter/gateway/cdn` | |
| `pkg/cdn/cdntest` | `internal/adapter/gateway/cdn/cdntest` | |
| `pkg/client/<spec>` (7) | `pkg/client/<spec>` | unchanged, stays public |
| `pkg/containertest` | `pkg/containertest` | unchanged |
| `pkg/layout` | `internal/entity/layout` | |
| `pkg/repo` | `internal/entity/repo` | |
| `pkg/store` | dissolved into use-case ports; `internal/adapter/repository/repository.go` | |
| `pkg/store/inmem` | `internal/adapter/repository/inmem` | |
| `pkg/store/storetest` | `internal/adapter/repository/repositorytest` | |
| `pkg/types` | `internal/entity/<subject>` | split by subject |
| `pkg/types/wire` | `internal/entity/wire` | |
| `test/acceptance`, `test/platform` | unchanged | |
| `test/server/<family>` (17) | unchanged | import paths only |
| `tools/*` (9) | unchanged | import `internal/devtool` |

## Numbers

Estimates, rounded.

| Measure | P9c | P41 |
| --- | --- | --- |
| Packages under internal/ and pkg/ | about 230 | about 400 |
| Use-case packages (excluding formats) | about 45 | about 110 |
| Import rules in the layout test | 6 plus 2 exception lists | same |
| Interfaces a new ecosystem implements | `format.Handler` plus optional Inspector, Admitter, Resolver, Merger (P10), MetadataRewriter | same |
| Files touched to add an ecosystem | about 25 across four rings and tests | same |
| Files touched to add a domain | about 30 across four rings, migrations, spec and tests | same |
| Largest package | `adapter/repository/sqlcommon`, about 10k lines today | about 25k lines |

## Placement choices and friction

Placement choices settled in this file:

- **Entities are private.** `pkg/types`, `pkg/repo`, `pkg/layout` and the AQL language move into `internal/entity`; `pkg/` keeps only generated clients and `containertest`.
- **Three shared contract packages in ring 2.** `usecase/format`, `usecase/unitofwork` and `usecase/event` are the only use-case packages others may import, because seven use cases call format hooks inside one transaction and every writing use case appends events.
- **Formats sit in ring 2.** Their pure parsing is not split into entities; it stays with the hooks so each format is one package.
- **Remote is split by policy and transport.** Decisions in `usecase/fetch` and friends; HTTP and wire reading in `adapter/gateway/upstream`.
- **The work queue runner is a driving adapter.** Enqueue is a port; `adapter/scheduler/work` polls and calls handlers.
- **One SQL repository.** A single concrete store satisfies every port; tables are not owned per use case.

Friction:

- **The item service fragments.** `internal/service/item` becomes eleven packages that all share `usecase/format` and `usecase/unitofwork`, so the per-consumer rule holds in name more than in practice for the write path.
- **The storage ports overlap.** `adapter/repository/sqlcommon` satisfies dozens of small interfaces with overlapping methods, and `repository.go` reintroduces a union interface for the contract suite.
- **Generated interfaces point outward.** `adapter/http/controller/artifactory.go` must implement a strict server interface from ring 4, which needs an explicit exception to rule 3.
- **Event flow is invisible in the tree.** Which use cases consume which events lives only in `internal/app/events.go`.
- **Domains are not separated.** Nothing stops `usecase/evaluatepolicy` (P25) from declaring a port over item tables and reaching into storage; the rings permit it.
- **Rule 2 forces ports for composition.** `transfer` needs deploy and remove behaviour but may not import them; it declares ports that `app` fills with those interactors, adding wiring for every composed operation.
- **The composition root grows long.** `internal/app/usecases.go` constructs about 110 interactors by P41.
