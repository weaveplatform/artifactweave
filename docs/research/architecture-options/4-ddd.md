# Option 4: Domain-driven design (tactical and strategic), detailed layout

This file is the detailed layout for option 4 in
[ARCHITECTURE-OPTIONS.md](../ARCHITECTURE-OPTIONS.md). It follows the same section
order as the other four layout files so they can be compared side by side.

## Summary

The tree is organised by business subject first. Nineteen bounded contexts each own
their model, their tables, their HTTP surface and their tests. Inside every context the
layering is the same: `domain/` holds aggregates, value objects, repository interfaces
and domain events; `application/` holds command and query handlers and event handlers;
`adapters/` holds HTTP, persistence and external-system code; `api/` is the published
interface other contexts may call. The one idea that organises the tree is the
aggregate as the consistency boundary: a transaction changes exactly one aggregate, and
everything else that must follow from that change follows from a domain event written to
the outbox in the same transaction.

Dependency rules

1. A package under `internal/<context>/` imports only `internal/kernel/...`,
   `internal/shared/...`, `pkg/...`, its own context's subtree, and other contexts'
   `internal/<other>/api` packages.
2. `internal/<context>/domain/...` imports only the standard library, `internal/kernel/...`
   and its own context's `domain/...`. It never imports `application/`, `adapters/`,
   `internal/shared/...` or any `api/`.
3. `internal/<context>/application/...` never imports `internal/<context>/adapters/...`.
4. `internal/<context>/api` imports only `internal/kernel/...` and its own `domain/`
   value types it chooses to publish. It never imports `application/` or `adapters/`.
5. `internal/kernel/...` imports nothing under `internal/` except `internal/kernel/...`.
6. `internal/shared/...` imports no context.
7. Only `internal/app` imports a context's root constructor package (`internal/<context>`),
   and only `internal/app` imports more than one context's adapters.
8. `pkg/...` never imports `internal/...` or `cmd/...`.

## Repository root

```text
artifactweave/
  cmd/
    artifactweave/              unchanged: cobra commands, calls app.Run; gains `worker-host` (P17)
  internal/                     CHANGED: nineteen contexts, kernel, shared, app (was app, server,
                                service, format, sqlstore, auth and friends)
  pkg/                          CHANGED: only generated clients and containertest remain public;
                                store, blob, cdn, aql, layout, repo and types move into contexts
  api/                          CHANGED: fragments regrouped by context under api/src/<spec>/<context>/
                                so each context's owner edits its own fragment files; bundled
                                specs and oapi configs stay at api/ root
  test/
    acceptance/                 unchanged (black-box godog suite)
    server/                     CHANGED: grouped by context, test/server/<context>/<subject>/
    platform/ terraform/ oci/   unchanged
  tools/                        CHANGED paths only: citestselection and formatinventory key on
                                internal/packages/<ecosystem>/ instead of internal/format and
                                internal/server
  docs/                         CHANGED: docs/implementation/repo-layout.md rewritten as a context
                                map; one context README per context
  deploy/                       unchanged
  Makefile                      CHANGED: generate target writes gen output to internal/shared/gen
  .golangci.yml                 CHANGED: depguard rules per context mirror the dependency rules
  .testcoverage.yml             CHANGED: exclusions for internal/shared/gen and pkg/client;
                                per-package floor now applies to roughly 260 packages
  go.mod                        unchanged: one module
```

## Full package tree

