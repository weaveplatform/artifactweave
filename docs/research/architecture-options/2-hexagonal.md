# Option 2: Hexagonal (ports and adapters), detailed layout

This file is one of five detailed layouts that accompany
[ARCHITECTURE-OPTIONS.md](../ARCHITECTURE-OPTIONS.md). Every file uses the same
sections in the same order, so two options can be read side by side. The stance is
the same as the summary document: no existing decision, gate or import rule is a
constraint, and the roadmap in the [implementation plan](../docs/implementation/plan.md)
(P10 to P41) is scope.

## Summary

One application core for the whole system, surrounded by adapters. The core
declares two kinds of port. Driving ports, in `internal/core/port/driving`, are the
interfaces through which the outside world asks the core to do something: deploy a
file, resolve a package, issue a token, run a search. Driven ports, in
`internal/core/port/driven`, are the interfaces through which the core reaches the
outside world: the metadata store, the blob store, CDN signers, upstream HTTP, the
keyring, and on the roadmap the outbox, identity providers, Vault, KMS, the Workers
sandbox, scanners and feeds. Adapters implement ports and live in
`internal/adapter/driving` (HTTP, CLI, peer protocol, scheduler, worker host, SCIM,
GraphQL, console) and `internal/adapter/driven` (SQL dialects, in-memory store, blob
backends, CDNs, upstream transport). Contract suites for every driven port live in
`internal/contract`. The one idea that organises the tree is the direction of
dependency: everything points at `internal/core`, and `internal/core` points at
nothing outside itself.

Dependency rules:

1. `internal/core/...` imports nothing under `internal/adapter/`, `internal/app/`,
   `internal/contract/` or `cmd/`.
2. `internal/core/port/...` imports only `internal/core/model/...`,
   `internal/core/repo` and the standard library.
3. `internal/adapter/driven/...` imports nothing under `internal/adapter/driving/`.
4. `internal/adapter/driving/...` imports nothing under `internal/adapter/driven/`,
   except `internal/adapter/driving/cli`, which may call `internal/app` to open the
   runtime for `migrate` and `keys`.
5. No package under `internal/adapter/` or `internal/core/` imports `internal/app/`.
6. `internal/adapter/driving/...` imports `internal/core/service/...` only through
   `internal/core/port/driving`; a handler holds an interface, never a service struct.
7. `internal/core/format/<ecosystem>` imports nothing under `internal/core/service/`;
   it reaches the package index and the work queue through `format.Env` and
   `internal/core/port/driven`.
8. `pkg/...` imports nothing under `internal/` or `cmd/`.

## Repository root

```text
artifactweave/
  cmd/
    artifactweave/              main.go only: builds the cobra root from adapter/driving/cli
                                and exits. Shrinks from 237 lines to about 20; the command
                                tree moves to internal/adapter/driving/cli
  internal/
    app/                        composition root (unchanged role, grows with every adapter)
    config/                     strict YAML, flag > env > file (unchanged)
    buildinfo/                  unchanged
    core/                       NEW: the hexagon (model, ports, services, formats, pure logic)
    adapter/                    NEW: driving/ and driven/ adapters
    contract/                   NEW: contract suites for driven ports, moved out of pkg/
    tooling/                    NEW: openapibundle, stubgen, specoracle (spec tooling, not runtime)
    layout/                     import-rule test, rewritten for the eight rules above
  pkg/                          SHRINKS: generated clients and containertest only
    client/<spec>/
    containertest/
  api/                          unchanged: fragments in src/, bundled specs, oapi configs.
                                The oapi-server-*.yaml output paths change to
                                internal/adapter/driving/http/gen/<spec>
  test/                         unchanged shape: acceptance, server, oci, platform, terraform.
                                test/server/support imports adapter/driving/http instead of
                                internal/server
  tools/                        unchanged; the three spec tools import internal/tooling/*
  docs/  deploy/                unchanged
  Makefile                      generate target writes to the new gen path
  .golangci.yml                 depguard rules mirror the eight dependency rules
  .testcoverage.yml             exclusion patterns move from internal/server/gen to
                                internal/adapter/driving/http/gen
  go.mod                        unchanged module path
```