Legend: `← path` is today's origin; `(Pn)` is a roadmap addition. Contexts are listed in
the order of the context map. Every context has `api/`, `domain/`, `application/` and
`adapters/`; small contexts still have all four (the option's uniform layering).

```text
internal/
  app/                                  ← internal/app (composition root only; seed and runtime split out)
  kernel/                               shared kernel: value objects every context uses
    ids/                                RepoKey, Path, PackageType, Checksum, SHA256 (new; from pkg/types, pkg/repo)
    principal/                          Principal, Subject, Scope   ← pkg/types principal.go scope.go
    version/                            version comparators         ← pkg/types version.go
    repo/                               repository configuration value object, validation, defaults,
                                        package types, display names, flexbool, views
                                                                    ← pkg/repo
    layout/                             repository layout pattern engine ← pkg/layout
    wire/                               JFrog JSON shapes           ← pkg/types/wire
    clock/                              Clock interface
  shared/                               infrastructure with no business rules
    sqlcommon/                          dialect, migrator per context, unit of work, Queryer
                                                                    ← internal/sqlstore/sqlcommon (dialect.go,
                                                                      migrate.go, unitofwork.go, store.go only)
    sqlite/ postgres/                   driver open, pragmas, pools ← internal/sqlstore/sqlite, postgres
    mysql/                              (P34)
    sqltest/                            container and dialect harness ← internal/sqlstore/sqltest
    outbox/                             transactional outbox: Append(ctx, Event) inside the caller's tx (P11, new)
    eventbus/                           dispatch from outbox to subscribed handlers, ordering, retries,
                                        dead letters, idempotency keys (P11, new)
    work/                               persistent work queue, leases ← internal/service/work + pkg/store work.go
    crypt/                              keyring, sealed columns     ← internal/crypt
    archive/                            bounded zip/tar walker      ← internal/archive
    httpkit/                            chi router assembly, middleware order, trace
                                                                    ← internal/server server.go middleware.go
                                                                      context.go trace.go protocols.go
      apierror/                         ← internal/server/apierror
      baseurl/                          ← internal/server/baseurl
      pkgroute/                         router kit for native protocols ← internal/server/pkgroute
    gen/                                generated strict servers    ← internal/server/gen
      access/ artifactory/ distribution/ event/ grid/ pfed/ worker/
      peer/                             (P13)
    testfault/                          failing fakes               ← internal/testfault
    containers/                         alias of pkg/containertest for internal suites (optional)
    buildinfo/                          ← internal/buildinfo
    config/                             process configuration (strict YAML, flag > env > file)
                                                                    ← internal/config
    tooling/
      openapibundle/                    ← internal/openapibundle
      stubgen/                          ← internal/stubgen
      specoracle/                       ← internal/specoracle

  content/                              CONTEXT: items, folders, blobs, trash, gc, multipart, download
    api/                                ContentAPI (commands other contexts may issue), RepositoryView,
                                        ItemView, DownloadGate interface, published events
    domain/
      repository/                       Repository aggregate (config, class, lifecycle) ← internal/repos
      item/                             Item and Folder aggregates, Properties value object
      blob/                             BlobReference aggregate (refcount, tier), Store port ← pkg/blob blob.go
      trash/                            TrashEntry aggregate ← internal/service/trash
      upload/                           UploadSession aggregate (multipart, resumable) ← internal/service/multipart
      events.go                         ItemDeployed, ItemDeleted, ItemMoved, ItemCopied,
                                        PropertiesChanged, RepositoryCreated, RepositoryDeleted
    application/
      deploy/                           DeployFile, DeployByChecksum, MakeFolder ← internal/service/item
      remove/                           Delete, Purge, Restore    ← internal/service/item remove.go restore.go
      transfer/                         Copy, Move                ← internal/service/item transfer*.go
      explode/                          archive explode and entries ← internal/service/item archive.go
      gc/                               garbage collection        ← internal/service/gc
      download/                         Resolve, ranged read, redirect decision ← internal/server redirect.go
      repositories/                     create, update, delete repositories ← internal/repos
      usage/                            storage summary           ← internal/service/usage
    adapters/
      http/                             storage, items, trash, transfer, multipart, repositories handlers
                                                                    ← internal/server items.go storage.go ...
      persistence/
        sql/                            content tables, migrations/{sqlite,postgres,mysql}/
                                                                    ← sqlcommon content*.go blobs.go replicas.go
        inmem/                          ← pkg/store/inmem content*.go blobs.go replicas.go
        persistencetest/                ← pkg/store/storetest content.go core.go replicas.go properties.go
      blob/
        fs/ s3/ gcs/ azure/ mirror/ session/   ← pkg/blob/*
        cold/                           (P16 cold tier member, configured beside hot)
        blobtest/                       ← pkg/blob/blobtest
      cdn/                              CloudFront, Cloud CDN, Front Door signers ← pkg/cdn
        cdntest/                        ← pkg/cdn/cdntest

  packages/                             CONTEXT: package model, index, 49 ecosystems, registry sessions
    api/                                PackagesAPI: Admit (synchronous validation), Metadata, Merge (P10)
    domain/
      pkg/                              Package aggregate             ← pkg/types source.go (package parts)
      version/                          PackageVersion aggregate, FileLink entity ← internal/service/pkgindex
      rules/                            FormatRules interface: Inspect, Admit, Index, Unindex
                                                                    ← internal/format format.go sidecar.go snapshots.go
      events.go                         PackageVersionIndexed, PackageVersionRemoved, TagMoved
    application/
      index/                            OnItemDeployed, OnItemDeleted, OnItemMoved handlers; Reconcile
                                                                    ← internal/service/pkgindex, item format.go
      reindex/                          repository reindex commands ← internal/service/item work.go, server reindex.go
      registry/                         RegistryUpload aggregate handlers ← internal/service/registry
      formats/                          package type to FormatRules lookup ← internal/format/formats
    adapters/
      persistence/sql/ inmem/ persistencetest/   packages, versions, version_files tables
                                                                    ← sqlcommon content_index.go
      formattest/                       contract suite every ecosystem runs ← internal/format/formattest
    <ecosystem>/                        one sub-context per package type, each with the same four folders:
      domain/                           format rules: inspect, admit, index
      application/                      protocol commands and derived-metadata work
      adapters/http/                    native protocol routes
      adapters/upstream/                remote protocol parsing (only where the type has a remote class)
    The 49 ecosystem directories:
      agentpackages agentplugins aieditorextensions alpine ansible bazelmodules bower cargo chef
      cocoapods composer conan conda cran debian docker (also serves helmoci) generic gitlfs golang
      (type go) helm hex huggingfaceml jetbrainsplugins luarocks machinelearning maven (also serves
      gradle, ivy, sbt) nim (← internal/server/nimmodel) nix npm nuget opkg p2 pub puppet pypi rpm
      skills swift terraform terraformbackend vagrant vcs
    terraformstate/                     Workspace, StateVersion, Lock aggregates ← internal/service/terraformstate
                                        (kept beside terraformbackend; see placement choices)
    gitlfslocks/                        Lock aggregate                ← pkg/store gitlfs_locks.go

  builds/                               CONTEXT: build info, promotion, build retention
    api/                                BuildsAPI: Get, ListArtifacts (for search and releases)
    domain/run/                         BuildRun aggregate (modules, artifacts, dependencies as values),
                                        Promotion entity, BuildPublished, BuildPromoted, BuildDeleted
    application/                        Publish, Append, Rename, Delete, Promote, ApplyRetention
                                                                    ← internal/service/build
    adapters/http/                      ← internal/server builds.go
    adapters/persistence/{sql,inmem,persistencetest}/ ← sqlcommon builds.go, inmem builds.go, storetest builds.go

  search/                               CONTEXT: AQL and the search families
    api/                                SearchAPI (for console and OneModel)
    domain/aql/                         grammar, AST, criteria, domains, eval ← pkg/aql (parse, eval, criteria ...)
    domain/projection/                  ItemRow, BuildRow, PropertyRow read models (new)
    application/                        RunAQL, AQLActions (items.delete, properties.update dispatch to
                                        content api), GET searches, transitive search, projection updaters
                                        subscribed to content and builds events
    adapters/http/                      ← internal/server aql*.go searches*.go
    adapters/sql/                       AQL compiler per dialect, projection tables
                                                                    ← pkg/aql sql.go joinsql.go, sqlcommon build_search.go
    adapters/aqltest/                   ← pkg/aql/aqltest

  remote/                               CONTEXT: upstream retrieval, cache policy, Smart Remote
    api/                                RemoteAPI: Fetch, Listing, Status
    domain/
      cache/                            CacheEntry aggregate (origin, expiry, negative cache) ← pkg/types remotecache.go
      upstream/                         UpstreamHealth aggregate (circuit, assumed offline) ← internal/remote circuit.go
      smart/                            SmartSubscription aggregate ← internal/remote smart*.go
    application/
      fetch/                            retrieval decision table, HEAD before GET, singleflight
      source/                           authorised read boundary ← internal/source
      maintenance/                      zap, cache cleanup ← internal/service/item zap.go cache*.go
      listing/                          browsing, mapped listing ← internal/remote listing*.go
    adapters/
      transport/                        pool, client, TLS, credentials, destination ← internal/remote client.go ...
      registryauth/                     Docker token challenge ← internal/remote registry_challenge.go registry_token.go
      http/                             smart status, remote browsing ← internal/server smart_status.go source*.go
      persistence/{sql,inmem,persistencetest}/ ← sqlcommon remotecache.go, inmem remotecache.go
      fakeupstream/                     ← internal/fakeupstream (test support)

  virtual/                              CONTEXT (P10): member resolution, metadata merging
    api/
    domain/resolution/                  ResolutionPlan value object, VirtualCacheEntry aggregate
    application/                        Resolve, MergeMetadata (calls packages api Merge per type)
    adapters/http/ adapters/persistence/sql/

  access/                               CONTEXT: identity and authorisation
    api/                                Authenticator, Decider, Principals, ProjectsAPI
    domain/
      user/ group/ token/ permission/ role/ keypair/   aggregates ← pkg/types user, permission, roles, token, keys
      project/                          Project, Environment aggregates (P11) ← pkg/types project.go
      oidc/                             OIDCIntegration aggregate ← internal/service/oidc
      idp/                              IdentityProviderSetting aggregate (P21: SAML, human OIDC, Crowd, HTTP-SSO)
      session/                          BrowserSession, Approval aggregates (P21)
      scim/                             ProvisionedIdentity aggregate (P22)
      ldap/                             LDAPSetting, GroupSync aggregates (P12)
      decision/                         permission evaluation as a domain service ← internal/authz
    application/
      authenticate/                     credential checks, suspension, deferred auth ← internal/auth
      tokens/                           issue, refresh, revoke ← internal/auth/token
      identity/                         users, groups, permissions APIs ← internal/service/identity, permission
      roles/                            ← internal/service/roles
      oidcexchange/                     ← internal/auth/idtoken, internal/service/oidc
      keys/                             key pair checks ← internal/keymaterial
      reindexauth/                      ← internal/reindexauth
      projects/ ldap/ login/ scim/      (P11, P12, P21, P22)
    adapters/
      http/                             ← internal/server access*.go auth.go tokens.go permissions.go roles.go
                                          security.go keypairs.go oidc.go
      middleware/                       request authentication middleware handed to every context's routes
      persistence/{sql,inmem,persistencetest}/ ← sqlcommon identity*.go, inmem identity*.go, storetest identity.go
      ldap/ saml/ oidcprovider/ scim/   (P12, P21, P22)
      idtokentest/                      ← internal/auth/idtoken/idtokentest

  configuration/                        CONTEXT: system configuration and descriptors
    api/
    domain/
      system/                           SystemConfiguration aggregate (baseUrl, webServer, mail, proxies,
                                        property sets, layouts definitions, gc and trash config)
      backup/                           BackupSchedule aggregate (P12, P36)
    application/
      descriptor/                       YAML apply, XML export (P12), XML POST (P36) ← internal/descriptor
      importexport/                     (P36)
    adapters/http/                      ← internal/server descriptor.go system.go reverseproxy.go
    adapters/persistence/{sql,inmem,persistencetest}/ ← sqlcommon operations_config.go

  notifications/                        CONTEXT (P11): webhooks
    api/ domain/webhook/ (Webhook, Delivery aggregates) application/ adapters/{http,sender,persistence}/
                                        ← sqlcommon operations_webhooks.go (tables exist today)
  retention/                            CONTEXT (P11, P16): cleanup and archive policies
    api/ domain/{cleanup,archive}/ (CleanupPolicy, CleanupRun, ArchivePolicy, ArchiveRun, RestoreJob)
    application/ adapters/{http,persistence}/   ← sqlcommon operations_cleanup.go
  replication/                          CONTEXT (P11, P37)
    api/ domain/ (ReplicationConfig, ReplicationRun) application/ adapters/{http,client,persistence}/
  federation/                           CONTEXT (P13, P14)
    api/ domain/{peer,pairing,membership,outbound,topology}/ application/
    adapters/{peer (signed peer API over gen/peer), http (pfed, grid), persistence}/
                                        ← sqlcommon peers.go, pkg/types peer.go federation.go platform.go
  releases/                             CONTEXT (P15, P24)
    api/ domain/{bundle,bundlev2,stage,distribution}/ application/
    adapters/{http (/api/release, /distribution, lifecycle), signing, persistence}/
                                        ← sqlcommon enterprise.go (bundle tables), pkg/types distribution.go
  workers/                              CONTEXT (P17)
    api/ domain/{worker,execution,binding}/ application/ adapters/{http,sandbox,contentgate,persistence}/
  evidence/                             CONTEXT (P23)
    api/ domain/{evidence,signer}/ application/ adapters/{http,verify,persistence}/
  governance/                           CONTEXT (P25)
    api/ domain/{application,policy,evaluation,approval}/ application/ adapters/{http,persistence}/
  security/                             CONTEXT (P26, P27)
    api/ domain/{component,scan,finding,watch,curation,catalog}/ application/
    adapters/{http,graphql (OneModel subgraph host), engine, feeds, persistence}/
  operations/                           CONTEXT (P18, P38)
    api/ domain/{integration,supportbundle,health}/ application/
    adapters/{http,metrics,vault,iam,logship,persistence}/
  estate/                               CONTEXT (P40)
    api/ domain/{member,bridge,registration}/ application/ adapters/{http,persistence}/

pkg/
  client/                               generated clients (unchanged) ← pkg/client
    access/ artifactory/ distribution/ event/ grid/ pfed/ worker/
    peer/                               (P13)
  containertest/                        ← pkg/containertest
```

Today's 65 files in the `internal/server` root, by destination:

| Destination | Files |
| --- | --- |
| `internal/shared/httpkit` | `server.go` `middleware.go` `context.go` `trace.go` `protocols.go` `stubs.go` |
| `internal/content/adapters/http` | `items.go` `handwritten.go` `storage.go` `storageinfo.go` `metadata_stats.go` `multipart.go` `trash.go` `transfer.go` `redirect.go` `repositories.go` |
| `internal/access/adapters/http` | `access.go` `access_identity.go` `auth.go` `tokens.go` `permissions.go` `roles.go` `security.go` `keypairs.go` `oidc.go` |
| `internal/search/adapters/http` | `aql.go` `aql_actions.go` `aql_builds.go` `aql_transitive.go` `aql_transitive_peer.go` `searches.go` `searches_builds.go` `searches_layout.go` `searches_upstream.go` `searches_upstream_maven.go` `searches_upstream_routes.go` `searches_versions.go` |
| `internal/builds/adapters/http` | `builds.go` |
| `internal/configuration/adapters/http` | `descriptor.go` `system.go` `reverseproxy.go` |
| `internal/remote/adapters/http` | `smart_status.go` `source.go` `source_listing.go` |
| `internal/packages/application/reindex` | `reindex.go` |
| `internal/packages/<ecosystem>/adapters/http` | `agentplugins.go` (agentplugins), `alpine.go` (alpine), `cran.go` `cran_upload.go` (cran), `debian.go` `debian_operations.go` `debian_snapshot.go` (debian), `docker_rest.go` (docker), `maven.go` (maven), `npm_auth.go` `npm_operations.go` (npm), `opkg.go` (opkg), `p2.go` `p2_external.go` (p2), `pypi_routes.go` (pypi), `rpm.go` (rpm), `vcs_download.go` `vcs_nested.go` `vcs_routes.go` (vcs) |
| `internal/virtual/application` (P10) | `debian_virtual.go`, as the first per-format merge |

## Deep dives

### npm ecosystem

```text
internal/packages/npm/
  domain/                                         format rules, no I/O
    npm.go            ← format/npm/npm.go         Rules type implementing packages/domain/rules.FormatRules
    metadata.go       ← format/npm/metadata.go    package.json parsing, name and version value objects
    tarball.go        ← format/npm/tarball.go     tarball inspection through shared/archive
    policy.go         ← format/npm/policy.go      overwrite and strict-layout policy
    latest.go         ← format/npm/latest.go      dist-tag latest selection
    npm_test.go metadata_test.go tarball_test.go policy_test.go latest_test.go
    testdata/                                     tarballs, package.json fixtures
  application/                                    commands and derived work
    publish.go        ← server/npm/publish.go     PublishVersion: decode body, call content api Deploy
    disttags.go       ← server/npm/disttags.go    MoveDistTag command (PackageVersion aggregate in packages)
    update.go         ← server/npm/update.go      deprecate, unpublish
    packument.go      ← server/npm/packument.go   BuildPackument query over the packages projection
    search.go         ← format/npm/search.go, server/npm/search.go
    index.go          ← format/npm/index.go       OnItemDeployed handler body for npm
    work.go           ← format/npm/work.go        strict-layout conversion, reindex work kinds
    login.go          ← server npm_auth.go        legacy login; calls access api Tokens.Issue
    operations.go     ← server npm_operations.go  /api/npm/<repo>/reindex and friends
    merge.go          (P10)                       packument merge for virtual
    *_test.go
  adapters/
    http/
      routes.go       ← server/npm/npm.go         pkgroute registration, mounts on /api/npm/{repo}
      body.go         ← server/npm/body.go        request decoding limits
      npmrc.go        ← server/npm/npmrc.go
      user.go         ← server/npm/user.go        /-/user and /-/whoami
      *_test.go       handler tests with fakes of application ports
    upstream/
      remote.go       ← server/npm/remote.go      upstream packument and tarball fetch through remote api
      remote_dist.go  ← server/npm/remote_dist.go
      remote_integrity.go remote_validate.go      ← server/npm/remote_integrity.go, remote_validate.go
      *_test.go
  contract_test.go                                runs packages/adapters/formattest against npm Rules
test/server/packages/npm/                         ← test/server/npm (12 assembly test files)
test/acceptance/features/npm.feature npm_remote.feature npm_search_auth.feature
  npm_overwrites_and_settings.feature             unchanged
test/acceptance/clients/node.Dockerfile node-npm12.Dockerfile   unchanged
```

npm becomes four packages (`domain`, `application`, `adapters/http`, `adapters/upstream`)
where today it is two. The packument is now built from the packages context's own
projection of versions and dist-tags, fed by `PackageVersionIndexed` events, rather than
read inside the deploy transaction.

### Deploy write path

```text
internal/content/
  api/
    content.go         ContentAPI: Deploy, DeployCommitted, Derive, Remove, Move, SetProperties
    gate.go            DownloadGate and DeployGate interfaces implemented by workers, security (P17, P27)
    events.go          published event types (aliases of domain/events.go)
  domain/
    item/
      item.go          Item aggregate: path, checksums, size, properties, created and modified
      folder.go        Folder aggregate
      properties.go    Properties value object (matrix parameters)
      repository.go    ItemRepository interface: Get, Save, Delete, Tree
    blob/
      reference.go     BlobReference aggregate: sha256, refcount, tier
      store.go         Store, Upload, Info, Digests port   ← pkg/blob/blob.go
    trash/entry.go     TrashEntry aggregate                ← service/trash
    upload/session.go  UploadSession aggregate             ← service/multipart
    events.go          ItemDeployed{RepoKey, Path, ItemID, SHA256, PackageType, By, Inspection}
  application/
    deploy/
      deploy.go        ← item/item.go Deploy, Upload, DeployCommitted, DeployByChecksum
      folder.go        ← item/item.go MakeFolder, EnsureRoot
      checksum.go      ← item/item.go RecordChecksum, RecordSidecar, Algorithm
      sidecar.go       ← item/sidecar.go
      cache.go         ← item/cache.go (remote cache writes, called by remote api)
      smart.go         ← item/smart.go (Smart Remote property propagation)
      derive.go        ← item format.go Env.Derive and Underive, now a command other contexts call
    remove/remove.go restore.go   ← item/remove.go restore.go
    transfer/transfer.go work.go  ← item/transfer.go transfer_work.go
    explode/explode.go            ← item/archive.go
    gc/collector.go               ← service/gc
internal/packages/
  api/packages.go      Admit(ctx, RepositoryView, path, Inspection) error   synchronous, read-only
  application/index/
    on_deployed.go     consumes content.ItemDeployed; loads PackageVersion; Index; saves; emits
                       PackageVersionIndexed
    on_deleted.go      consumes ItemDeleted; Unindex; prunes empty versions (pkgindex.Prune today)
    reconcile.go       ← service/pkgindex Reconcile, for repair and replay
  application/registry/                    ← service/registry (manifests, tags, retention)
internal/builds/application/publish.go     ← service/build build.go document.go
internal/shared/
  sqlcommon/unitofwork.go                  ← sqlstore/sqlcommon/unitofwork.go
  outbox/outbox.go                         Append(ctx, evt) joins the ctx transaction
  eventbus/bus.go                          Subscribe, Dispatch, at-least-once with idempotency keys
```

The rule this option imposes: a transaction changes one aggregate. Today a deploy changes
the Item, the package version and its file link, any derived item (maven-metadata.xml,
an npm strict-layout move), and for build-info repositories the build record, all inside
one `store.Tx`. Under option 4 the deploy commits only the Item and its BlobReference
increment (one aggregate plus its owned reference, written through content's own
repository) and appends `ItemDeployed` to the outbox in the same transaction. Everything
else is a consequence:

```go
// internal/content/application/deploy/deploy.go
func (h *Handler) Deploy(ctx context.Context, cmd Deploy) (item.Item, error) {
	info, err := h.upload(ctx, cmd) // blob write, outside any transaction, as today
	if err != nil {
		return item.Item{}, err
	}
	insp, err := h.packages.Inspect(ctx, cmd.Repo, cmd.Path, info) // packages api, read-only
	if err != nil {
		return item.Item{}, err
	}
	if err := h.packages.Admit(ctx, cmd.Repo, cmd.Path, insp); err != nil {
		return item.Item{}, err // a RejectionError still reaches the client synchronously
	}
	var it item.Item
	err = h.uow.Do(ctx, func(ctx context.Context) error {
		var err error
		it, err = item.New(cmd.Repo.Key, cmd.Path, info, cmd.Properties, cmd.By, h.clock.Now())
		if err != nil {
			return err
		}
		if err := h.items.Save(ctx, it); err != nil {
			return err
		}
		return h.outbox.Append(ctx, item.Deployed(it, cmd.Repo.PackageType, insp))
	})
	return it, err
}

// internal/packages/application/index/on_deployed.go
func (h *Indexer) OnItemDeployed(ctx context.Context, e content.ItemDeployed) error {
	rules := h.formats.For(e.PackageType)
	return h.uow.Do(ctx, func(ctx context.Context) error {
		v, err := rules.Index(ctx, e) // returns the PackageVersion change
		if err != nil {
			return err // retried by the bus; parked after N attempts
		}
		if err := h.versions.Save(ctx, v); err != nil {
			return err
		}
		return h.outbox.Append(ctx, version.Indexed(v, e.EventID))
	})
}
```

What becomes eventually consistent, and what a client can observe:

| Change | Today | Under option 4 | Observable effect |
| --- | --- | --- | --- |
| Item row and blob reference | deploy tx | deploy tx | none |
| Package version and file link | deploy tx (`format.Handler.Deployed` then `pkgindex.Link`) | `OnItemDeployed` in packages, later tx | `npm publish` returns 200, an immediate `npm view` or packument GET can miss the version until the handler runs |
| Derived items (maven-metadata.xml, Helm index.yaml, Debian Release) | deploy tx through `format.Env.Derive` | packages handler calls `content.api.Derive` in a third tx | `mvn deploy` then an immediate resolve can read the previous metadata |
| Strict-layout moves (npm), unique snapshot cleanup (Maven) | deploy tx through `Env.Move` and `Env.Remove` | further content commands from packages handlers | a client may briefly see the file at its uploaded path |
| Build record plus build-info document item | one tx in `service/build` | BuildRun saved in builds; `BuildPublished` triggers content to store the document item | `jf rt bp` succeeds, the storage API can briefly lack the build-info JSON; a failed document write needs a compensating `BuildDocumentMissing` repair |
| Docker manifest plus tag in the index | one tx in `service/registry` | manifest Item in content; tag in packages via event | `docker push` then `docker pull :tag` immediately can 404 |
| Rejection of an invalid package | rollback of the deploy tx | synchronous `Admit` before commit | none; validation stays synchronous because it changes no aggregate |
| Index rejection discovered while indexing (for example a duplicate version found only by `Index`) | rollback | the Item is already committed; the handler emits `IndexRejected` and content quarantines or removes the item | a client can get 200 for a deploy that is later undone |

The last row is the hard one: any rule that today fails a deploy from inside a hook must
either move into `Admit` (and be checkable without writing) or become a compensating
action after commit.

### Access and identity

```text
internal/access/
  api/
    authenticator.go    Authenticate(r *http.Request) (principal.Principal, error)
    decider.go          Decider: CanRead, CanDeploy, CanDelete, CanAnnotate, CanManage(repo, path)
    projects.go         (P11) ProjectOf(repoKey), RoleActions
  domain/
    user/user.go group/group.go               aggregates ← pkg/types userconv.go, principal.go
    token/token.go                            Token aggregate (scope, expiry, revocation) ← pkg/types token.go
    permission/target.go                      PermissionTarget aggregate ← pkg/types permission.go permconv.go
    role/role.go                              Role aggregate, built-in environments ← pkg/types roles.go
    keypair/keypair.go                        KeyPair, Certificate aggregates ← pkg/types keys.go
    oidc/integration.go identitymapping.go    ← service/oidc
    decision/evaluator.go pattern.go repopatterns.go   pure evaluation ← internal/authz
    project/ ldap/ idp/ session/ scim/        (P11, P12, P21, P22)
    events.go                                 UserCreated, UserDeactivated, TokenRevoked, PermissionChanged
  application/
    authenticate/authenticator.go password.go current.go current_registry.go deferred.go
                 download.go policy.go suspension.go seed.go     ← internal/auth
    tokens/jwt.go keys.go issue.go                                ← internal/auth/token
    identity/identity.go access.go                                ← service/identity
    permissions/permission.go                                     ← service/permission
    roles/roles.go                                                ← service/roles
    oidcexchange/exchange.go match.go                             ← auth/idtoken, service/oidc
    keys/keymaterial.go                                           ← internal/keymaterial
    reindexauth/reindexauth.go                                    ← internal/reindexauth
    login/ scim/ ldapsync/ projects/                              (P21, P22, P12, P11)
  adapters/
    http/          access.go access_identity.go auth.go tokens.go permissions.go roles.go
                   security.go keypairs.go oidc.go                ← internal/server root
                   scim.go (P22) saml.go login.go (P21)
    middleware/authn.go                                           chi middleware that calls application/authenticate
    persistence/sql/ identity.go identity_keys.go migrations/     ← sqlcommon identity*.go
    persistence/inmem/ persistencetest/                           ← inmem identity*.go, storetest identity.go
    ldap/ saml/ oidcprovider/                                     (P12, P21)
    idtokentest/                                                  ← auth/idtoken/idtokentest
```

How a protocol handler gets authentication and authorisation: `internal/app` mounts
`access/adapters/middleware` on the root router once, so every context's routes see a
`principal.Principal` in the request context. Each context's application layer takes an
`access/api.Decider` in its constructor and asks it per request:

```go
// internal/packages/npm/adapters/http/routes.go
type Routes struct {
	App    *application.Service
	Decide accessapi.Decider
	Auth   accessapi.Authenticator
}
```

The Decider needs repository configuration and project membership. Repository
configuration lives in content; the Decider takes a `kernel/repo.Config` argument rather
than looking repositories up itself, so access does not call content. Project roles
(P11) are inside access.

### Storage and persistence

`pkg/store` disappears. Each context's `domain/` declares a repository interface per
aggregate, and each context's `adapters/persistence/sql` implements it against its own
tables, using the shared dialect and unit of work.

```text
internal/shared/sqlcommon/
  dialect.go           ← sqlcommon/dialect.go     placeholders, upsert, locking per dialect
  migrate.go           ← sqlcommon/migrate.go     now runs one migration set per context, in context order,
                                                  each recorded under its own schema_versions row
  unitofwork.go        ← sqlcommon/unitofwork.go  ctx-carried transaction, AfterCommit
  store.go             ← sqlcommon/store.go       DB handle, close, health (business methods removed)
  reseal.go            ← sqlcommon/reseal.go      sealed column rotation across contexts' tables
internal/shared/{sqlite,postgres}/               ← sqlstore/sqlite, sqlstore/postgres (open, pragmas)
internal/shared/mysql/                           (P34)
internal/shared/sqltest/sqltest.go rotation.go   ← sqlstore/sqltest (remotecache.go and
                                                   repository_headers.go move to their contexts)
internal/shared/outbox/                          outbox table, Append, ordered read for the bus (new)
internal/shared/work/work.go                     ← service/work + pkg/store work.go, sqlcommon work.go

Per context, the same shape. Content as the example:
internal/content/adapters/persistence/
  sql/
    items.go           ← sqlcommon content_items.go     ItemRepository
    content.go         ← sqlcommon content.go           RepoStore, trash, uploads
    blobs.go replicas.go ← sqlcommon blobs.go replicas.go
    repository_headers.go ← sqlcommon repository_headers.go
    migrations/
      sqlite/0001_content.sql 0006_blob_replicas.sql 0007_blob_states.sql
             0014_item_properties_modified.sql 0016_repository_header_secrets.sql
      postgres/ (same set plus 0013_property_values.sql)
      mysql/   (P34)
  inmem/               ← pkg/store/inmem content.go content_items.go blobs.go replicas.go page.go
  persistencetest/     ← pkg/store/storetest content.go core.go replicas.go properties.go
```

| Today's migration | Owning context |
| --- | --- |
| `0001_content.sql`, `0006_blob_replicas.sql`, `0007_blob_states.sql`, `0013_property_values.sql`, `0014_item_properties_modified.sql`, `0016_repository_header_secrets.sql` | content (package tables in 0001 split out to packages) |
| `0002_identity.sql`, `0008_certificate_keys.sql`, `0009_user_lockout.sql`, `0010_token_purpose.sql`, `0011_token_identity.sql` | access |
| `0003_operations.sql` | split: configuration, notifications, retention, replication, shared/work |
| `0004_peers.sql` | federation |
| `0005_enterprise.sql` | split: releases, retention (archive), workers |
| `0012_builds.sql` | builds |
| `0015_remote_cache.sql` | remote |
| `0017_gitlfs_locks.sql` | packages (gitlfslocks) |

| Today's `pkg/store` file | Repository interfaces now in |
| --- | --- |
| `content.go` | `content/domain/{repository,item,blob,trash,upload}`; `PackageStore` part to `packages/domain/version` |
| `identity.go` | `access/domain/{user,group,permission,token,keypair}` |
| `build.go` | `builds/domain/run` |
| `operations.go` | `access/domain/project`, `notifications/domain`, `retention/domain`, `replication/domain`, `configuration/domain`, `shared/work` (LeaseStore) |
| `peers.go` | `federation/domain` |
| `enterprise.go` | `releases/domain`, `retention/domain/archive`, `workers/domain` |
| `remotecache.go`, `remotecache_impl.go` | `remote/domain/cache` |
| `gitlfs_locks.go` | `packages/gitlfslocks` |
| `aqlsource.go`, `layout_sources.go`, `properties.go` | `search/domain/projection` |
| `work.go` | `shared/work` |
| `normalise.go`, `store.go` | `shared/sqlcommon` (Page, cursors, sentinel errors move to `kernel`) |

Blob and CDN stay in content (`content/adapters/blob/*`, `content/adapters/cdn`), with
`blobtest` and `cdntest` beside them. The P16 cold tier is a content blob member; the
retention context decides what moves and asks content to move it.

### Remote and virtual repositories

```text
internal/remote/
  api/remote.go               Fetch(ctx, RepositoryView, path) (FetchResult, error); Listing; Status
  domain/
    cache/entry.go negative.go          CacheEntry aggregate          ← pkg/types remotecache.go
    upstream/circuit.go                 UpstreamHealth aggregate      ← remote/circuit.go
    smart/subscription.go statistics.go ← remote/smart_statistics.go smart_properties.go
    policy/rules.go policy.go           retrieval decision table      ← remote/rules.go policy.go
  application/
    fetch/fetch.go flight.go discovery.go       ← remote/fetch.go flight.go discovery.go
    source/source.go gitlfs_action.go           ← internal/source
    listing/listing.go listing_html.go listing_mapped.go listing_smart.go   ← remote/listing*.go
    smart/smart.go smart_aql.go smart_discovery.go smart_peer.go smart_queue.go
          smart_request.go smart_trace.go smart_status.go                   ← remote/smart*.go
    maintenance/zap.go cleanup.go               ← service/item zap.go cache_cleanup.go
  adapters/
    transport/client.go pool.go destination.go credentials.go security.go url.go protocol.go
                                                ← remote/client.go pool.go destination.go ...
    registryauth/registry_challenge.go registry_token.go   ← remote/registry_challenge.go registry_token.go
    http/smart_status.go source.go source_listing.go        ← server root
    persistence/sql/ inmem/ persistencetest/                ← sqlcommon remotecache.go, storetest remotecache.go
    fakeupstream/proxy.go record.go sync.go tls.go upstream.go   ← internal/fakeupstream
internal/packages/<ecosystem>/adapters/upstream/   format-specific upstream parsing:
    golang/  ← remote/golang.go golang_zip.go module_location.go
    maven/   ← remote/maven.go maven_eager.go
    nuget/   ← remote/nuget_empty_feed.go
    docker/  ← remote/registry.go registry_children.go registry_content.go registry_descriptors.go
               registry_pages.go
internal/virtual/                               (P10)
  api/virtual.go
  domain/resolution/plan.go                     ordered members, defaultDeploymentRepo
  domain/cache/entry.go                         VirtualCacheEntry (virtualRetrievalCachePeriodSecs)
  application/resolve.go merge.go               merge calls packages api Merge(packageType, docs)
  adapters/http/ adapters/persistence/sql/
```

Caching a remote file crosses contexts: remote fetches, then calls `content/api.Cache`
(today `item.Service.Cache`), which commits the cached Item and emits `ItemDeployed`
with a cached flag; packages indexes it later. Today the cached item and its index rows
are one transaction.

### Workers (P17), a roadmap domain end to end

```text
api/src/worker/                       ← api/src/worker (fragments), plus the unpublished v2 paths of P17 task 0
internal/workers/
  api/workers.go                      Execute(ctx, action, payload) for other contexts (rarely used)
  domain/
    worker/worker.go                  Worker aggregate: key, action, source, filter, enabled, limits
    execution/execution.go            Execution aggregate: worker, event, outcome, logs, duration
    binding/binding.go                HookBinding value object: action to filter
    events.go                         WorkerCreated, ExecutionFinished
  application/
    crud.go test.go execute.go rerun.go history.go
    on_item_deployed.go               AFTER_CREATE: consumes content.ItemDeployed, runs worker,
                                      calls content api SetProperties in a new tx
    before_download.go                implements content/api.DownloadGate
  adapters/
    http/workers.go                   implements the worker slice of shared/gen/worker
    sandbox/
      pool.go                         helper process pool, one execution per helper
      host.go                         `worker-host` subcommand body, called from cmd/artifactweave
      transpile.go                    esbuild TypeScript transpile
      runtime.go                      goja runtime with limits: 64 MiB, 1 s CPU, 5 s per call, 20 s total
      httpclient.go                   allow-listed outbound HTTP
    contentgate/gate.go               adapter registering before_download with content
    persistence/sql/ migrations/      ← workers tables from 0005_enterprise.sql
cmd/artifactweave/main.go             adds hidden `worker-host` command that calls workers/adapters/sandbox.Host
```

The hook without an import: `content/api/gate.go` declares

```go
// internal/content/api/gate.go
type DownloadGate interface {
	BeforeDownload(ctx context.Context, d DownloadAttempt) (Verdict, error)
}
```

`content/application/download` holds a slice of `DownloadGate` passed in its
constructor. `internal/app` builds `workers/adapters/contentgate.Gate` and passes it to
content. Content never imports workers; workers imports `content/api`, which rule 1
allows. AFTER events need no gate: workers subscribes to `ItemDeployed` on the bus.

### Webhooks and events (P11, P13)

```text
internal/<context>/domain/events.go        events defined by their producer (content, packages, builds,
                                            access, releases, ...), as Go structs with a stable Name()
internal/<context>/api/events.go           published copies other contexts subscribe to
internal/shared/outbox/
  outbox.go          Append(ctx, evt): inserts into outbox table inside the caller's unit of work
  reader.go          ordered reading by sequence for the bus
internal/shared/eventbus/
  bus.go             Subscribe(name, handler); dispatch loop on shared/work leases
  idempotency.go     processed(event_id, subscriber) table
  deadletter.go      parked events, replay command
internal/notifications/                    (P11)
  domain/webhook/webhook.go criteria.go    Webhook aggregate (event names, criteria, secret)
  domain/delivery/delivery.go              Delivery aggregate (attempts, status)
  application/on_event.go                  one subscriber for every published event; maps to the
                                           JFrog event catalogue names
  application/test.go troubleshoot.go
  adapters/sender/http.go signing.go       signed delivery with retries
  adapters/http/ persistence/sql/          ← sqlcommon operations_webhooks.go
internal/federation/                       (P13)
  domain/outbound/entry.go                 OutboundEntry aggregate per peer, acknowledged sequence
  application/on_event.go                  subscribes to content and packages events for federated repos,
                                           translates to the peer protocol's published language
  application/apply.go                     idempotent apply of inbound events through content and packages api
  adapters/peer/                           implements shared/gen/peer; signed requests
```

Two outboxes exist by design: the shared outbox carries in-process domain events; the
federation context keeps its own outbound log per peer because acknowledgement and
full-sync fallback are federation rules, not bus rules.

### Composition root

```text
internal/app/
  app.go            ← app/app.go           Run, serve, drain
  runtime.go        ← app/runtime.go       opens shared infrastructure, builds contexts
  contexts.go       new                    one constructor call per context
  subscriptions.go  new                    every bus.Subscribe line, in one file
  strict.go         new                    stitches generated strict-server slices (see next section)
  blob.go mirrors.go ← app/blob.go mirrors.go   now build content/adapters/blob members
  remote.go         ← app/remote.go        builds remote context
  gc.go             ← app/gc.go            schedules content gc on shared/work
  descriptor.go     ← app/descriptor.go    bootstrap descriptor through configuration api
  instance.go       ← app/instance.go      instance identity (federation at P13)
  seed.go           ← app/seed.go          built-in principals through access api
  store.go          ← app/store.go         opens shared/sqlcommon and runs every context's migrations
```

```go
// internal/app/contexts.go (shape at P9c)
func buildContexts(rt *runtime) (*contexts, error) {
	acc := access.New(access.Deps{DB: rt.db, UoW: rt.uow, Outbox: rt.outbox, Keys: rt.keys})
	cnt := content.New(content.Deps{DB: rt.db, UoW: rt.uow, Outbox: rt.outbox,
		Blobs: rt.blobs, CDN: rt.cdn, Decide: acc.Decider()})
	pkgs := packages.New(packages.Deps{DB: rt.db, UoW: rt.uow, Outbox: rt.outbox,
		Content: cnt.API(), Decide: acc.Decider(), Work: rt.work})
	cnt.SetPackages(pkgs.API()) // content's deploy needs Inspect and Admit: a cycle broken by a setter
	bld := builds.New(builds.Deps{DB: rt.db, UoW: rt.uow, Outbox: rt.outbox, Content: cnt.API()})
	rem := remote.New(remote.Deps{DB: rt.db, UoW: rt.uow, Content: cnt.API(), Pool: rt.remoteHTTP})
	srch := search.New(search.Deps{DB: rt.db, Content: cnt.API(), Builds: bld.API(), Remote: rem.API()})
	cfg := configuration.New(configuration.Deps{DB: rt.db, UoW: rt.uow, Content: cnt.API(), Access: acc.API()})

	rt.bus.Subscribe(content.ItemDeployedName, pkgs.OnItemDeployed)
	rt.bus.Subscribe(content.ItemDeletedName, pkgs.OnItemDeleted)
	rt.bus.Subscribe(content.ItemDeployedName, srch.ProjectItem)
	rt.bus.Subscribe(builds.BuildPublishedName, cnt.StoreBuildDocument)
	rt.bus.Subscribe(builds.BuildPublishedName, srch.ProjectBuild)
	return &contexts{acc, cnt, pkgs, bld, rem, srch, cfg}, nil
}
```

At P41 the same file holds nineteen constructor calls and roughly seventy `Subscribe`
lines; `subscriptions.go` becomes the readable map of who reacts to what. The cycle
between content (needs `Admit`) and packages (needs `Deploy`, `Derive`) is real and
recurs between content and workers, content and security, releases and evidence.

### Generated code and API specifications

```text
api/
  src/
    artifactory/
      content/      paths-storage.yaml paths-trash.yaml paths-repositories.yaml paths-archive.yaml
      packages/     paths-packagetypes.yaml paths-cargo.yaml paths-composer.yaml
      search/       paths-searches.yaml
      builds/       paths-builds.yaml
      configuration/ paths-system.yaml paths-admin.yaml
      access/       paths-security.yaml
      replication/  paths-replication.yaml           (P11)
      federation/   paths-federation.yaml            (P13)
      releases/     paths-releasebundles.yaml        (P15)
      retention/    paths-cleanup.yaml               (P11)
      components-common.yaml info.yaml
    access/ distribution/ event/ grid/ pfed/ worker/ (each split by context where it spans more than one)
    peer/                                             (P13, artifactweave's own peer API)
  artifactory.openapi.yaml ... worker.openapi.yaml    bundled, unchanged location
  oapi-server-<spec>.yaml   output: internal/shared/gen/<spec>/<spec>.gen.go
  oapi-client-<spec>.yaml   output: pkg/client/<spec>/<spec>.gen.go
internal/shared/gen/<spec>/                           generated strict servers
internal/shared/tooling/{openapibundle,stubgen,specoracle}/
```

Which package implements each generated strict-server interface:

| Spec | Implemented by |
| --- | --- |
| artifactory | stitched in `internal/app/strict.go` from content, packages, search, builds, configuration, remote, retention, replication, federation and releases `adapters/http` handler structs |
| access | access `adapters/http` (plus federation for Access federation at P14) |
| event | notifications `adapters/http` (P11) |
| distribution | releases `adapters/http` (P15) |
| grid, pfed | federation `adapters/http` (P14) |
| worker | workers `adapters/http` (P17) |
| peer | federation `adapters/peer` (P13) |

```go
// internal/app/strict.go
type artifactoryStrict struct {
	*contenthttp.Strict
	*packageshttp.Strict
	*searchhttp.Strict
	*buildshttp.Strict
	*configurationhttp.Strict
	*remotehttp.Strict
	unimplemented // stubgen's 501 type, one level deeper so it never makes a method ambiguous
}

type unimplemented struct{ artifactory.Unimplemented }

var _ artifactory.StrictServerInterface = artifactoryStrict{}
```

Each context's `Strict` struct implements only its own operations. Go's embedding
resolves each method to the one context that defines it; two contexts defining the same
operation is a compile error (ambiguous selector), which is the check that keeps
ownership single.

### Tests and enforcement

```text
internal/<context>/domain/...       unit tests beside the code, no I/O
internal/<context>/application/...  unit tests with in-memory repositories from adapters/persistence/inmem
internal/<context>/adapters/persistence/persistencetest/   contract suite per context, run by inmem,
                                                           sqlite, postgres (and mysql at P34)
internal/content/adapters/blob/blobtest/                    blob contract
internal/content/adapters/cdn/cdntest/                      CDN contract
internal/packages/adapters/formattest/                      format contract every ecosystem runs
internal/shared/eventbus/bustest/                           ordering, redelivery, idempotency contract (new)
test/server/<context>/<subject>/                            server assembly suites
  packages/{npm,maven,pypi,nuget,helm,helmoci,docker,golang,gems,terraform,terraformbackend,
            agentpackages,agentplugins}/                    ← test/server/<protocol>
  shared/ support/ registryfixture/ agentcommon/            ← test/server/shared support registryfixture agentcommon
test/acceptance/ test/platform/ test/terraform/ test/oci/   unchanged
internal/app/layout_test.go                                 the dependency rules (replaces internal/layout)
```

```go
// internal/app/layout_test.go (shape of the rule check)
var contexts = []string{"content", "packages", "builds", "search", "remote", "virtual", "access",
	"configuration", "notifications", "retention", "replication", "federation", "releases",
	"workers", "evidence", "governance", "security", "operations", "estate"}

// allowed reports whether a file in dir may import imp, both relative to the module root.
func allowed(dir, imp string) bool {
	switch {
	case strings.HasPrefix(dir, "pkg/"):
		return !strings.HasPrefix(imp, "internal/") && !strings.HasPrefix(imp, "cmd/")
	case strings.HasPrefix(dir, "internal/kernel/"):
		return !strings.HasPrefix(imp, "internal/") || strings.HasPrefix(imp, "internal/kernel/")
	case strings.HasPrefix(dir, "internal/shared/"):
		return contextOf(imp) == ""
	}
	own := contextOf(dir)
	if own == "" || !strings.HasPrefix(imp, "internal/") {
		return true
	}
	if strings.Contains(dir, "/domain") && (strings.HasPrefix(imp, "internal/shared/") ||
		strings.Contains(imp, "/api") || strings.Contains(imp, "/application") ||
		strings.Contains(imp, "/adapters")) {
		return false
	}
	if strings.Contains(dir, "/application") && strings.HasPrefix(imp, "internal/"+own+"/adapters") {
		return false
	}
	other := contextOf(imp)
	return other == "" || other == own || imp == "internal/"+other+"/api"
}
```

Coverage: roughly 260 packages at P9c against 174 today, every one under the 90% floor.
Packages likely to struggle are the thin ones the uniform layering creates: small
ecosystems' `application/` packages (bower, aieditorextensions, gitlfs, generic with a
handful of lines each), every context's `api/` package (interfaces and type aliases,
which coverage tools count as zero statements but some files hold small adapters), and
the small contexts' four-folder skeletons (estate, notifications) in their first phase.

## Walkthroughs

### An npm publish request

1. `internal/shared/httpkit/server.go`: chi router; `access/adapters/middleware/authn.go`
   authenticates and puts the Principal in the context.
2. `internal/packages/npm/adapters/http/routes.go`: `PUT /api/npm/{repo}/{package}` decoded by
   `body.go`.
3. `internal/packages/npm/application/publish.go`: asks `access/api.Decider.CanDeploy`, reads
   the repository through `content/api.Repository(key)`, splits the publish body into the
   tarball and the version document.
4. `internal/content/api` `Deploy` call into `internal/content/application/deploy/deploy.go`:
   streams the tarball to `content/adapters/blob/<backend>` (no transaction).
5. Deploy calls `packages/api.Inspect` then `Admit`, which run
   `packages/npm/domain/tarball.go` and `policy.go`; a rejection returns 400 here.
6. Deploy opens `shared/sqlcommon` unit of work, saves the Item through
   `content/adapters/persistence/sql/items.go`, appends `ItemDeployed` through
   `shared/outbox`, commits.
7. `publish.go` returns 201 to the npm client.
8. Later: `shared/eventbus` delivers `ItemDeployed` to
   `packages/application/index/on_deployed.go`, which runs `packages/npm/application/index.go`,
   saves the PackageVersion and dist-tags in a new transaction, appends
   `PackageVersionIndexed`.