The visible change at the root is that `pkg/` stops being the home of contracts and
backends. In a single hexagon the store, blob and CDN interfaces are driven ports,
so they belong to the core; their implementations are driven adapters. Keeping them
public is listed as a rejected alternative under
[Placement choices and friction](#placement-choices-and-friction).

## Full package tree

Annotations: `← today/path` is the origin, `(Pnn)` is a roadmap addition, a line
with neither is new structure for today's code.

```text
internal/
  app/                                    ← internal/app
  config/                                 ← internal/config
  buildinfo/                              ← internal/buildinfo
  layout/                                 ← internal/layout (rules rewritten)

  core/
    model/                                ← pkg/types (item, build, principal, token, permission,
                                            roles, project, peer, federation, distribution,
                                            archive, platform, webhook, replica, remotecache,
                                            source, scope, version, matrix, keys, operations)
      wire/                               ← pkg/types/wire
    repo/                                 ← pkg/repo (repository configuration model, validation)
    layout/                               ← pkg/layout (repository layout patterns)
    aql/                                  ← pkg/aql (parser, AST, evaluator, SQL builder)
    authz/                                ← internal/authz (permission Decider, pure)
    keymaterial/                          ← internal/keymaterial (key pair checks, pure)
    format/                               ← internal/format (Handler, Env, Inspector, Admitter, ...)
      formats/                            ← internal/format/formats (picks a Handler per type)
      generic/                            ← internal/format/generic
      agentpackages/ agentplugins/ aieditorextensions/ alpine/ bazelmodules/ bower/
      cargo/ cocoapods/ composer/ conan/ conda/ cran/ debian/ docker/ gems/ gitlfs/
      golang/ helm/ hex/ huggingface/ luarocks/ maven/ npm/ nuget/ opkg/ p2/ pub/ pypi/
      rpm/ skills/ swift/ terraform/ terraformbackend/
                                          ← internal/format/<same name>
      ansible/ chef/ puppet/ vagrant/ nix/ vcs/ jetbrainsplugins/ machinelearning/ nimmodel/
                                          (P9c) dedicated format packages for the types that
                                            today use generic item routes or protocol-only code
    port/
      driving/                            NEW: what adapters may ask the core to do
        content.go                        Content: Deploy, DeployByChecksum, Upload, MakeFolder,
                                            Delete, Purge, Restore, Tree, Annotate, Zap, Explode
        download.go                       Download: Open, Resolve, Redirect decision
        packages.go                       Packages: Reindex, ReindexPath, Index queries
        registry.go                       Registry: upload sessions, manifests, tags, promote
        builds.go                         Builds: publish, append, rename, promote, delete
        search.go                         Search: AQL, GET searches, upstream searches
        repos.go                          Repos: create, update, delete, descriptor apply
        remote.go                         Remote: fetch, cache maintenance, Smart Remote status
        identity.go                       Identity: users, groups, tokens, keys, permissions, roles
        auth.go                           Authenticator: credentials to auth.Principal
        access.go                         Access: Decider for a principal (wraps core/authz)
        oidc.go                           OIDC: token exchange
        terraformstate.go                 TerraformState: workspaces, states, locks
        storage.go                        Storage: gc, trash, usage, mirrors administration
        work.go                           Work: enqueue, run due (for scheduler and admin)
        virtual.go                        (P10) Virtual: ordered resolution, merged metadata
        projects.go                       (P11) Projects
        webhooks.go                       (P11) Webhooks
        cleanup.go                        (P11) Cleanup policies, runs, dry runs
        replication.go                    (P11, P37) Replication
        sysconfig.go                      (P12) System configuration, XML descriptor
        directory.go                      (P12) LDAP settings and group sync
        federation.go                     (P13) Peers, pairing, federated repositories
        topology.go                       (P14) pfed and grid
        releases.go                       (P15, P24) Release bundles, lifecycle, distribution
        archive.go                        (P16) Archive policies, restore jobs
        workers.go                        (P17) Worker CRUD, test, execute, history
        sessions.go                       (P21) Browser sessions, approvals, MFA
        provisioning.go                   (P22) SCIM
        evidence.go                       (P23) Evidence
        governance.go                     (P25) Applications, policies, approvals
        security.go                       (P26) Scans, findings, SBOMs, watches, Catalog
        curation.go                       (P27) Curation decisions
        operations.go                     (P38) Support bundles, integrations, queue admin
        estate.go                         (P40) Estate and account functions
        types.go                          request and result DTOs shared by the files above
                                            (Target, Deploy, CacheUpload, Explode, ...)
      driven/                             NEW: what the core needs from outside
        store.go                          ← pkg/store/store.go (Store, Tx, AfterCommit, Page)
        content.go                        ← pkg/store/content.go (RepoStore, ItemStore, BlobIndex,
                                            ReplicaIndex, UploadStore, TrashStore, PackageStore)
        identity.go                       ← pkg/store/identity.go
        build.go                          ← pkg/store/build.go
        operations.go                     ← pkg/store/operations.go
        peers.go                          ← pkg/store/peers.go (InstanceStore, PeerStore,
                                            OutboxStore, FederationStore, PlatformStore)
        enterprise.go                     ← pkg/store/enterprise.go
        remotecache.go                    ← pkg/store/remotecache.go
        gitlfs_locks.go                   ← pkg/store/gitlfs_locks.go
        properties.go work.go aqlsource.go layout_sources.go normalise.go
                                          ← pkg/store/<same name>
        errors.go                         ← sentinel errors from pkg/store
        blob.go                           ← pkg/blob/blob.go (blob.Store, Info, upload sessions)
        cdn.go                            ← pkg/cdn/cdn.go (Signer interface only)
        upstream.go                       Upstream: Do, credentials per revision, pool
        keyring.go                        Keyring: seal, open, derive (from internal/crypt API)
        archive.go                        ArchiveReader: bounded walk (from internal/archive API)
        clock.go                          Clock
        jwks.go                           JWKS: fetch discovery and keys for OIDC providers
        outbox.go                         (P11) Outbox: Append(ctx, Event) inside Tx
        hooks.go                          (P17) ActionHooks: BeforeDownload, BeforeUpload,
                                            AfterCreate, ... ; implemented by core services
        sandbox.go                        (P17) Sandbox: Execute(script, event, limits)
        delivery.go                       (P11) Delivery: HTTP POST for webhooks
        mail.go                           (P12) Mail
        ldap.go                           (P12) Directory
        peer.go                           (P13) PeerClient: signed requests to another instance
        idp.go                            (P21) SAML, human OIDC, Crowd identity providers
        lease.go                          (P35) Lease and fencing tokens
        vault.go kms.go iam.go logship.go (P38)
        scanner.go feed.go                (P26) analysis engine, intelligence feed
    service/
      item/                               ← internal/service/item
      trash/                              ← internal/service/trash
      gc/                                 ← internal/service/gc
      multipart/                          ← internal/service/multipart
      pkgindex/                           ← internal/service/pkgindex
      registry/                           ← internal/service/registry
      build/                              ← internal/service/build
      terraformstate/                     ← internal/service/terraformstate
      usage/                              ← internal/service/usage
      work/                               ← internal/service/work
      identity/                           ← internal/service/identity
      permission/                         ← internal/service/permission
      roles/                              ← internal/service/roles
      oidc/                               ← internal/service/oidc
      auth/                               ← internal/auth (Authenticator, policy, suspension)
        token/                            ← internal/auth/token
        idtoken/                          ← internal/auth/idtoken (exchange logic; HTTP via driven.JWKS)
      reindexauth/                        ← internal/reindexauth
      repos/                              ← internal/repos
      descriptor/                         ← internal/descriptor
      source/                             ← internal/source
      remote/                             ← internal/remote (retrieval policy, protocols, Smart Remote)
      virtual/                            (P10)
      project/                            (P11)
      webhook/                            (P11)
      cleanup/                            (P11)
      replication/                        (P11, P37)
      events/                             (P11) event catalogue and subscriber registry
      sysconfig/ backup/ mail/ directory/ (P12)
      peer/ federation/                   (P13)
      pfed/ grid/                         (P14)
      release/ distribution/              (P15)
      archive/                            (P16)
      workers/                            (P17)
      health/ metrics/                    (P18)
      session/ saml/ humanoidc/ crowd/ httpsso/ mfa/   (P21)
      scim/                               (P22)
      evidence/ signer/                   (P23)
      lifecycle/ releasev2/               (P24)
      apptrust/ policy/ approval/         (P25)
      xray/ sbom/ catalog/ onemodel/      (P26)
      curation/ contextual/ runtime/      (P27)
      importexport/                       (P36)
      multipush/                          (P37)
      integrations/ supportbundle/ queueadmin/   (P38)
      estate/                             (P40)

  adapter/
    driving/
      http/                               ← internal/server (router, middleware, root files)
        apierror/                         ← internal/server/apierror
        baseurl/                          ← internal/server/baseurl
        pkgroute/                         ← internal/server/pkgroute
        gen/
          artifactory/ access/ distribution/ event/ grid/ pfed/ worker/
                                          ← internal/server/gen/<same name>
          peer/                           (P13) artifactweave's own peer API
          evidence/ lifecycle/ apptrust/ xray/ curation/ scim/   (P22 to P27)
        management/                       ← the hand-written families of the internal/server root
          access/ storage/ search/ builds/ repositories/ remote/ system/
          projects/ webhooks/ cleanup/ replication/        (P11)
          sysconfig/                      (P12)
          federation/                     (P13)
          releases/ distribution/         (P15, P24)
          archive/                        (P16)
          workers/                        (P17)
          evidence/ governance/ security/ curation/       (P23 to P27)
          operations/ estate/             (P38, P40)
        protocol/
          agentpackages/ aieditorextensions/ bazelmodules/ bower/ cargo/ cocoapods/
          composer/ conan/ conda/ docker/ gems/ gitlfs/ golang/ helm/ hex/ huggingface/
          jetbrainsplugins/ luarocks/ nimmodel/ npm/ nuget/ pub/ pypi/ skills/ swift/
          terraform/ terraformbackend/ vcs/
                                          ← internal/server/<same name>
          agentplugins/ alpine/ cran/ debian/ maven/ opkg/ p2/ rpm/
                                          ← files in the internal/server root (see below)
          ansible/ chef/ puppet/ vagrant/ nix/ machinelearning/
                                          (P9c) where the type has a native route beyond
                                            the generic item path
      cli/                                ← cmd/artifactweave/main.go command tree
                                            (serve, migrate, config, keys, version)
      events/                             (P11) outbox dispatcher: polls the outbox and calls
                                            subscriber ports in the core
      scheduler/                          (P11) cron triggers for cleanup, archive, replication
      peer/                               (P13) peer protocol server side
      workerhost/                         (P17) hidden worker-host subcommand: the helper
                                            process that runs goja
      scim/                               (P22) if SCIM is not served from http/gen/scim
      graphql/                            (P26) OneModel gateway
      web/                                (P21 login and approval, P39 console)
    driven/
      sqlstore/
        sqlcommon/                        ← internal/sqlstore/sqlcommon
        sqlite/                           ← internal/sqlstore/sqlite (+ migrations/)
        postgres/                         ← internal/sqlstore/postgres (+ migrations/)
        mysql/                            (P34)
      inmemstore/                         ← pkg/store/inmem
      blob/
        fs/                               ← pkg/blob/fs
        s3/                               ← pkg/blob/s3
        gcs/                              ← pkg/blob/gcs
        azure/                            ← pkg/blob/azure
        mirror/                           ← pkg/blob/mirror
        session/                          ← pkg/blob/session
        cold/                             (P16) cold-tier policy wrapper over any backend
      cdn/
        cloudfront/ cloudcdn/ frontdoor/  ← pkg/cdn (cloudfront.go, cloudcdn.go, frontdoor.go)
      httpupstream/                       ← transport files of internal/remote
      jwks/                               ← discovery and key fetch from internal/auth/idtoken
      keyring/                            ← internal/crypt
      archive/                            ← internal/archive
      fakeupstream/                       ← internal/fakeupstream (test adapter)
      testfault/                          ← internal/testfault (failing fakes)
      webhookhttp/                        (P11) Delivery over HTTP with signing
      smtp/                               (P12)
      ldap/                               (P12)
      peerclient/                         (P13)
      sandbox/                            (P17) helper-process pool, IPC with workerhost
      saml/ oidcidp/ crowd/               (P21)
      scanengine/ feeds/                  (P26)
      lease/                              (P35) if leases leave the SQL store
      vault/ kms/ iam/ logship/           (P38)

  contract/
    storetest/                            ← pkg/store/storetest
    sqltest/                              ← internal/sqlstore/sqltest
    blobtest/                             ← pkg/blob/blobtest
    cdntest/                              ← pkg/cdn/cdntest
    aqltest/                              ← pkg/aql/aqltest
    formattest/                           ← internal/format/formattest
    terraformtest/                        ← internal/format/terraform/terraformtest
    idtokentest/                          ← internal/auth/idtoken/idtokentest
    outboxtest/                           (P11)
    sandboxtest/                          (P17)
    idptest/                              (P21)

  tooling/
    openapibundle/                        ← internal/openapibundle
    stubgen/                              ← internal/stubgen
    specoracle/                           ← internal/specoracle

pkg/
  client/
    artifactory/ access/ distribution/ event/ grid/ pfed/ worker/
                                          ← pkg/client/<same name>
    peer/ evidence/ lifecycle/ apptrust/ xray/   (P13 to P26)
  containertest/                          ← pkg/containertest
```

### All 49 package types

| Package type | Format package (core) | Protocol package (driving adapter) |
| --- | --- | --- |
| agentpackages | core/format/agentpackages | protocol/agentpackages |
| agentplugins | core/format/agentplugins | protocol/agentplugins ← server/agentplugins.go |
| aieditorextensions | core/format/aieditorextensions | protocol/aieditorextensions |
| alpine | core/format/alpine | protocol/alpine ← server/alpine.go |
| ansible | core/format/ansible (P9c) | generic item routes |
| bazelmodules | core/format/bazelmodules | protocol/bazelmodules |
| bower | core/format/bower | protocol/bower |
| cargo | core/format/cargo | protocol/cargo |
| chef | core/format/chef (P9c) | generic item routes |
| cocoapods | core/format/cocoapods | protocol/cocoapods |
| composer | core/format/composer | protocol/composer |
| conan | core/format/conan | protocol/conan |
| conda | core/format/conda | protocol/conda |
| cran | core/format/cran | protocol/cran ← server/cran.go, cran_upload.go |
| debian | core/format/debian | protocol/debian ← server/debian*.go |
| docker | core/format/docker | protocol/docker (+ docker_rest.go) |
| oci | shares core/format/docker | shares protocol/docker |
| helmoci | shares core/format/docker and core/format/helm | shares protocol/docker |
| gems | core/format/gems | protocol/gems |
| generic | core/format/generic | management/storage (item routes) |
| gitlfs | core/format/gitlfs | protocol/gitlfs |
| go | core/format/golang | protocol/golang |
| gradle, ivy, sbt | share core/format/maven | share protocol/maven ← server/maven.go |
| helm | core/format/helm | protocol/helm |
| hex | core/format/hex | protocol/hex |
| huggingfaceml | core/format/huggingface | protocol/huggingface |
| jetbrainsplugins | core/format/jetbrainsplugins (P9c) | protocol/jetbrainsplugins |
| luarocks | core/format/luarocks | protocol/luarocks |
| machinelearning | core/format/machinelearning (P9c) | generic item routes, then protocol/machinelearning |
| maven | core/format/maven | protocol/maven |
| nimmodel | core/format/nimmodel (P9c) | protocol/nimmodel |
| nix | core/format/nix (P9c) | generic item routes |
| npm | core/format/npm | protocol/npm (+ npm_auth.go, npm_operations.go) |
| nuget | core/format/nuget | protocol/nuget |
| opkg | core/format/opkg | protocol/opkg ← server/opkg.go |
| p2 | core/format/p2 | protocol/p2 ← server/p2.go, p2_external.go |
| pub | core/format/pub | protocol/pub |
| puppet | core/format/puppet (P9c) | generic item routes |
| pypi | core/format/pypi | protocol/pypi (+ pypi_routes.go) |
| releasebundles | core/format/generic until P15 | management/releases (P15) |
| rpm | core/format/rpm | protocol/rpm ← server/rpm.go |
| skills | core/format/skills | protocol/skills |
| swift | core/format/swift | protocol/swift |
| terraform | core/format/terraform | protocol/terraform |
| terraformbackend | core/format/terraformbackend | protocol/terraformbackend |
| vagrant | core/format/vagrant (P9c) | generic item routes |
| vcs | core/format/vcs (P9c) | protocol/vcs (+ vcs_download.go, vcs_nested.go, vcs_routes.go) |

### Where the 65 files of today's internal/server root go

| Destination | Files |
| --- | --- |
| `adapter/driving/http` (router and cross-cutting) | server.go, middleware.go, context.go, auth.go, trace.go, handwritten.go, protocols.go, stubs.go |
| `http/management/access` | access.go, access_identity.go, permissions.go, roles.go, tokens.go, security.go, keypairs.go, oidc.go |
| `http/management/storage` | items.go, storage.go, storageinfo.go, metadata_stats.go, multipart.go, redirect.go, trash.go, transfer.go |
| `http/management/remote` | source.go, source_listing.go, smart_status.go |
| `http/management/search` | aql.go, aql_actions.go, aql_builds.go, aql_transitive.go, aql_transitive_peer.go, searches.go, searches_builds.go, searches_layout.go, searches_upstream.go, searches_upstream_maven.go, searches_upstream_routes.go, searches_versions.go |
| `http/management/builds` | builds.go |
| `http/management/repositories` | repositories.go, descriptor.go, reindex.go |
| `http/management/system` | system.go, reverseproxy.go |
| `http/protocol/<ecosystem>` | agentplugins.go, alpine.go, cran.go, cran_upload.go, debian.go, debian_operations.go, debian_snapshot.go, debian_virtual.go, docker_rest.go, maven.go, npm_auth.go, npm_operations.go, opkg.go, p2.go, p2_external.go, pypi_routes.go, rpm.go, vcs_download.go, vcs_nested.go, vcs_routes.go |

`debian_virtual.go` moves to `protocol/debian` now and its resolution logic to
`core/service/virtual` in P10.

## Deep dives

### npm ecosystem

```text
internal/core/format/npm/                         ← internal/format/npm
  npm.go                                          Handler: Deployed, Deleting, Deleted; Inspector
  tarball.go                                      reads package.json from the tarball (driven.ArchiveReader)
  metadata.go                                     packument and version document model
  index.go                                        pkgindex rows for name, version, dist-tags
  latest.go                                       latest and dist-tag rules
  policy.go                                       overwrite, deprecate, unpublish policy
  search.go                                       search ranking over the index
  work.go                                         strict-layout conversion, reindex (format.Worker)
  remote_validate.go                              ← internal/server/npm/remote_validate.go
                                                  (admission of upstream packuments is a rule)
  remote_integrity.go                             ← internal/server/npm/remote_integrity.go
  *_test.go                                       unit tests, fake driven ports
  formattest_test.go                              runs contract/formattest with the npm fixture
  testdata/                                       tarballs, packuments

internal/core/service/remote/
  npm.go                                          NEW: npm upstream resolution (packument fetch,
                                                  tarball fetch) over driven.Upstream; today this
                                                  logic sits in internal/server/npm/remote.go

internal/adapter/driving/http/protocol/npm/       ← internal/server/npm
  npm.go                                          mounts routes through pkgroute; holds
                                                  driving.Content, driving.Packages, driving.Access
  packument.go                                    GET packument, abbreviated form
  publish.go                                      PUT publish: decodes body, calls driving.Content.Deploy
  body.go                                         publish body decoding and limits
  disttags.go                                     dist-tag routes
  update.go                                       deprecate, unpublish
  search.go                                       /-/v1/search
  user.go                                         legacy login and logout
  npmrc.go                                        api/npm/auth
  auth.go                                         ← internal/server/npm_auth.go
  operations.go                                   ← internal/server/npm_operations.go (reindex, convert)
  remote.go remote_dist.go                        upstream-backed reads: response shaping and
                                                  URL rewriting; fetching is driving.Remote
  *_test.go                                       handler tests against fake driving ports

test/server/npm/                                  unchanged location
  fixture_test.go matrix_*.go status_*.go npm_auth_test.go npm_operations_test.go
test/server/support/npm.go npm_routes.go          import adapter/driving/http
test/acceptance/
  features/npm.feature npm_remote.feature npm_search_auth.feature
           npm_overwrites_and_settings.feature
  npm_steps_test.go npm_remote_steps_test.go
  clients/node.Dockerfile node-npm12.Dockerfile
```

The npm ecosystem is split across the hexagon's edge: rules in
`core/format/npm`, upstream resolution in `core/service/remote/npm.go`, the wire
protocol in `adapter/driving/http/protocol/npm`. Three directories, each with a
clear job, and none of them adjacent in the tree.

### Deploy write path

```text
internal/core/port/driving/content.go             Content interface, implemented by item.Service
internal/core/port/driving/types.go               Target, Deploy, CacheUpload, Explode, SidecarMove
internal/core/service/item/                       ← internal/service/item
  item.go                                         Service, Deploy, DeployByChecksum, Upload,
                                                  DeployCommitted, MakeFolder, Delete, Purge, Tree
  format.go                                       Env construction, Annotate, Resolve, Reindex
  sidecar.go                                      checksum sidecars, RecordChecksum, RecordSidecar
  remove.go restore.go                            delete into trash, restore from trash
  archive.go                                      Explode, ArchiveEntry, ArchiveFiles
  cache.go cache_cleanup.go zap.go smart.go       remote cache writes and maintenance
  transfer.go transfer_work.go                    copy and move with hooks
  work.go                                         RegisterWork with core/service/work
  download.go                                     (P17) download path with ActionHooks
internal/core/service/pkgindex/                   ← internal/service/pkgindex
  pkgindex.go reconcile.go                        Link, VersionOf, Prune, Sync, Reconcile
internal/core/service/build/                      ← internal/service/build (document item + record in one Tx)
internal/core/service/registry/                   ← internal/service/registry (uploads end as items)
internal/core/service/trash/ gc/ multipart/       ← internal/service/<same name>
internal/core/format/format.go                    Handler, Env (Store, Blobs, Derive, Enqueue, ...)
internal/core/port/driven/store.go                Store: Tx(ctx, fn), AfterCommit(ctx, fn)
internal/core/port/driven/outbox.go               (P11) Outbox.Append(ctx, Event)
internal/adapter/driven/sqlstore/sqlcommon/
  unitofwork.go                                   runTx, the context-carried unit
  content_items.go content_index.go blobs.go      item, package index, blob reference tables
```

```go
// internal/core/service/item/item.go (shape, not the full implementation)
package item

// Service implements driving.Content.
var _ driving.Content = (*Service)(nil)

type Service struct {
	Store   driven.Store      // metadata, unit of work
	Blobs   driven.BlobStore  // content
	Formats func(repo.PackageType) format.Handler
	Work    Enqueuer
	Outbox  driven.Outbox     // (P11); nil before webhooks exist
	Hooks   driven.ActionHooks // (P17); nil means no workers or curation
	Now     func() time.Time
}

func (s *Service) Deploy(ctx context.Context, d driving.Deploy) (model.Item, error) {
	h := s.Formats(d.Repo.PackageType)
	if err := s.Hooks.BeforeUpload(ctx, d.Repo, d.Path); err != nil { // (P17, P27)
		return model.Item{}, err
	}
	info, err := s.Blobs.Put(ctx, d.Body) // content first, outside the transaction
	if err != nil {
		return model.Item{}, err
	}
	insp, err := inspect(ctx, h, info) // format.Inspector, optional
	if err != nil {
		return model.Item{}, err
	}
	var it model.Item
	err = s.Store.Tx(ctx, func(ctx context.Context) error {
		if err := admit(ctx, h, d, insp); err != nil { // format.Admitter
			return err
		}
		if it, err = s.Store.Items().Put(ctx, d.Target.Repo.Key, d.Path, info); err != nil {
			return err
		}
		if err := h.Deployed(ctx, s.env(insp), d.Repo, it); err != nil { // pkgindex.Link inside
			return err
		}
		return s.Outbox.Append(ctx, events.ItemDeployed(it)) // (P11) same transaction
	})
	return it, err
}
```

The whole write stays inside the core. The only boundary it crosses is the store
port, and the transaction travels in the context exactly as it does today.

### Access and identity

```text
internal/core/port/driving/auth.go                Authenticator: Authenticate(r credentials) (auth.Principal, error)
internal/core/port/driving/access.go              Access: For(ctx, principal) (*authz.Decider, error)
internal/core/port/driving/identity.go            Identity: users, groups, tokens, keys, permissions, roles
internal/core/service/auth/                       ← internal/auth
  authenticator.go                                credential dispatch: basic, token, API key, OIDC
  principal.go                                    Principal and its context key
  password.go policy.go suspension.go             password rules, lockout
  current.go current_registry.go deferred.go      current-credential checks for queued work
  download.go                                     signed-download credentials
  seed.go                                         built-in admin and anonymous principals
  token.go                                        token verification entry
  token/   jwt.go keys.go token.go                ← internal/auth/token
  idtoken/ idtoken.go                             ← internal/auth/idtoken (exchange; keys via driven.JWKS)
internal/core/authz/                              ← internal/authz
  authz.go pattern.go repopatterns.go             Decider: pure evaluation over Sources
internal/core/service/identity/ permission/ roles/ oidc/    ← internal/service/<same name>
internal/core/service/reindexauth/                ← internal/reindexauth
internal/core/keymaterial/                        ← internal/keymaterial
internal/core/port/driven/keyring.go              Keyring
internal/adapter/driven/keyring/                  ← internal/crypt (sealed columns, rotation)
internal/adapter/driven/jwks/                     discovery and JWKS fetch with caching
internal/core/service/project/                    (P11) projects, membership, environments, quotas
internal/core/service/directory/                  (P12) LDAP sync over driven.Directory
internal/adapter/driven/ldap/                     (P12)
internal/core/service/session/ mfa/               (P21) sessions, approvals, MFA policy
internal/core/service/saml/ humanoidc/ crowd/ httpsso/   (P21) login flows over driven.IdP
internal/adapter/driven/saml/ oidcidp/ crowd/     (P21)
internal/core/service/scim/                       (P22) reconciliation and deprovisioning
internal/adapter/driving/http/gen/scim/           (P22) generated SCIM server
internal/adapter/driving/http/management/access/  hand-written access families (see the root mapping)
internal/adapter/driving/web/                     (P21) login and approval pages
```

A protocol handler gets the principal from the request context, set by
`adapter/driving/http/middleware.go`, which calls `driving.Authenticator`. It asks
`driving.Access.For(ctx, p)` for a `*authz.Decider` once per request and asks it
questions. Neither the middleware nor the handler sees the token service or the
store.

### Storage and persistence

```text
internal/core/port/driven/                        ← pkg/store (interfaces only)
  store.go                                        Store with 31 accessors, Tx, AfterCommit, Close
  content.go identity.go build.go operations.go peers.go enterprise.go
  remotecache.go gitlfs_locks.go properties.go work.go aqlsource.go layout_sources.go normalise.go
  remotecache_impl.go                             ← shared helper; moves to core/service/remote
  errors.go                                       ErrNotFound, ErrConflict, ...
  blob.go                                         ← pkg/blob/blob.go
  cdn.go                                          ← pkg/cdn/cdn.go (Signer)
internal/adapter/driven/sqlstore/
  sqlcommon/                                      ← internal/sqlstore/sqlcommon
    dialect.go migrate.go store.go unitofwork.go  dialect contract, migrator, Store, unit of work
    content.go content_items.go content_index.go blobs.go replicas.go remotecache.go
    identity.go identity_keys.go builds.go build_search.go operations.go operations_cleanup.go
    operations_config.go operations_webhooks.go peers.go enterprise.go gitlfs_locks.go
    repository_headers.go reseal.go work.go
  sqlite/   sqlite.go  migrations/0001_content.sql ... 0017_gitlfs_locks.sql
  postgres/ postgres.go migrations/0001_content.sql ... 0017_gitlfs_locks.sql
  mysql/    (P34) mysql.go migrations/
internal/adapter/driven/inmemstore/               ← pkg/store/inmem (reference backend, 17 files)
internal/adapter/driven/blob/
  fs/ s3/ gcs/ azure/                             ← pkg/blob/<same name>
  mirror/   mirror.go read.go repair.go status.go upload.go
  session/                                        ← pkg/blob/session (durable upload sessions)
  cold/                                           (P16)
internal/adapter/driven/cdn/
  cloudfront/ cloudfront.go                       ← pkg/cdn/cloudfront.go
  cloudcdn/   cloudcdn.go                         ← pkg/cdn/cloudcdn.go
  frontdoor/  frontdoor.go                        ← pkg/cdn/frontdoor.go
internal/contract/
  storetest/                                      ← pkg/store/storetest (15 files, 62 subtests)
  sqltest/                                        ← internal/sqlstore/sqltest
  blobtest/ cdntest/ aqltest/                     ← pkg/blob/blobtest, pkg/cdn/cdntest, pkg/aql/aqltest
```

Interfaces live in the core; one SQL adapter owns every table and every migration.
The roadmap's new tables (projects, webhooks, outbox consumers, evidence, policies,
findings) are all added to `sqlcommon` and to the migration directories of every
dialect. Contract suites run against `inmemstore`, `sqlite`, `postgres` and later
`mysql` from each adapter's own tests.