9. Later: `search/application` projects the item; notifications (P11) and federation
   (P13) receive `ItemDeployed` or `PackageVersionIndexed` for their own handlers.

### A BEFORE_DOWNLOAD worker blocks a download (P17)

1. `internal/packages/npm/adapters/http/routes.go`: tarball GET.
2. `internal/packages/npm/application/...` asks `access/api.Decider.CanRead`, then calls
   `content/api.Open`.
3. `internal/content/application/download/download.go`: iterates its `[]api.DownloadGate`.
4. `internal/workers/adapters/contentgate/gate.go` implements the gate and calls
   `workers/application/before_download.go`.
5. `before_download.go` loads matching Worker aggregates from
   `workers/adapters/persistence/sql`, runs one in `workers/adapters/sandbox/pool.go`.
6. `sandbox/host.go` in a helper process transpiles with `transpile.go` and runs `runtime.go`
   under the limits; the verdict returns over the helper pipe.
7. `before_download.go` saves an Execution aggregate in its own transaction and returns
   `Verdict{Allow: false, Status: 403, Message: ...}`.
8. `download.go` maps the verdict to `apierror` 403; `routes.go` writes it.

### Adding a new package type

For a hypothetical `zigpkg` with local and remote classes:

1. `internal/kernel/repo/packagetypes.go`, `displayname.go`, `validate.go`: add the type and
   its classes.