### Remote and virtual repositories

```text
internal/core/service/source/                     ← internal/source
  source.go gitlfs_action.go                      authorised read boundary shared by adapters
internal/core/service/remote/                     ← internal/remote (33 of 40 files)
  policy.go rules.go fetch.go flight.go circuit.go destination.go discovery.go protocol.go
                                                  retrieval decision table, singleflight, assumed-offline
  listing.go listing_html.go listing_mapped.go listing_smart.go
                                                  remote browsing and permission-safe mapping
  maven.go maven_eager.go golang.go golang_zip.go module_location.go nuget_empty_feed.go
                                                  per-format upstream protocols
  registry.go registry_children.go registry_content.go registry_descriptors.go registry_pages.go
                                                  Docker and OCI upstream protocol
  smart.go smart_aql.go smart_discovery.go smart_peer.go smart_properties.go smart_queue.go
  smart_request.go smart_statistics.go smart_status.go smart_trace.go
                                                  Smart Remote synchronisation
  npm.go pypi.go nuget.go helm.go terraform.go    upstream logic moved from protocol packages
internal/core/port/driven/upstream.go             Upstream: Do(ctx, req, revision) with credentials
internal/adapter/driven/httpupstream/             ← internal/remote (7 of 40 files)
  client.go pool.go credentials.go security.go url.go registry_challenge.go registry_token.go
internal/adapter/driven/fakeupstream/             ← internal/fakeupstream (fault modes)
internal/core/service/virtual/                    (P10)
  resolve.go                                      ordered member resolution, defaultDeploymentRepo
  merge.go                                        per-format merge entry point, virtualRetrievalCachePeriodSecs
internal/core/format/<ecosystem>/virtual.go       (P10) per-format metadata merging (format.Merger)
internal/adapter/driving/http/protocol/<eco>/     route virtual keys to driving.Virtual
```