2. `internal/packages/zigpkg/domain/zigpkg.go`, `metadata.go`, `*_test.go`, `testdata/`:
   format rules implementing `packages/domain/rules.FormatRules`.
3. `internal/packages/zigpkg/application/index.go`, `publish.go`, `*_test.go`.
4. `internal/packages/zigpkg/adapters/http/routes.go`, `*_test.go`.
5. `internal/packages/zigpkg/adapters/upstream/remote.go`, `*_test.go`: remote parsing and
   metadata rewriting.
6. `internal/packages/zigpkg/contract_test.go`: run `formattest`.
7. `internal/packages/application/formats/formats.go`: register the rules.
8. `internal/app/contexts.go`: mount the routes (or `packages.New` registers them itself).
9. `api/src/artifactory/packages/paths-packagetypes.yaml` if a management endpoint exists.
10. `test/server/packages/zigpkg/*_test.go`, `test/acceptance/features/zigpkg.feature`,
    `zigpkg_steps_test.go`, `test/acceptance/clients/zig.Dockerfile`.
11. `docs/implementation/package-format-inventory.json` and the generated matrix;
    `tools/citestselection/test_ownership.json`.

Five new Go packages, about eighteen files.

### Adding a new business domain: Evidence (P23)

1. `api/src/artifactory/evidence/` (or a new `api/src/evidence/` spec), bundle, new
   `oapi-server-evidence.yaml` and `oapi-client-evidence.yaml` if a separate service.