### Workers (P17), a roadmap domain end to end

```text
api/src/worker/                                   unchanged; plus v2 endpoints (P17 task 0)
internal/adapter/driving/http/gen/worker/         generated strict server
internal/adapter/driving/http/management/workers/
  workers.go                                      implements the worker strict server over driving.Workers
internal/core/port/driving/workers.go             Workers: Create, Update, Delete, Test, Execute,
                                                  Rerun, History
internal/core/port/driven/hooks.go                ActionHooks: BeforeDownload, BeforeUpload,
                                                  AfterCreate, AfterDelete, ...
internal/core/port/driven/sandbox.go              Sandbox: Execute(ctx, Script, Event, Limits)
internal/core/service/workers/
  workers.go                                      catalogue, CRUD, enablement
  hooks.go                                        implements driven.ActionHooks: finds bound
                                                  workers, calls driven.Sandbox, maps the verdict
  executions.go                                   execution history in driven.WorkerStore
  transpile.go                                    esbuild invocation (pure transform)
internal/core/service/item/download.go            calls driven.ActionHooks.BeforeDownload
internal/adapter/driven/sandbox/
  pool.go                                         helper-process pool, one execution per helper
  ipc.go                                          framing between parent and helper
  limits.go                                       64 MiB, 1 s CPU, 5 s per call, 20 s overall
internal/adapter/driving/workerhost/
  host.go                                         the helper's loop: read request, run goja, reply
  bindings.go                                     platform client with allow-list
internal/adapter/driving/cli/workerhost.go        hidden worker-host subcommand
internal/adapter/driven/sqlstore/sqlcommon/enterprise.go   worker tables (exist today)
internal/contract/sandboxtest/                    timeouts, memory limit, crash recovery
test/acceptance/features/workers.feature          BEFORE_DOWNLOAD blocks, AFTER_CREATE sets a property
```