2. `internal/shared/gen/evidence/`, `pkg/client/evidence/` regenerated.
3. `internal/evidence/api/evidence.go`: published queries (ForSubject) and events.
4. `internal/evidence/domain/evidence/evidence.go`, `subject.go`, `payload.go`,
   `repository.go`, `events.go`, tests.
5. `internal/evidence/domain/signer/signer.go`, `repository.go`, tests.
6. `internal/evidence/application/upload.go`, `verify.go`, `query.go`, `on_item_deleted.go`,
   `on_release_promoted.go`, tests.
7. `internal/evidence/adapters/http/evidence.go` (strict slice), tests.
8. `internal/evidence/adapters/verify/dsse.go`, tests.
9. `internal/evidence/adapters/persistence/sql/evidence.go`, `migrations/{sqlite,postgres,mysql}/
   0001_evidence.sql`; `inmem/`; `persistencetest/`.
10. `internal/evidence/evidence.go`: `New(Deps)` constructor.
11. `internal/app/contexts.go`, `subscriptions.go`, `strict.go`: construct, subscribe to
    content, builds and releases events, add the strict slice.
12. `internal/app/layout_test.go`: add `evidence` to the context list.
13. `test/server/evidence/`, `test/acceptance/features/evidence.feature` and steps.

About twelve new packages and thirty files; edits outside `internal/evidence` are limited
to `internal/app`, the spec and the tests.

## Package mapping

| Today's package | New location | Notes |
| --- | --- | --- |
| `api` | `api` | spec fragments regrouped by context |
| `cmd/artifactweave` | `cmd/artifactweave` | adds `worker-host` at P17 |
| `internal/app` | `internal/app` | gains contexts.go, subscriptions.go, strict.go |
| `internal/archive` | `internal/shared/archive` | |
| `internal/auth` | `internal/access/application/authenticate` | |
| `internal/auth/idtoken` | `internal/access/application/oidcexchange` | |
| `internal/auth/idtoken/idtokentest` | `internal/access/adapters/idtokentest` | |
| `internal/auth/token` | `internal/access/application/tokens` | |
| `internal/authz` | `internal/access/domain/decision` | pure evaluation; Decider published from `access/api` |
| `internal/buildinfo` | `internal/shared/buildinfo` | |
| `internal/config` | `internal/shared/config` | process configuration, not the configuration context |
| `internal/crypt` | `internal/shared/crypt` | |
| `internal/descriptor` | `internal/configuration/application/descriptor` | applies through other contexts' api |
| `internal/fakeupstream` | `internal/remote/adapters/fakeupstream` | |
| `internal/format` (root: format.go sidecar.go snapshots.go) | `internal/packages/domain/rules` | Handler hooks split into Admit (sync) and Index (event) |
| `internal/format/<ecosystem>` (33 packages) | `internal/packages/<ecosystem>/domain` and `application` | rules to domain, index and work to application |
| `internal/format/formats` | `internal/packages/application/formats` | |
| `internal/format/formattest` | `internal/packages/adapters/formattest` | now drives events instead of hooks |
| `internal/format/generic` | `internal/packages/generic/domain` | deploy policy every type embeds |
| `internal/format/docker` | `internal/packages/docker/domain` | also serves helmoci |
| `internal/format/terraform`, `terraform/terraformtest` | `internal/packages/terraform/domain`, `.../domain/terraformtest` | |
| `internal/format/terraformbackend` | `internal/packages/terraformbackend/domain` | |
| `internal/format/gitlfs` | `internal/packages/gitlfs/domain` | |
| `internal/keymaterial` | `internal/access/application/keys` | |
| `internal/layout` | `internal/app/layout_test.go` | rules rewritten per context |
| `internal/openapibundle` | `internal/shared/tooling/openapibundle` | |
| `internal/reindexauth` | `internal/access/application/reindexauth` | |
| `internal/remote` | `internal/remote/{domain,application,adapters}` plus `internal/packages/{golang,maven,nuget,docker}/adapters/upstream` | 40 files split by concern |
| `internal/repos` | `internal/content/application/repositories` | Repository aggregate in content |
| `internal/server` (root, 65 files) | see the root-file table | |
| `internal/server/<protocol>` (25 packages) | `internal/packages/<ecosystem>/adapters/http` | protocol commands move to `application` |
| `internal/server/apierror` | `internal/shared/httpkit/apierror` | |
| `internal/server/baseurl` | `internal/shared/httpkit/baseurl` | |
| `internal/server/gen/*` (7 packages) | `internal/shared/gen/*` | |
| `internal/server/pkgroute` | `internal/shared/httpkit/pkgroute` | |
| `internal/server/docker` | `internal/packages/docker/adapters/http` | registry protocol, also helmoci |
| `internal/server/golang` | `internal/packages/golang/adapters/http` | |
| `internal/server/vcs` | `internal/packages/vcs/adapters/http` | joined by the root vcs_*.go files |
| `internal/server/nimmodel` | `internal/packages/nim/adapters/http` | renamed to its package type |
| `internal/service/build` | `internal/builds/application` | BuildRun aggregate in `builds/domain/run` |
| `internal/service/gc` | `internal/content/application/gc` | |
| `internal/service/identity` | `internal/access/application/identity` | |
| `internal/service/item` | `internal/content/application/{deploy,remove,transfer,explode}`, `internal/remote/application/maintenance` | zap and cache cleanup to remote |
| `internal/service/multipart` | `internal/content/application/deploy` plus `content/domain/upload` | |
| `internal/service/oidc` | `internal/access/domain/oidc`, `application/oidcexchange` | |
| `internal/service/permission` | `internal/access/application/permissions` | |
| `internal/service/pkgindex` | `internal/packages/domain/version`, `application/index` | |
| `internal/service/registry` | `internal/packages/application/registry` | RegistryUpload aggregate |
| `internal/service/roles` | `internal/access/application/roles` | |
| `internal/service/terraformstate` | `internal/packages/terraformstate` | |
| `internal/service/trash` | `internal/content/domain/trash` and `application/remove` | |
| `internal/service/usage` | `internal/content/application/usage` | |
| `internal/service/work` | `internal/shared/work` | |
| `internal/source` | `internal/remote/application/source` | |
| `internal/specoracle` | `internal/shared/tooling/specoracle` | |
| `internal/sqlstore/postgres` | `internal/shared/postgres` | migrations split to contexts |
| `internal/sqlstore/sqlcommon` | `internal/shared/sqlcommon` plus each context's `adapters/persistence/sql` | 24 files split by table owner |
| `internal/sqlstore/sqlite` | `internal/shared/sqlite` | migrations split to contexts |
| `internal/sqlstore/sqltest` | `internal/shared/sqltest` | remotecache.go to remote, repository_headers.go to content |
| `internal/stubgen` | `internal/shared/tooling/stubgen` | |
| `internal/testfault` | `internal/shared/testfault` | |
| `pkg/aql` | `internal/search/domain/aql`, `internal/search/adapters/sql` | no longer public |
| `pkg/aql/aqltest` | `internal/search/adapters/aqltest` | |
| `pkg/blob` | `internal/content/domain/blob` | port |
| `pkg/blob/{azure,fs,gcs,mirror,s3,session}` | `internal/content/adapters/blob/*` | no longer public |
| `pkg/blob/blobtest` | `internal/content/adapters/blob/blobtest` | |
| `pkg/cdn`, `pkg/cdn/cdntest` | `internal/content/adapters/cdn`, `.../cdntest` | |
| `pkg/client/*` (7 packages) | `pkg/client/*` | unchanged |
| `pkg/containertest` | `pkg/containertest` | unchanged |
| `pkg/layout` | `internal/kernel/layout` | |
| `pkg/repo` | `internal/kernel/repo` | value object; aggregate behaviour in content |
| `pkg/store` | each context's `domain/` repository interfaces | see the storage table |
| `pkg/store/inmem` | each context's `adapters/persistence/inmem` | |
| `pkg/store/storetest` | each context's `adapters/persistence/persistencetest` | |
| `pkg/types` | split: `internal/kernel/{ids,principal,version}` and each context's `domain/` | |
| `pkg/types/wire` | `internal/kernel/wire` | |
| `test/acceptance` | `test/acceptance` | unchanged |
| `test/platform` | `test/platform` | unchanged |
| `test/server/<protocol>` (13 packages) | `test/server/packages/<protocol>` | |
| `test/server/{agentcommon,registryfixture,shared,support}` | `test/server/{agentcommon,registryfixture,shared,support}` | support gains per-context builders |
| `tools/*` (9 commands) | `tools/*` | path keys updated |