The core offers the hook through `driven.ActionHooks`. The item service calls the
port; `core/service/workers` implements it. Both are core packages, so the
inversion is between siblings: rule 7 does not cover services, and nothing in the
dependency rules stops `item` from importing `workers` directly. A ninth rule
(`core/service/item` imports no other `core/service` package except `pkgindex` and
`work`) would be needed to hold the line.

### Webhooks and events (P11, P13)

```text
internal/core/model/event.go                      (P11) Event: kind, subject, payload, at
internal/core/service/events/
  catalogue.go                                    (P11) the exact JFrog event names and criteria
  registry.go                                     (P11) subscribers by event kind
internal/core/port/driven/outbox.go               (P11) Outbox: Append(ctx, Event) inside Tx
internal/core/port/driving/events.go              (P11) Subscriber: Handle(ctx, Event) error
internal/adapter/driven/sqlstore/sqlcommon/peers.go   OutboxStore exists today; becomes the
                                                  backing table for driven.Outbox
internal/adapter/driving/events/
  dispatcher.go                                   (P11) polls the outbox, calls subscribers,
                                                  acknowledges, retries with backoff
internal/core/service/webhook/
  webhook.go deliver.go                           (P11) Subscriber for webhooks; sends via driven.Delivery
internal/adapter/driven/webhookhttp/              (P11) HMAC signing, delivery log
internal/core/service/federation/
  outbound.go                                     (P13) Subscriber that queues peer events
  apply.go                                        (P13) idempotent apply of inbound events
internal/adapter/driven/peerclient/               (P13) signed requests to peers
internal/adapter/driving/peer/                    (P13) inbound peer API over http/gen/peer
```

Events fit the hexagon well as mechanism: append is a driven port, dispatch is a
driving adapter, consumers are core services. What the hexagon does not provide is a
rule about which service may subscribe to which event; every subscriber is in the
same core and may read anything.

### Composition root

```text
internal/app/                                     ← internal/app
  app.go                                          Run: open runtime, build adapters, serve, drain
  runtime.go                                      runtime struct: every driven adapter and service
  store.go                                        OpenStore, Migrate, RotateKeys, Keyring
  blob.go                                         hot and cold tiers, mirror, CDN signers
  remote.go                                       upstream pool, sources, policy key
  descriptor.go seed.go instance.go               bootstrap descriptor, seed principals, instance id
  gc.go mirrors.go                                periodic GC, mirror repair
  services.go                                     NEW: constructs every core service from adapters
  router.go                                       NEW: builds http.Config with driving ports
  events.go                                       (P11) subscriber registration
  hooks.go                                        (P17) ActionHooks composition (workers, curation, policy)
```

```go
// internal/app/services.go at P9c (shape)
func buildServices(rt *runtime) *services {
	idx := &pkgindex.Index{Store: rt.store}
	items := &item.Service{Store: rt.store, Blobs: rt.blobs, Formats: formats.For, Work: rt.work}
	return &services{
		content:  items,
		packages: items,
		builds:   &build.Service{Store: rt.store, Items: items},
		registry: &registry.Service{Store: rt.store, Blobs: rt.blobs, Items: items},
		auth:     auth.NewAuthenticator(rt.store, rt.keys, rt.jwks),
		access:   &authz.Factory{Sources: authz.StoreSources(rt.store)},
		identity: &identity.Service{Store: rt.store, Keys: rt.keys},
		remote:   remote.New(rt.store, rt.upstream, items, idx),
		search:   &search.Service{Store: rt.store, Repos: rt.store.Repos()},
		storage:  &storage.Service{GC: rt.gc, Trash: &trash.Service{Store: rt.store}},
		// ... about 20 services at P9c
	}
}

func buildRouter(cfg config.Config, s *services) http.Handler {
	return httpadapter.New(httpadapter.Config{
		Content: s.content, Packages: s.packages, Builds: s.builds, Registry: s.registry,
		Auth: s.auth, Access: s.access, Identity: s.identity, Remote: s.remote,
		Search: s.search, Storage: s.storage, BaseURL: cfg.BaseURL,
	})
}
```

At P41 the same two functions construct about sixty services and pass about
thirty-five driving ports to the HTTP adapter, plus the peer, scheduler, worker host,
GraphQL and console adapters. `services.go` alone would run to several hundred
lines, and every lane in the plan edits it.

### Generated code and API specifications

```text
api/
  src/<service>/*.yaml                            fragments (artifactory, access, distribution,
                                                  event, grid, pfed, worker; peer in P13;
                                                  evidence, lifecycle, apptrust, xray, curation in P23 to P27)
  <service>.openapi.yaml                          bundled by internal/tooling/openapibundle
  oapi-server-<service>.yaml                      output: internal/adapter/driving/http/gen/<service>/server.gen.go
  oapi-client-<service>.yaml                      output: pkg/client/<service>/client.gen.go (unchanged)
internal/adapter/driving/http/gen/<service>/     strict servers
internal/tooling/stubgen/                         501 stubs for unimplemented strict operations
internal/tooling/specoracle/                      parity comparison with cached JFrog definitions
```

| Generated strict server | Implemented by |
| --- | --- |
| gen/artifactory | `http/management/{storage,search,builds,repositories,system,remote}` composed into one type in `http/handwritten.go` |
| gen/access | `http/management/access` |
| gen/event | `http/management/webhooks` (P11) |
| gen/distribution | `http/management/distribution` (P15) |
| gen/grid, gen/pfed | `http/management/federation` (P14) |
| gen/worker | `http/management/workers` (P17) |
| gen/peer | `adapter/driving/peer` (P13) |
| gen/evidence and later specs | the matching `http/management/<family>` (P23 to P27) |

Generated code is entirely an adapter concern. The core never imports a generated
type; handlers convert between generated request types and `driving` DTOs.

### Tests and enforcement

| Kind | Location |
| --- | --- |
| Unit tests | beside the code, in every core and adapter package |
| Core service tests | `core/service/*`, with fakes of driven ports; `adapter/driven/inmemstore` where a whole store is easier |
| Driven adapter contract runs | each adapter's `_test.go` calls `contract/storetest.Run`, `contract/blobtest.Run`, `contract/cdntest.Run` |
| Format contract runs | `core/format/<eco>/formattest_test.go` calls `contract/formattest.Run` |
| Handler tests | `adapter/driving/http/...`, with fakes of driving ports |
| Server-assembly suites | `test/server/<family>` (unchanged), importing `adapter/driving/http` and `app` |
| Acceptance | `test/acceptance` (unchanged) |
| Platform and Terraform | `test/platform`, `test/terraform` (unchanged) |

```go
// internal/layout/layout_test.go (shape)
var forbidden = map[string][]string{
	"internal/core/":             {mod + "/internal/adapter/", mod + "/internal/app", mod + "/internal/contract/", mod + "/cmd/"},
	"internal/core/port/":        {mod + "/internal/core/service/", mod + "/internal/core/format/"},
	"internal/core/format/":      {mod + "/internal/core/service/"},
	"internal/adapter/driven/":   {mod + "/internal/adapter/driving/", mod + "/internal/app"},
	"internal/adapter/driving/":  {mod + "/internal/adapter/driven/", mod + "/internal/core/service/"},
	"pkg/":                       {mod + "/internal/", mod + "/cmd/"},
}

// exceptions are explicit and few.
var allowed = map[string][]string{
	"internal/adapter/driving/cli/": {mod + "/internal/app"},
}
```

Coverage implications: about 190 packages at P9c and about 300 at P41. Driving ports
make handler tests cheap, which helps the large protocol packages. The packages most
likely to struggle with a 90% per-package floor are thin port-only packages (which
have no statements and are excluded), the outbox dispatcher, the sandbox pool and
the helper host, whose failure paths need process-level fault injection.

## Walkthroughs

### An npm publish request