## Numbers

Estimates, stated as such.

| Measure | Value |
| --- | --- |
| Packages at P9c | about 260 (today 174): ecosystems alone about 170 with four folders each where they have a remote class |
| Packages at P41 | about 360 |
| Import rules | 8 |
| Interfaces a new ecosystem implements | 3 to 5: FormatRules (Inspect, Admit, Index, Unindex), HTTP route registration, optional upstream parser, optional virtual Merger (P10), optional work handlers |
| Files touched to add an ecosystem | about 18, five new packages |
| Files touched to add a domain | about 30, twelve new packages, edits outside the context limited to `internal/app`, the spec and tests |
| Bus subscriptions at P41 | about 70 |
| Largest hand-written package | `internal/content/adapters/http`, about 8k lines (items, storage, trash, transfer from today's 24k-line server root) |
| Largest package overall | `internal/shared/gen/artifactory`, about 63k generated lines |

## Placement choices and friction

Placement choices settled in this file:

- **Repository configuration is a shared-kernel value object.** `kernel/repo` holds the
  configuration shape every format hook reads; the Repository aggregate that owns its
  lifecycle is in content. Without this, every ecosystem would call content's api for its
  own repository settings on every request.
- **Blob and CDN belong to content.** Only content stores bytes. Retention's cold tier
  and releases' bundle storage go through `content/api`.
- **Format-specific upstream parsing belongs to packages, generic retrieval to remote.**
  `internal/remote`'s golang, maven, nuget and registry files move to the ecosystem's
  `adapters/upstream`; transport, policy, listing and Smart Remote stay in remote.
- **Terraform state stays in packages** beside terraformbackend, rather than becoming a
  twentieth context, because its only client surface is the backend protocol.
- **Validation is synchronous, indexing is asynchronous.** `format.Handler` splits into
  `Admit` (no writes, before commit) and `Index` (event handler). This is the only way to
  keep rejecting bad packages with the client's request under one aggregate per
  transaction.
- **Search owns projections.** AQL reads search's own read model, fed by content, packages
  and builds events, instead of joining other contexts' tables.

Friction, stated plainly:

- **The deploy write is split across three transactions.**
  `content/application/deploy/deploy.go`, `packages/application/index/on_deployed.go` and
  the `content/api.Derive` calls from packages handlers. Native clients that publish then
  immediately resolve (npm, Maven, Docker, Helm, NuGet acceptance scenarios) can see stale
  metadata. The acceptance suite would need waits or read-your-writes tokens.
- **Hook-time rejections become compensations.** Any rule that today fails inside
  `format.Handler.Deployed` must move into `Admit` or become `IndexRejected` handling in
  content, which can undo a write the client was told succeeded.
- **AQL consistency changes.** `search/adapters/sql` projections lag the item tables, so
  `items.find` immediately after a deploy may miss the item; today it never does.
  `items.delete` and `properties.update` actions dispatch commands to content and cannot
  be one transaction across many items.
- **Context cycles need setters.** content needs packages (`Admit`), packages needs content
  (`Deploy`, `Derive`); `internal/app/contexts.go` breaks it with `SetPackages`. The same
  shape recurs with workers and security gates.
- **The artifactory strict server spans ten contexts.** `internal/app/strict.go` stitches
  slices by embedding; adding an operation means knowing which context's `Strict` it belongs
  to, and the spec fragments are split to match.
- **Migrations split by context.** Today's `0001_content.sql` holds content and package
  tables; under option 4 each context has its own numbered set and the migrator orders
  contexts. Foreign keys across contexts (version_files to items) are dropped or kept as
  plain IDs.
- **Uniform layering creates thin packages.** Small ecosystems and small contexts get
  four folders each, about 90 packages under 300 lines, each held to the 90% floor.
- **The store contract suite fragments.** Today's single `storetest` (62 subtests on four
  backends) becomes nineteen `persistencetest` suites; a behaviour that spans contexts
  (deploy updates item, index and build) has no single suite and is proven only by
  `test/server` assembly tests with the bus running.
- **Event infrastructure is new platform work.** `shared/outbox`, `shared/eventbus`
  ordering, idempotency, dead letters and replay are required before P10, because the
  deploy path itself depends on them, not only the P11 webhooks.