1. `internal/adapter/driving/http/server.go`: chi routes `PUT /artifactory/api/npm/{repo}/{pkg}` to the npm protocol router mounted through `pkgroute`.
2. `internal/adapter/driving/http/middleware.go`: calls `driving.Authenticator.Authenticate`, implemented in `internal/core/service/auth/authenticator.go`, and stores the `auth.Principal` in the context.
3. `internal/adapter/driving/http/protocol/npm/publish.go`: decodes the body with `body.go`, asks `driving.Access.For(ctx, p)` for a Decider and checks Deploy on the path.
4. `protocol/npm/publish.go`: calls `driving.Content.Deploy` with the tarball body and target.
5. `internal/core/service/item/item.go`: `Deploy` calls `driven.BlobStore.Put`; the adapter is `internal/adapter/driven/blob/s3`.
6. `internal/core/format/npm/tarball.go`: `Inspect` reads `package.json` through `driven.ArchiveReader` (`internal/adapter/driven/archive`).
7. `internal/core/service/item/item.go`: opens `driven.Store.Tx`; the unit of work is `internal/adapter/driven/sqlstore/sqlcommon/unitofwork.go`.
8. `internal/core/format/npm/policy.go`: `Admit` applies overwrite rules.
9. `internal/adapter/driven/sqlstore/sqlcommon/content_items.go`: the item row is written.
10. `internal/core/format/npm/npm.go`: `Deployed` builds index rows in `index.go` and calls `pkgindex.Link` (`internal/core/service/pkgindex`), which writes through `driven.PackageStore`.
11. `internal/core/service/item/item.go`: (P11) appends `ItemDeployed` to `driven.Outbox` in the same transaction.
12. `sqlcommon/unitofwork.go`: commits; `AfterCommit` wakes `core/service/work` for derived work.
13. `protocol/npm/publish.go`: writes the npm success response.

### A BEFORE_DOWNLOAD worker blocks a download (P17)

1. `internal/adapter/driving/http/management/storage/items.go`: `GET /artifactory/{repo}/{path}` authenticates and authorises as above.
2. It calls `driving.Download.Open`, implemented by `internal/core/service/item/download.go`.
3. `download.go` calls `driven.ActionHooks.BeforeDownload(ctx, repo, path, principal)`.
4. `internal/core/service/workers/hooks.go` implements the port, finds enabled workers bound to `BEFORE_DOWNLOAD` for the repository in `driven.WorkerStore`.
5. It calls `driven.Sandbox.Execute`, implemented by `internal/adapter/driven/sandbox/pool.go`, which takes an idle helper.
6. The helper is `artifactweave worker-host` (`internal/adapter/driving/cli/workerhost.go`), running `internal/adapter/driving/workerhost/host.go` with goja.
7. The script returns a stop verdict; `sandbox/ipc.go` returns it; `workers/hooks.go` records the execution in `executions.go`.
8. `download.go` returns a `driving.Denied` error with the worker's message.
9. `management/storage/items.go` maps it through `http/apierror` to 403 with the JFrog error envelope.

### Adding a new package type

Hypothetical `zigpkg`, local and remote classes:

1. `internal/core/repo/packagetypes.go`: add the type and its class matrix.
2. `internal/core/repo/displayname.go`: display name.
3. `internal/core/format/zigpkg/zigpkg.go`: Handler and Inspector.
4. `internal/core/format/zigpkg/index.go`: index rows.
5. `internal/core/format/zigpkg/zigpkg_test.go` and `formattest_test.go`, plus `testdata/`.
6. `internal/core/format/formats/formats.go`: register the handler.
7. `internal/core/service/remote/zigpkg.go`: upstream protocol.
8. `internal/adapter/driving/http/protocol/zigpkg/zigpkg.go`: routes through `pkgroute`.
9. `internal/adapter/driving/http/protocol/zigpkg/remote.go`: upstream-backed response shaping.
10. `internal/adapter/driving/http/protocol/zigpkg/*_test.go`.
11. `internal/adapter/driving/http/protocols.go`: mount the protocol.
12. `internal/config/zigpkg.go`: settings, if any.
13. `api/src/artifactory/paths-packagetypes.yaml`: management endpoints, if any.
14. `test/server/zigpkg/` and `test/server/support/zigpkg.go`.
15. `test/acceptance/features/zigpkg.feature`, `zigpkg_steps_test.go`, `clients/zigpkg.Dockerfile`.
16. `docs/implementation/package-format-inventory.json` and `tools/citestselection/test_ownership.json`.

About 20 files across 4 top-level areas of `internal/`.

### Adding a new business domain: Evidence (P23)

1. `api/src/evidence/*.yaml`, `api/oapi-server-evidence.yaml`, `api/oapi-client-evidence.yaml`.
2. `internal/adapter/driving/http/gen/evidence/` and `pkg/client/evidence/` (generated).
3. `internal/core/model/evidence.go`: attestation, subject, signer.
4. `internal/core/port/driving/evidence.go`: Evidence interface.
5. `internal/core/port/driven/evidence.go`: EvidenceStore; `store.go` gains `Evidence()`.
6. `internal/core/service/evidence/evidence.go`, `verify.go`, `subject.go`, `signers.go`.
7. `internal/core/service/evidence/events.go`: subscriber for `ItemDeleted` and `ItemMoved`.
8. `internal/adapter/driven/sqlstore/sqlcommon/evidence.go`.
9. `internal/adapter/driven/sqlstore/{sqlite,postgres,mysql}/migrations/00NN_evidence.sql`.
10. `internal/adapter/driven/inmemstore/evidence.go`.
11. `internal/contract/storetest/evidence.go`.
12. `internal/adapter/driving/http/management/evidence/evidence.go`.
13. `internal/app/services.go`, `router.go`, `events.go`: wiring.
14. `test/server/evidence/`, `test/acceptance/features/evidence.feature`.

The domain touches every top-level area of the tree, including the shared
`port/driven/store.go` and `app/services.go`.

## Package mapping

| Today's package | New location | Notes |
| --- | --- | --- |
| api | api | unchanged; parity test stays |
| cmd/artifactweave | cmd/artifactweave | command tree moves to adapter/driving/cli |
| internal/app | internal/app | adds services.go, router.go |
| internal/archive | internal/adapter/driven/archive | behind driven.ArchiveReader |
| internal/auth | internal/core/service/auth | |
| internal/auth/idtoken | internal/core/service/auth/idtoken | HTTP discovery split to adapter/driven/jwks |
| internal/auth/idtoken/idtokentest | internal/contract/idtokentest | |
| internal/auth/token | internal/core/service/auth/token | |
| internal/authz | internal/core/authz | pure logic |
| internal/buildinfo | internal/buildinfo | |
| internal/config | internal/config | |
| internal/crypt | internal/adapter/driven/keyring | behind driven.Keyring |
| internal/descriptor | internal/core/service/descriptor | |
| internal/fakeupstream | internal/adapter/driven/fakeupstream | test adapter |
| internal/format | internal/core/format | contract: Handler, Env, optional interfaces |
| internal/format/formats | internal/core/format/formats | |
| internal/format/formattest | internal/contract/formattest | |
| internal/format/generic | internal/core/format/generic | |
| internal/format/docker | internal/core/format/docker | also serves oci and helmoci |
| internal/format/terraform | internal/core/format/terraform | |
| internal/format/terraform/terraformtest | internal/contract/terraformtest | |
| internal/format/terraformbackend | internal/core/format/terraformbackend | pairs with core/service/terraformstate |
| internal/format/gitlfs | internal/core/format/gitlfs | lock store via driven.GitLFSLockStore |
| internal/format/<ecosystem> (the other 27) | internal/core/format/<ecosystem> | same names |
| internal/keymaterial | internal/core/keymaterial | pure |
| internal/layout | internal/layout | rules rewritten |
| internal/openapibundle | internal/tooling/openapibundle | |
| internal/reindexauth | internal/core/service/reindexauth | |
| internal/remote | internal/core/service/remote (33 files) and internal/adapter/driven/httpupstream (7 files) | split by transport versus policy |
| internal/repos | internal/core/service/repos | |
| internal/server | internal/adapter/driving/http and http/management/<family> | see the root file mapping |
| internal/server/apierror | internal/adapter/driving/http/apierror | |
| internal/server/baseurl | internal/adapter/driving/http/baseurl | |
| internal/server/gen/<spec> | internal/adapter/driving/http/gen/<spec> | oapi output paths change |
| internal/server/pkgroute | internal/adapter/driving/http/pkgroute | |
| internal/server/docker | internal/adapter/driving/http/protocol/docker | gains docker_rest.go |
| internal/server/golang | internal/adapter/driving/http/protocol/golang | |
| internal/server/vcs | internal/adapter/driving/http/protocol/vcs | gains vcs_*.go from the root |
| internal/server/<protocol> (the other 24) | internal/adapter/driving/http/protocol/<protocol> | upstream logic moves to core/service/remote |
| internal/service/build | internal/core/service/build | |
| internal/service/gc | internal/core/service/gc | |
| internal/service/identity | internal/core/service/identity | |
| internal/service/item | internal/core/service/item | implements driving.Content |
| internal/service/multipart | internal/core/service/multipart | |
| internal/service/oidc | internal/core/service/oidc | |
| internal/service/permission | internal/core/service/permission | |
| internal/service/pkgindex | internal/core/service/pkgindex | formats reach it through format.Env |
| internal/service/registry | internal/core/service/registry | |
| internal/service/roles | internal/core/service/roles | |
| internal/service/terraformstate | internal/core/service/terraformstate | |
| internal/service/trash | internal/core/service/trash | |
| internal/service/usage | internal/core/service/usage | |
| internal/service/work | internal/core/service/work | queue logic; tables in sqlstore |
| internal/source | internal/core/service/source | |
| internal/specoracle | internal/tooling/specoracle | |
| internal/sqlstore/sqlcommon | internal/adapter/driven/sqlstore/sqlcommon | |
| internal/sqlstore/sqlite | internal/adapter/driven/sqlstore/sqlite | |
| internal/sqlstore/postgres | internal/adapter/driven/sqlstore/postgres | |
| internal/sqlstore/sqltest | internal/contract/sqltest | |
| internal/stubgen | internal/tooling/stubgen | |
| internal/testfault | internal/adapter/driven/testfault | |
| pkg/aql | internal/core/aql | no longer public |
| pkg/aql/aqltest | internal/contract/aqltest | |
| pkg/blob | internal/core/port/driven (blob.go) | interface only |
| pkg/blob/fs, s3, gcs, azure, mirror, session | internal/adapter/driven/blob/<same name> | |
| pkg/blob/blobtest | internal/contract/blobtest | |
| pkg/cdn | internal/core/port/driven (cdn.go) and internal/adapter/driven/cdn/<provider> | split by provider |
| pkg/cdn/cdntest | internal/contract/cdntest | |
| pkg/client/<spec> | pkg/client/<spec> | unchanged |
| pkg/containertest | pkg/containertest | unchanged |
| pkg/layout | internal/core/layout | |
| pkg/repo | internal/core/repo | |
| pkg/store | internal/core/port/driven | interfaces; remotecache_impl.go to core/service/remote |
| pkg/store/inmem | internal/adapter/driven/inmemstore | |
| pkg/store/storetest | internal/contract/storetest | |
| pkg/types | internal/core/model | |
| pkg/types/wire | internal/core/model/wire | |
| test/acceptance | test/acceptance | unchanged |
| test/platform | test/platform | unchanged |
| test/server/<family> and test/server/shared | test/server/<family> | unchanged location; imports change |
| test/server/support, agentcommon, registryfixture | unchanged | imports change |
| tools/* | tools/* | unchanged; three import internal/tooling |

## Numbers

Estimates, rounded.

| Measure | P9c | P41 |
| --- | --- | --- |
| Go packages under internal/ and pkg/ | about 190 | about 300 |
| Import rules in the layout test | 8 (6 forbidden prefixes, 1 exception, plus pkg) | 9 with the service-isolation rule the roadmap needs |
| Interfaces a new ecosystem implements | format.Handler, optionally Inspector, Admitter, Resolver, Reindexer, Worker, Merger | same |
| Files touched to add an ecosystem | about 20, in 4 areas of internal/ | same |
| Files touched to add a domain | about 25, in every area of internal/ plus app | same, and growing as shared files grow |
| Driving port files | about 15 | about 35 |
| Core services | about 25 | about 60 |
| Largest package | adapter/driving/http/gen/artifactory, about 63k lines generated; hand-written: sqlstore/sqlcommon, about 10k lines | sqlcommon grows past 25k lines as every domain adds tables |

## Placement choices and friction

Placement choices settled in this file:

- **Driven ports live in the core, not in `pkg/`.** The store, blob and CDN
  interfaces move into `internal/core/port/driven` and their backends into
  `internal/adapter/driven`. Keeping them public in `pkg/` would make the tree
  hexagonal in substance but not in shape, and the greenfield stance has no public
  contract to protect. The generated clients and `containertest` stay public.
- **One driven port package, not one per port.** `driven` holds every outward
  interface in separate files. One package per port would add about twenty small
  packages with no statements.
- **Formats are core.** Format rules are business rules, so `internal/core/format`
  sits inside the hexagon and protocols sit outside it.
- **Upstream protocol logic is core, transport is an adapter.** `internal/remote`
  is split by file: 33 files of policy and protocol stay in the core, 7 files of HTTP
  transport and credentials become `adapter/driven/httpupstream`.
- **The outbox dispatcher is a driving adapter.** It is a loop that drives core
  subscribers, like the scheduler and the HTTP server.
- **The CLI is a driving adapter.** `cmd/artifactweave` keeps only `main.go`.

Friction, stated plainly:

- **One core, about sixty services.** `internal/core/service/` has no internal
  boundary. Governance, security, evidence and storage are siblings, and nothing in
  the rules stops `core/service/apptrust` importing `core/service/item` directly.
  The ninth rule mentioned under Workers is the first of several such exceptions.
- **Shared files for every lane.** `core/port/driven/store.go`,
  `core/port/driving/types.go`, `core/model/`, `adapter/driven/sqlstore/sqlcommon/`
  and `app/services.go` are edited by every domain on the roadmap. Parallel lanes
  after P20 will meet in these files.
- **DTOs move out of services.** `item.Deploy`, `item.Target` and similar request
  types must live in `core/port/driving/types.go` so handlers can use them without
  importing the service. That separates each service from its own request types.
- **Wide driving ports.** `driving.Content` carries about fifteen methods today and
  gains download hooks, virtual paths and archive moves. Fakes of it are large.
- **An ecosystem lives in three places.** `core/format/npm`,
  `core/service/remote/npm.go` and `adapter/driving/http/protocol/npm` are far apart
  in the tree. Review and ownership of one ecosystem spans three directories.
- **Interception is between siblings.** `driven.ActionHooks` is implemented by core
  services (workers, curation, policy), so the dependency inversion is real at the
  type level but provides no package-level boundary.
- **The AQL SQL builder stays in the core.** `core/aql/sql.go` and `joinsql.go`
  build SQL, which is arguably infrastructure. Moving them to `sqlstore` would need
  an AST-to-dialect port; this file leaves them in place.
- **The composition root concentrates the system.** `internal/app` is the only
  package that sees everything; at P41 it is the largest hand-written coordination
  point in the codebase.
