# Option 1: Layered, detailed layout

This file is the detailed layout behind Option 1 in
[ARCHITECTURE-OPTIONS.md](../ARCHITECTURE-OPTIONS.md). It follows the same
section order as the other four option files, so two can be read side by side.

Conventions in the trees:

- `← internal/service/item` names where the code lives today.
- `(P17)` marks a package or file the roadmap adds, with its phase.
- A line with neither is new structure for code that exists today.

## Summary

Three horizontal layers over a shared model. **Transport** turns a protocol
(HTTP management API, 49 native package protocols, CLI, peer protocol,
scheduler, worker host) into calls on the application layer. **Application**
holds every business operation and owns transaction boundaries. **Persistence**
holds storage and every external system the application reaches: SQL stores,
blob stores, CDNs, upstream HTTP, keyring, archives, identity providers, vaults
and scanners. Beneath all three, `model` holds the shared types and `support`
holds pure libraries with no layer of their own (AQL, layouts, version
comparison). Packages are grouped by technical role first and by subject
second. Interfaces for storage and external systems are declared in
persistence beside their implementations, and application packages call each
other directly.

Dependency rules:

1. `internal/model/...` and `internal/support/...` import nothing under
   `internal/transport`, `internal/application`, `internal/persistence` or
   `internal/app`.
2. `internal/persistence/...` never imports `internal/application/...` or
   `internal/transport/...`.
3. `internal/application/...` never imports `internal/transport/...`.
4. `internal/transport/...` never imports `internal/persistence/...` except
   the interface root packages `internal/persistence/store` and
   `internal/persistence/blob` (handlers stream content and read through the
   store's read API).
5. `internal/application/format/<ecosystem>` never imports another
   `internal/application/format/<ecosystem>` (formats are independent).
6. `pkg/...` never imports `internal/...` or `cmd/...`.
7. Only `internal/app` and `cmd/...` import `internal/app`.

Rules 1 to 3 and 6 are what the pattern itself says. Rules 4, 5 and 7 are
added because the layered pattern does not stop them by shape; without them
the tree drifts. There is no rule between application packages; that is the
pattern's main gap and is discussed under friction.

## Repository root

```text
artifactweave/
├── cmd/
│   └── artifactweave/            unchanged: cobra entry point; serve, migrate, config, keys, version,
│                                 and the hidden worker-host subcommand (P17)
├── internal/                     CHANGED: regrouped into model, support, transport, application,
│                                 persistence, app, tooling, archtest
├── pkg/                          CHANGED: shrinks to generated clients and containertest; the store,
│                                 blob, cdn, aql, layout, repo and types packages move to internal/
├── api/                          unchanged: OpenAPI fragments, bundles and oapi-codegen configs;
│                                 output paths in the configs change (see Generated code)
├── test/                         unchanged in shape: acceptance, server, oci, platform, terraform
├── tools/                        unchanged: thin mains over internal/tooling
├── docs/                         repo-layout.md rewritten to describe the layers
├── deploy/                       unchanged
├── Makefile                      generate targets point at the new gen paths
├── .golangci.yml                 depguard rules mirror the dependency rules above
├── .testcoverage.yml             exclusions updated for transport/http/gen and pkg/client
├── go.mod                        unchanged module path
└── ARCHITECTURE-OPTIONS.md, architecture-options/
```

`pkg/` shrinks because a layered design has no public contract tier: storage
interfaces and their implementations are one layer, and a consumer outside
the repository is not part of the design. The generated clients stay public
because tests and the planned own client import them.

## Full package tree

```text
internal/
├── app/                                   composition root ← internal/app
├── config/                                strict YAML, flag > env > file ← internal/config
├── buildinfo/                             ← internal/buildinfo
├── archtest/                              dependency rules as a test ← internal/layout
├── tooling/                               packages behind tools/
│   ├── openapibundle/                     ← internal/openapibundle
│   ├── stubgen/                           ← internal/stubgen
│   └── specoracle/                        ← internal/specoracle
│
├── model/                                 SHARED TYPES for every layer ← pkg/types
│   ├── repo/                              repository configuration model ← pkg/repo
│   ├── wire/                              JFrog JSON shapes ← pkg/types/wire
│   ├── project.go webhook.go …            already present today in pkg/types
│   ├── policy.go application.go           (P25)
│   ├── evidence.go                        (P23)
│   ├── finding.go sbom.go                 (P26)
│   └── release.go lifecycle.go            (P15, P24)
│
├── support/                               PURE LIBRARIES, no layer
│   ├── aql/                               parser, AST, evaluator, SQL compiler ← pkg/aql
│   │   └── aqltest/                       shared AQL cases ← pkg/aql/aqltest
│   ├── layout/                            repository layout patterns ← pkg/layout
│   └── testfault/                         failing fakes for handler tests ← internal/testfault
│
├── transport/                             LAYER 1: everything that speaks a protocol
│   ├── http/
│   │   ├── (root files)                   router, middleware, auth middleware, dispatch
│   │   │                                  ← internal/server/{server,middleware,context,auth,
│   │   │                                    handwritten,protocols,stubs}.go
│   │   ├── apierror/                      ← internal/server/apierror
│   │   ├── baseurl/                       ← internal/server/baseurl
│   │   ├── gen/                           generated strict servers
│   │   │   ├── access/ artifactory/ distribution/ event/ grid/ pfed/ worker/
│   │   │   │                              ← internal/server/gen/<spec>
│   │   │   └── peer/                      (P13)
│   │   ├── management/                    REST families split out of today's server root
│   │   │   ├── storage/                   items, storage, storageinfo, trash, transfer, multipart,
│   │   │   │                              redirect, metadata_stats, source, source_listing
│   │   │   ├── search/                    aql*, searches*
│   │   │   ├── builds/                    builds
│   │   │   ├── security/                  security, keypairs, permissions, tokens, access,
│   │   │   │                              access_identity, roles, oidc
│   │   │   ├── repositories/              repositories, descriptor, reindex
│   │   │   ├── system/                    system, trace, reverseproxy
│   │   │   ├── remote/                    smart_status
│   │   │   ├── projects/                  (P11)
│   │   │   ├── webhooks/                  (P11)
│   │   │   ├── cleanup/ replication/      (P11)
│   │   │   ├── configuration/ ldap/       (P12)
│   │   │   ├── federation/ topology/      (P13, P14)
│   │   │   ├── releases/ distribution/    (P15, P24)
│   │   │   ├── archive/                   (P16)
│   │   │   ├── workers/                   (P17)
│   │   │   ├── metrics/ health/           (P18)
│   │   │   ├── sessions/ saml/ sso/       (P21)
│   │   │   ├── evidence/                  (P23)
│   │   │   ├── apptrust/ policy/          (P25)
│   │   │   ├── xray/ catalog/             (P26)
│   │   │   ├── curation/                  (P27)
│   │   │   ├── backup/ importexport/      (P36)
│   │   │   ├── operations/ support/       (P38)
│   │   │   └── estate/                    (P40)
│   │   ├── protocol/                      49 native protocols, one package per family
│   │   │   ├── pkgroute/                  router kit for native protocols ← internal/server/pkgroute
│   │   │   ├── agentpackages/             ← internal/server/agentpackages
│   │   │   ├── agentplugins/              ← internal/server/agentplugins.go
│   │   │   ├── aieditorextensions/        ← internal/server/aieditorextensions
│   │   │   ├── alpine/                    ← internal/server/alpine.go
│   │   │   ├── ansible/                   (P9c)
│   │   │   ├── bazelmodules/              ← internal/server/bazelmodules
│   │   │   ├── bower/                     ← internal/server/bower
│   │   │   ├── cargo/                     ← internal/server/cargo
│   │   │   ├── chef/                      (P9c)
│   │   │   ├── cocoapods/                 ← internal/server/cocoapods
│   │   │   ├── composer/                  ← internal/server/composer
│   │   │   ├── conan/                     ← internal/server/conan
│   │   │   ├── conda/                     ← internal/server/conda
│   │   │   ├── cran/                      ← internal/server/cran.go, cran_upload.go
│   │   │   ├── debian/                    ← internal/server/debian*.go
│   │   │   ├── docker/                    docker, oci and helmoci ← internal/server/docker,
│   │   │   │                              docker_rest.go
│   │   │   ├── gems/                      ← internal/server/gems
│   │   │   ├── gitlfs/                    ← internal/server/gitlfs
│   │   │   ├── golang/                    go ← internal/server/golang
│   │   │   ├── helm/                      ← internal/server/helm
│   │   │   ├── hex/                       ← internal/server/hex
│   │   │   ├── huggingface/               huggingfaceml ← internal/server/huggingface
│   │   │   ├── jetbrainsplugins/          ← internal/server/jetbrainsplugins
│   │   │   ├── luarocks/                  ← internal/server/luarocks
│   │   │   ├── machinelearning/           (P9c)
│   │   │   ├── maven/                     maven, gradle, ivy, sbt ← internal/server/maven.go
│   │   │   ├── nimmodel/                  ← internal/server/nimmodel
│   │   │   ├── nix/                       (P9c)
│   │   │   ├── npm/                       ← internal/server/npm, npm_auth.go, npm_operations.go
│   │   │   ├── nuget/                     ← internal/server/nuget
│   │   │   ├── opkg/                      ← internal/server/opkg.go
│   │   │   ├── p2/                        ← internal/server/p2.go, p2_external.go
│   │   │   ├── pub/                       ← internal/server/pub
│   │   │   ├── puppet/                    (P9c)
│   │   │   ├── pypi/                      ← internal/server/pypi, pypi_routes.go
│   │   │   ├── releasebundles/            (P15)
│   │   │   ├── rpm/                       ← internal/server/rpm.go
│   │   │   ├── skills/                    ← internal/server/skills
│   │   │   ├── swift/                     ← internal/server/swift
│   │   │   ├── terraform/                 ← internal/server/terraform
│   │   │   ├── terraformbackend/          ← internal/server/terraformbackend
│   │   │   ├── vagrant/                   (P9c)
│   │   │   └── vcs/                       ← internal/server/vcs, vcs_*.go
│   │   │                                  generic has no package: the storage family serves it
│   │   ├── scim/                          (P22)
│   │   ├── graphql/                       OneModel gateway (P26)
│   │   └── web/                           console and browser sessions (P39)
│   ├── cli/                               migrate, config, keys command bodies ← cmd/artifactweave
│   │                                      (today inline in main.go)
│   ├── peer/                              peer protocol server and client (P13)
│   ├── scheduler/                         cron-style triggers for cleanup, archive, replication (P11)
│   └── workerhost/                        helper-process side of the Workers sandbox (P17)
│
├── application/                           LAYER 2: business operations, transaction boundaries
│   ├── item/                              deploy, checksum deploy, delete, restore, transfer, cache,
│   │                                      zap, explode ← internal/service/item
│   ├── trash/                             ← internal/service/trash
│   ├── gc/                                ← internal/service/gc
│   ├── multipart/                         ← internal/service/multipart
│   ├── pkgindex/                          ← internal/service/pkgindex
│   ├── registry/                          Docker upload sessions and manifests ← internal/service/registry
│   ├── build/                             ← internal/service/build
│   ├── terraformstate/                    ← internal/service/terraformstate
│   ├── usage/                             ← internal/service/usage
│   ├── work/                              queue worker pool ← internal/service/work
│   ├── repos/                             ← internal/repos
│   ├── descriptor/                        ← internal/descriptor
│   ├── source/                            authorized read boundary ← internal/source
│   ├── remote/                            retrieval policy, Smart Remote, per-format upstream
│   │                                      adapters ← internal/remote (policy half)
│   ├── virtual/                           ordered resolution and metadata merging (P10)
│   ├── format/                            format contract ← internal/format
│   │   ├── formats/                       ← internal/format/formats
│   │   ├── formattest/                    ← internal/format/formattest
│   │   ├── generic/                       ← internal/format/generic
│   │   ├── agentpackages/ agentplugins/ aieditorextensions/ alpine/ bazelmodules/ bower/ cargo/
│   │   │   cocoapods/ composer/ conan/ conda/ cran/ debian/ docker/ gems/ gitlfs/ golang/
│   │   │   helm/ hex/ huggingface/ luarocks/ maven/ npm/ nuget/ opkg/ p2/ pub/ pypi/ rpm/
│   │   │   skills/ swift/ terraform/ terraformbackend/
│   │   │                                  ← internal/format/<same name>
│   │   ├── terraform/terraformtest/       ← internal/format/terraform/terraformtest
│   │   ├── ansible/ chef/ machinelearning/ nix/ puppet/ vagrant/   (P9c)
│   │   └── releasebundles/                (P15)
│   ├── auth/                              authenticator, principals, password, seed, suspension
│   │   │                                  ← internal/auth
│   │   ├── token/                         ← internal/auth/token
│   │   ├── idtoken/                       ← internal/auth/idtoken
│   │   │   └── idtokentest/               ← internal/auth/idtoken/idtokentest
│   │   ├── session/                       browser sessions, CSRF, MFA (P21)
│   │   ├── saml/ sso/ crowd/              (P21)
│   │   └── approval/                      npm web login, Terraform login.v1 (P21)
│   ├── authz/                             the permission Decider ← internal/authz
│   ├── identity/                          users and groups ← internal/service/identity
│   ├── permission/                        ← internal/service/permission
│   ├── roles/                             ← internal/service/roles
│   ├── oidc/                              ← internal/service/oidc
│   ├── keymaterial/                       ← internal/keymaterial
│   ├── reindexauth/                       ← internal/reindexauth
│   ├── project/                           (P11)
│   ├── webhook/                           (P11)
│   ├── cleanup/                           package cleanup policies (P11)
│   ├── replication/                       (P11, P37)
│   ├── ldap/                              (P12)
│   ├── sysconfig/                         XML descriptor, system configuration (P12)
│   ├── federation/                        peers, outbox apply, federated repositories (P13)
│   ├── topology/                          pfed, grid (P14)
│   ├── release/                           release bundles v1 and v2 (P15, P24)
│   ├── distribution/                      (P15)
│   ├── archive/                           archive policies and restore jobs (P16)
│   ├── workers/                           worker CRUD, executions, hook dispatch (P17)
│   ├── scim/                              (P22)
│   ├── evidence/                          (P23)
│   ├── lifecycle/                         stages, promotion (P24)
│   ├── apptrust/                          (P25)
│   ├── policy/                            Unified Policy, approvals (P25)
│   ├── scan/ catalog/ sbom/               (P26)
│   ├── curation/                          (P27)
│   ├── backup/ migration/                 (P36)
│   ├── operations/                        support bundles, queue administration (P38)
│   └── estate/                            (P40)
│
└── persistence/                           LAYER 3: storage and external systems
    ├── store/                             store interfaces, Page, sentinel errors ← pkg/store
    │   ├── inmem/                         ← pkg/store/inmem
    │   ├── storetest/                     ← pkg/store/storetest
    │   ├── sqlcommon/                     ← internal/sqlstore/sqlcommon
    │   ├── sqlite/                        ← internal/sqlstore/sqlite
    │   ├── postgres/                      ← internal/sqlstore/postgres
    │   ├── mysql/                         (P34)
    │   └── sqltest/                       ← internal/sqlstore/sqltest
    ├── blob/                              blob.Store interface ← pkg/blob
    │   ├── fs/ s3/ gcs/ azure/            ← pkg/blob/<same>
    │   ├── mirror/                        ← pkg/blob/mirror
    │   ├── session/                       ← pkg/blob/session
    │   └── blobtest/                      ← pkg/blob/blobtest
    ├── cdn/                               ← pkg/cdn
    │   └── cdntest/                       ← pkg/cdn/cdntest
    ├── upstream/                          remote HTTP transport, pools, credentials, registry token
    │   │                                  auth, circuit breaker ← internal/remote (transport half)
    │   └── fakeupstream/                  ← internal/fakeupstream
    ├── crypt/                             ← internal/crypt
    ├── archive/                           ← internal/archive
    ├── outbox/                            ordered event outbox over the store (P13)
    ├── lease/                             leases and fencing for multi-node (P35)
    ├── ldapdir/                           LDAP client (P12)
    ├── idp/                               SAML and OIDC identity-provider clients (P21)
    ├── sandbox/                           goja runtime and helper-process pool (P17)
    ├── esbuild/                           TypeScript transpile (P17)
    ├── vault/ kms/ iam/ logship/          (P38)
    ├── scanengine/ feeds/                 analysis engines, vulnerability feeds (P26)
    └── vendorreplication/                 cross-vendor replication adapters (P37)

pkg/
├── client/                                generated clients ← pkg/client/<spec>
│   └── access/ artifactory/ distribution/ event/ grid/ pfed/ worker/ peer/ (P13)
└── containertest/                         ← pkg/containertest
```

Where the 65 files in today's `internal/server` root go:

| Destination | Files from today's `internal/server` root |
| --- | --- |
| `transport/http` root | server.go, middleware.go, context.go, auth.go, handwritten.go, protocols.go, stubs.go |
| `management/storage` | items.go, storage.go, storageinfo.go, trash.go, transfer.go, multipart.go, redirect.go, metadata_stats.go, source.go, source_listing.go |
| `management/search` | aql.go, aql_actions.go, aql_builds.go, aql_transitive.go, aql_transitive_peer.go, searches.go, searches_builds.go, searches_layout.go, searches_upstream.go, searches_upstream_maven.go, searches_upstream_routes.go, searches_versions.go |
| `management/builds` | builds.go |
| `management/security` | security.go, keypairs.go, permissions.go, tokens.go, access.go, access_identity.go, roles.go, oidc.go |
| `management/repositories` | repositories.go, descriptor.go, reindex.go |
| `management/system` | system.go, trace.go, reverseproxy.go |
| `management/remote` | smart_status.go |
| `protocol/agentplugins` | agentplugins.go |
| `protocol/alpine` | alpine.go |
| `protocol/cran` | cran.go, cran_upload.go |
| `protocol/debian` | debian.go, debian_operations.go, debian_snapshot.go, debian_virtual.go |
| `protocol/docker` | docker_rest.go |
| `protocol/maven` | maven.go |
| `protocol/npm` | npm_auth.go, npm_operations.go |
| `protocol/opkg` | opkg.go |
| `protocol/p2` | p2.go, p2_external.go |
| `protocol/pypi` | pypi_routes.go |
| `protocol/rpm` | rpm.go |
| `protocol/vcs` | vcs_download.go, vcs_nested.go, vcs_routes.go |

That is 7 + 10 + 12 + 1 + 8 + 3 + 3 + 1 + 20 = 65.

## Deep dives

### npm ecosystem

```text
internal/application/format/npm/           ← internal/format/npm
├── npm.go                                 Handler: Deployed, Deleting, Deleted; Inspector; Admitter
├── metadata.go                            package.json parsing, normalised version records
├── tarball.go                             bounded tarball read through persistence/archive
├── index.go                               pkgindex.Link and Reconcile for npm versions
├── latest.go                              dist-tag latest selection
├── policy.go                              overwrite and strict-layout policy
├── search.go                              search index rows
├── work.go                                strict-layout conversion as queued work (format.Worker)
├── npm_test.go metadata_test.go …         unit tests
└── formattest_test.go                     runs application/format/formattest with the npm fixture

internal/transport/http/protocol/npm/      ← internal/server/npm plus two root files
├── npm.go                                 route table on pkgroute; mounts under /api/npm/{repo}
├── packument.go                           full and abbreviated packuments
├── publish.go                             PUT publish: decode, call application/item.Deploy
├── body.go                                publish body decoding and limits
├── disttags.go                            dist-tag get, put, delete
├── update.go                              deprecate, unpublish
├── search.go                              /-/v1/search
├── user.go                                legacy login and logout
├── npmrc.go                               api/npm/auth .npmrc rendering
├── auth.go                                ← internal/server/npm_auth.go
├── operations.go                          reindex and strict-layout endpoints
│                                          ← internal/server/npm_operations.go
├── remote.go                              remote packument and tarball reads via application/source
├── remote_dist.go                         dist URL rewriting per caller
├── remote_integrity.go                    integrity verification of cached tarballs
├── remote_validate.go                     upstream metadata validation
└── *_test.go                              handler tests with support/testfault fakes

internal/application/remote/               npm upstream adapter has no package of its own today;
                                           the npm remote logic lives in protocol/npm/remote*.go and
                                           uses application/source plus persistence/upstream

test/server/npm/                           server-assembly suites, unchanged
├── fixture_test.go matrix_fixture_test.go matrix_types_test.go
├── matrix_npm_test.go matrix_npm_auth_test.go matrix_npm_lifecycle_test.go
├── npm_auth_test.go npm_operations_test.go
└── status_fixture_test.go status_npm_test.go status_npm_auth_test.go status_npm_lifecycle_test.go
test/server/support/npm.go npm_routes.go   shared fixtures

test/acceptance/features/
├── npm.feature npm_remote.feature npm_search_auth.feature npm_overwrites_and_settings.feature
test/acceptance/clients/
└── node.Dockerfile node-npm12.Dockerfile
```

An npm change touches two trees in the internal layers: `application/format/npm`
and `transport/http/protocol/npm`. The remote half sits with the protocol
because it rewrites client-facing URLs, which is a transport concern, but it
also applies cache policy, which is an application concern. That split is a
judgement call this layout settles by keeping the files together in transport.

### Deploy write path

```text
internal/application/item/                 ← internal/service/item
├── item.go                                Service{Store, Blobs, Now, Formats, Work, Periodic,
│                                          ReindexAuthorize}; Deploy, Upload, DeployCommitted,
│                                          DeployByChecksum, MakeFolder, EnsureRoot, Delete, Purge, Tree
├── format.go                              builds format.Env; Annotate, Resolve, Reindex, ReindexPath
├── sidecar.go                             checksum and sidecar files
├── remove.go                              delete into trash, prune folders
├── restore.go                             trash restore through format hooks
├── transfer.go transfer_work.go           copy and move, planned then executed
├── cache.go cache_cleanup.go              remote cache publication and unused cleanup
├── smart.go                               Smart Remote property state
├── zap.go                                 metadata-only zap
├── archive.go                             explode, archive entry and file listing
└── work.go                                RegisterWork, Enqueuer

internal/application/pkgindex/             ← internal/service/pkgindex
├── pkgindex.go                            Link, VersionOf, Prune, Stored
└── reconcile.go                           Sync, Reconcile, RecordDownload

internal/application/format/               ← internal/format
├── format.go                              Handler, Env, Inspector, Admitter, WriteGuard, Worker,
│                                          Reindexer, RejectionError
├── sidecar.go snapshots.go

internal/application/build/                ← internal/service/build
internal/application/registry/             ← internal/service/registry
internal/application/trash/  gc/  multipart/

internal/persistence/store/
├── store.go                               Store interface with Tx(ctx, fn) and AfterCommit
└── sqlcommon/unitofwork.go                context-carried transaction, runTx, afterCommit

internal/persistence/blob/blob.go          blob.Store, upload sessions
```

The deploy flow stays as it is today. Every participant is an application
package, so the item service calls them directly and the transaction is the
store's context-carried unit of work.

```go
// internal/application/item/item.go (shape, not the full function)
func (s *Service) Deploy(ctx context.Context, d Deploy) (model.Item, error) {
	h := s.formats(d.Repo.PackageType)
	info, err := s.Upload(ctx, d) // content into persistence/blob outside the transaction
	if err != nil {
		return model.Item{}, err
	}
	inspection, err := inspect(ctx, h, s.env(), d, info) // format.Inspector, no locks held
	if err != nil {
		return model.Item{}, err
	}
	var it model.Item
	err = s.Store.Tx(ctx, func(ctx context.Context) error {
		env := s.env()
		env.Inspection = inspection
		w, err := admit(ctx, h, env, d) // format.Admitter, format.WriteGuard
		if err != nil {
			return err
		}
		if it, err = s.Store.Items().Put(ctx, w.Item(info)); err != nil {
			return err
		}
		// Hooks run inside the transaction: the format indexes through
		// pkgindex.Link and asks for derived work through env.Enqueue.
		if err := h.Deployed(ctx, env, d.Repo, it); err != nil {
			return err
		}
		// Roadmap: the item service calls the other domains directly.
		if err := s.Webhooks.Record(ctx, webhook.ItemDeployed(it)); err != nil { // (P11)
			return err
		}
		return s.Workers.After(ctx, workers.AfterCreate, it) // (P17)
	})
	return it, err
}
```

The last two calls are the layered answer to events and hooks: the item
service holds a field per interested domain and calls each one. By P27 the
item service also calls curation and policy for admission. That list is the
main cost of this option.

### Access and identity

```text
internal/application/auth/                 ← internal/auth
├── authenticator.go                       Authenticator, NewAuthenticator, Options
├── principal.go                           Principal and its context key
├── password.go policy.go suspension.go    passwords, lockout policy
├── token.go current.go current_registry.go deferred.go download.go
├── seed.go                                built-in principals
├── token/                                 ← internal/auth/token (jwt.go keys.go token.go)
├── idtoken/                               OIDC token exchange ← internal/auth/idtoken
│   └── idtokentest/
├── session/                               (P21) browser sessions, CSRF, expiry
├── saml/ sso/ crowd/                      (P21)
├── mfa/                                   (P21)
└── approval/                              (P21) native-client browser approval with PKCE

internal/application/authz/                ← internal/authz
├── authz.go                               Decider, New(ctx, Sources, Subject), Subject, Sources, Repo
├── pattern.go repopatterns.go

internal/application/identity/             users, groups ← internal/service/identity
internal/application/permission/           ← internal/service/permission
internal/application/roles/                ← internal/service/roles
internal/application/oidc/                 workload OIDC providers ← internal/service/oidc
internal/application/project/              (P11) projects, membership, environments, quotas
internal/application/ldap/                 (P12) bind, group sync; client in persistence/ldapdir
internal/application/scim/                 (P22) provisioning, reconciliation, deprovisioning
internal/application/keymaterial/          ← internal/keymaterial
internal/application/reindexauth/          ← internal/reindexauth
internal/persistence/crypt/                keyring, sealed columns ← internal/crypt
internal/persistence/idp/                  (P21) SAML and OIDC provider clients

internal/transport/http/auth.go            middleware: Authenticator → Principal on the context
internal/transport/http/management/security/   REST for users, groups, tokens, permissions, roles
internal/transport/http/scim/              (P22)
```

A protocol handler gets the principal from the request context, set by the
middleware in `transport/http/auth.go`, and builds a Decider per request with
`authz.New`. Both are application packages, so the handler imports them
directly. Every protocol package therefore imports `application/auth` and
`application/authz`.

### Storage and persistence

```text
internal/persistence/store/                ← pkg/store
├── store.go                               Store with Repos, Items, Blobs, Replicas, Uploads, Trash,
│                                          Packages, GitLFSLocks, Users, Groups, Permissions, Tokens,
│                                          Keys, Projects, Webhooks, Cleanup, Replications, Settings,
│                                          Leases, WorkQueue, Configs, Instance, Peers, Outbox,
│                                          Federation, Platform, Bundles, Distribution, Archive,
│                                          Workers, Builds; Tx; AfterCommit; Close
├── content.go                             RepoStore, ItemStore, BlobIndex, ReplicaIndex, UploadStore,
│                                          TrashStore
├── identity.go                            UserStore, GroupStore, PermissionStore, TokenStore, KeyStore
├── operations.go                          ProjectStore, WebhookStore, CleanupStore, ReplicationStore,
│                                          SettingsStore, LeaseStore, ConfigStore
├── peers.go                               InstanceStore, PeerStore, OutboxStore, FederationStore,
│                                          PlatformStore
├── enterprise.go                          BundleStore, DistributionStore, ArchiveStore, WorkerStore
├── build.go                               BuildStore
├── remotecache.go remotecache_impl.go     RemoteCacheStore
├── gitlfs_locks.go properties.go work.go aqlsource.go layout_sources.go normalise.go
├── evidence.go                            (P23) EvidenceStore
├── governance.go                          (P25) ApplicationStore, PolicyStore
├── security.go                            (P26) ScanStore, FindingStore
├── inmem/                                 reference store, 17 files ← pkg/store/inmem
├── storetest/                             contract suite, 15 files ← pkg/store/storetest
├── sqlcommon/                             ← internal/sqlstore/sqlcommon
│   ├── dialect.go migrate.go unitofwork.go store.go reseal.go
│   ├── content.go content_items.go content_index.go blobs.go replicas.go repository_headers.go
│   ├── identity.go identity_keys.go
│   ├── operations.go operations_cleanup.go operations_config.go operations_webhooks.go
│   ├── peers.go enterprise.go builds.go build_search.go remotecache.go gitlfs_locks.go work.go
│   └── evidence.go governance.go security.go   (P23, P25, P26)
├── sqlite/
│   ├── sqlite.go
│   └── migrations/0001_content.sql … 0017_gitlfs_locks.sql
├── postgres/
│   ├── postgres.go
│   └── migrations/0001_content.sql … 0017_gitlfs_locks.sql
├── mysql/                                 (P34)
│   ├── mysql.go
│   └── migrations/
└── sqltest/                               shared SQL harness ← internal/sqlstore/sqltest

internal/persistence/blob/                 ← pkg/blob
├── blob.go                                Store, Info, upload sessions, ranged reads, presign
├── fs/ s3/ gcs/ azure/                    backends
├── mirror/                                mirror.go read.go repair.go status.go upload.go
├── session/                               upload sessions in the uploads table
├── cold/                                  (P16) cold tier wrapper
└── blobtest/                              contract suite

internal/persistence/cdn/                  CloudFront, Cloud CDN, Front Door signers ← pkg/cdn
└── cdntest/
```

Interfaces live with their implementations in persistence. One `Store`
interface aggregates every domain's store, one migration sequence per dialect
holds every domain's tables, and one contract suite tests them all. Each new
domain adds a store file, an sqlcommon file, migrations in each dialect and a
storetest file.

### Remote and virtual repositories

```text
internal/application/remote/               ← internal/remote (policy and protocol half)
├── fetch.go policy.go rules.go discovery.go protocol.go
├── listing.go listing_html.go listing_mapped.go listing_smart.go
├── maven.go maven_eager.go                Maven upstream rules
├── golang.go golang_zip.go module_location.go
├── nuget_empty_feed.go
├── registry.go registry_children.go registry_content.go registry_descriptors.go registry_pages.go
├── smart.go smart_aql.go smart_discovery.go smart_peer.go smart_properties.go smart_queue.go
│   smart_request.go smart_statistics.go smart_status.go smart_trace.go
└── testdata/

internal/persistence/upstream/             ← internal/remote (transport half)
├── client.go pool.go credentials.go destination.go url.go security.go
├── circuit.go flight.go                   circuit breaker, singleflight
├── registry_challenge.go registry_token.go   Bearer challenge and token exchange
└── fakeupstream/                          ← internal/fakeupstream

internal/application/source/               authorized read boundary ← internal/source
├── source.go gitlfs_action.go

internal/application/virtual/              (P10)
├── resolve.go                             ordered member resolution, defaultDeploymentRepo
├── merge.go                               per-format metadata merging interface
└── cache.go                               virtualRetrievalCachePeriodSecs
```

Per-format virtual merging (P10) lands in each `application/format/<ecosystem>`
as a `Merger` implementation, called from `application/virtual`. Today's
`internal/remote` splits along transport and policy lines; 11 of its 40 files
move to persistence.

### Workers (P17), a roadmap domain end to end

```text
api/src/worker/                            Workers v2 fragments (P17 task 0 adds the v2 paths)
internal/transport/http/gen/worker/        generated strict server
internal/transport/http/management/workers/
├── workers.go                             implements gen/worker: CRUD, test, execute, rerun
└── executions.go                          execution history
internal/transport/workerhost/             helper-process main loop for the hidden subcommand
├── host.go                                reads a job from stdin, runs it, writes the result
└── limits.go                              64 MiB, 1 s CPU, 5 s per call, 20 s overall
internal/application/workers/
├── workers.go                             worker CRUD and validation
├── actions.go                             action catalogue: BEFORE_DOWNLOAD, AFTER_CREATE, …
├── dispatch.go                            Before(ctx, action, item) error; After(ctx, action, item)
├── executions.go                          execution records and rerun
└── outbound.go                            platform HTTP client with allow-list
internal/persistence/sandbox/
├── pool.go                                helper-process pool, one execution per helper
└── goja.go                                runtime bindings exposed to the script
internal/persistence/esbuild/              TypeScript to JavaScript
internal/persistence/store/enterprise.go   WorkerStore (exists today)
cmd/artifactweave/main.go                  registers the hidden worker-host subcommand
```

The core offers the hook by importing the workers package. The item service
and the download handler hold an `application/workers` dispatcher and call
`Before` and `After`. There is no inversion: the artifact core depends on
Workers, and later on curation and policy in the same way.

### Webhooks and events (P11, P13)

```text
internal/model/webhook.go                  event names and payload types (exists today in pkg/types)
internal/application/webhook/              (P11)
├── catalogue.go                           the exact JFrog event names and criteria
├── record.go                              Record(ctx, event): called by producers inside their Tx
├── deliver.go                             signing, retries, delivery log, test, troubleshooting
internal/application/federation/           (P13)
├── outbox.go                              Append(ctx, event) inside the producer's Tx
├── apply.go                               idempotent apply of a peer's events
internal/persistence/outbox/               (P13) ordered outbox table access
internal/transport/peer/                   (P13) sends and acknowledges outbox events
```

The pattern has no event mechanism of its own. Each producer calls each
consumer it knows about: the item service calls `webhook.Record`,
`federation.Append` and `workers.After`; the build service and the release
service do the same. Two consumers (webhooks and federation) each keep a
record of the same events in different shapes unless a shared outbox is
agreed outside the pattern. This layout puts the outbox in persistence and
lets webhooks read from it too, but nothing in the structure requires that.

### Composition root

```text
internal/app/                              ← internal/app
├── app.go                                 Run, serve, drain
├── runtime.go                             openRuntime: everything the server needs
├── store.go                               open the SQL store and migrate
├── blob.go mirrors.go                     blob stores and mirror administration
├── remote.go                              remote pools and sources
├── gc.go                                  garbage collector schedule
├── descriptor.go seed.go instance.go
├── workers.go                             (P17) sandbox pool lifecycle
├── peers.go                               (P13) peer transport lifecycle
└── *_test.go                              20 test files today
```

```go
// internal/app/runtime.go at P9c (shape)
func openRuntime(ctx context.Context, cfg config.Config, log *slog.Logger) (*runtime, error) {
	st, err := openStore(ctx, cfg.Database) // persistence/store/{sqlite,postgres}
	if err != nil {
		return nil, err
	}
	blobs, cold, err := openBlobs(ctx, cfg.Blob) // persistence/blob/...
	if err != nil {
		return nil, err
	}
	q, _ := work.New(st, work.Options{})
	items := &item.Service{Store: st, Blobs: blobs, Formats: formats.For, Work: q}
	return &runtime{
		store: st, blobs: blobs, coldBlobs: cold, work: q, items: items,
		auth:  auth.NewAuthenticator(st, auth.Options{}),
		gc:    gc.New(st, blobs),
	}, nil
}

// at P41 the same function also builds about forty application services and
// hands each the siblings it calls: item needs webhook, federation, workers,
// curation and policy; release needs item, evidence and policy; and so on.
// server.Config (today about thirty fields) grows a field per service.
```

### Generated code and API specifications

```text
api/
├── src/<service>/*.yaml                   fragments: access artifactory distribution event grid pfed worker
│   └── peer/                              (P13)
├── <service>.openapi.yaml                 bundled specs (make generate)
├── oapi-server-<service>.yaml             output: internal/transport/http/gen/<service>/
└── oapi-client-<service>.yaml             output: pkg/client/<service>/

internal/transport/http/gen/<service>/     generated strict servers, *.gen.go
pkg/client/<service>/                      generated clients
internal/tooling/stubgen/                  501 stubs for unimplemented operations
internal/tooling/specoracle/               JFrog definitions and parity comparison
internal/tooling/openapibundle/            fragment bundler
```

| Generated strict server | Implemented by |
| --- | --- |
| gen/artifactory | `transport/http` root dispatcher, delegating to the management families |
| gen/access | `management/security` |
| gen/event | `management/webhooks` (P11) |
| gen/distribution | `management/distribution` (P15) |
| gen/pfed, gen/grid | `management/topology` (P14) |
| gen/worker | `management/workers` (P17) |
| gen/peer | `transport/peer` (P13) |

The artifactory strict server interface has one method per operation across
the whole spec, so its implementation is one type spread over several
management packages. This layout keeps a root type in `transport/http` that
embeds per-family handler structs, each defined in its family package.

### Tests and enforcement

```text
<package>/*_test.go                        unit (TestUnit…) and API tests (TestAcc…) beside the code
internal/persistence/store/storetest/      store contract suite, run by inmem, sqlite, postgres, mysql
internal/persistence/blob/blobtest/        blob contract suite, run by every backend
internal/persistence/cdn/cdntest/
internal/application/format/formattest/    format contract suite, run by every format
internal/support/aql/aqltest/              shared AQL cases, run by the evaluator and every store
test/server/<family>/                      server-assembly suites, unchanged
test/acceptance/                           godog features, steps and client images, unchanged
test/oci/ test/platform/ test/terraform/   unchanged
internal/archtest/                         dependency rules
```

```go
// internal/archtest/archtest_test.go
var forbidden = map[string][]string{
	"internal/model/":       {mod + "/internal/transport", mod + "/internal/application", mod + "/internal/persistence", mod + "/internal/app"},
	"internal/support/":     {mod + "/internal/transport", mod + "/internal/application", mod + "/internal/persistence", mod + "/internal/app"},
	"internal/persistence/": {mod + "/internal/application", mod + "/internal/transport", mod + "/internal/app"},
	"internal/application/": {mod + "/internal/transport", mod + "/internal/app"},
	"pkg/":                  {mod + "/internal/", mod + "/cmd/"},
}

// transport may reach persistence only through the two interface roots.
var allowedFromTransport = []string{
	mod + "/internal/persistence/store",
	mod + "/internal/persistence/blob",
}
```

Rule 5 (formats do not import each other) is a separate walk that matches
`internal/application/format/<a>` importing `internal/application/format/<b>`
for any `a != b`.

Coverage: package count rises from 174 to about 180 at P9c (the server root
splits into 7 management packages) and to about 260 at P41. Packages likely
to struggle with a 90% floor are the thin management families (`builds`,
`remote`, `system`), which hold little beyond strict-server glue, and the
roadmap adapters in persistence that wrap external services (`vault`, `kms`,
`iam`), whose error paths need fakes.

## Walkthroughs

### An npm publish request

1. `internal/transport/http/server.go`: chi router matches `/api/npm/{repo}/{pkg}` and hands
   it to the protocol mount.
2. `internal/transport/http/middleware.go`: request id, base URL, limits, panic recovery.
3. `internal/transport/http/auth.go`: `application/auth.Authenticator` verifies the token and
   puts the `Principal` on the context.
4. `internal/transport/http/protocol/pkgroute/pkgroute.go`: resolves the repository and checks
   its package type.
5. `internal/transport/http/protocol/npm/publish.go`: decodes the body (`body.go`), builds an
   `authz.Decider` with `authz.New` and checks deploy rights.
6. `internal/application/item/item.go`: `Deploy` streams the tarball to `persistence/blob`
   through `Upload`.
7. `internal/application/format/npm/npm.go`: `Inspect` reads package.json through
   `persistence/archive`.
8. `internal/persistence/store/sqlcommon/unitofwork.go`: `Store.Tx` opens the transaction.
9. `internal/application/format/npm/policy.go`: `Admit` applies overwrite and layout policy.
10. `internal/persistence/store/sqlcommon/content_items.go`: the item row is written.
11. `internal/application/format/npm/index.go`: `Deployed` calls `application/pkgindex.Link`.
12. `internal/application/webhook/record.go` and `internal/application/federation/outbox.go`:
    the item service records the event for each consumer (P11, P13).
13. `internal/persistence/store/sqlcommon/unitofwork.go`: commit; `AfterCommit` wakes the
    work queue.
14. `internal/transport/http/protocol/npm/publish.go`: writes the npm success body.

### A BEFORE_DOWNLOAD worker blocks a download (P17)

1. `internal/transport/http/management/storage/items.go`: GET on an item path.
2. `internal/transport/http/auth.go`: principal on the context.
3. `internal/transport/http/management/storage/items.go`: `authz.Decider` checks read.
4. `internal/application/item/item.go`: resolves the item through `format.Resolver`.
5. `internal/application/workers/dispatch.go`: the handler calls `Before(ctx, BEFORE_DOWNLOAD,
   item)`.
6. `internal/application/workers/workers.go`: looks up enabled workers for the action and
   repository.
7. `internal/persistence/sandbox/pool.go`: takes a healthy helper process.
8. `internal/transport/workerhost/host.go`: the helper runs the script under the limits.
9. `internal/application/workers/dispatch.go`: the script returns a stop status; `Before`
   returns a rejection.
10. `internal/application/workers/executions.go`: records the execution.
11. `internal/transport/http/apierror/apierror.go`: the handler writes the 403 envelope.

### Adding a new package type

For a hypothetical `zigpkg` with local and remote classes:

1. `internal/model/repo/packagetypes.go`: add the package type and its classes.
2. `internal/model/repo/displayname.go`, `typename.go`: display name and type name.
3. `internal/application/format/zigpkg/zigpkg.go`: `Handler`, `Inspector`, `Admitter`.
4. `internal/application/format/zigpkg/metadata.go`, `index.go`: manifest parsing, pkgindex.
5. `internal/application/format/zigpkg/formattest_test.go`: run the contract suite.
6. `internal/application/format/formats/formats.go`: register the format.
7. `internal/transport/http/protocol/zigpkg/zigpkg.go`: route table on pkgroute.
8. `internal/transport/http/protocol/zigpkg/remote.go`: remote reads through `application/source`.
9. `internal/transport/http/protocols.go`: mount the protocol.
10. `test/server/zigpkg/`: server-assembly suite; `test/server/support/zigpkg.go` fixture.
11. `test/acceptance/features/zigpkg.feature`, a steps file and `test/acceptance/clients/zig.Dockerfile`.
12. `docs/implementation/package-format-inventory.json`: add the type.
13. `tools/citestselection/test_ownership.json`: map the new packages.

Two internal trees are touched, plus model, registration and tests. Thirteen
places in all.

### Adding a new business domain: Evidence (P23)

1. `api/src/artifactory/paths-evidence.yaml` (or a new `api/src/evidence/` service): the contract.
2. `api/oapi-server-*.yaml`, `make generate`: regenerate the strict server.
3. `internal/model/evidence.go`: Evidence, Subject, Signer types.
4. `internal/persistence/store/evidence.go`: `EvidenceStore` interface; add `Evidence()` to `Store`.
5. `internal/persistence/store/inmem/evidence.go`: reference implementation.
6. `internal/persistence/store/storetest/evidence.go`: contract cases.
7. `internal/persistence/store/sqlcommon/evidence.go`: SQL implementation.
8. `internal/persistence/store/{sqlite,postgres,mysql}/migrations/00NN_evidence.sql`: tables.
9. `internal/application/evidence/evidence.go`, `verify.go`, `signers.go`: upload, DSSE
   verification, trusted signers.
10. `internal/application/item/item.go`, `transfer.go`, `remove.go`: call evidence on copy,
    promotion and subject deletion.
11. `internal/application/release/release.go`, `internal/application/build/promote.go`: call
    evidence on promotion.
12. `internal/application/webhook/catalogue.go`: evidence events.
13. `internal/transport/http/management/evidence/evidence.go`: handlers.
14. `internal/transport/http/server.go`: mount the family.
15. `internal/app/runtime.go` and `internal/transport/http/server.go` `Config`: wire the service.
16. `test/acceptance/features/evidence.feature`, steps; `test/server/evidence/`.

The domain spreads across model, persistence, application, transport and app,
and edits three existing application services to call it.

## Package mapping

| Today's package | New location | Notes |
| --- | --- | --- |
| api | api | unchanged; parity test stays |
| cmd/artifactweave | cmd/artifactweave | command bodies other than serve move to transport/cli |
| internal/app | internal/app | unchanged role |
| internal/archive | internal/persistence/archive | |
| internal/auth | internal/application/auth | middleware part moves to transport/http/auth.go |
| internal/auth/idtoken | internal/application/auth/idtoken | |
| internal/auth/idtoken/idtokentest | internal/application/auth/idtoken/idtokentest | |
| internal/auth/token | internal/application/auth/token | |
| internal/authz | internal/application/authz | |
| internal/buildinfo | internal/buildinfo | |
| internal/config | internal/config | |
| internal/crypt | internal/persistence/crypt | |
| internal/descriptor | internal/application/descriptor | |
| internal/fakeupstream | internal/persistence/upstream/fakeupstream | |
| internal/format | internal/application/format | contract |
| internal/format/formats | internal/application/format/formats | |
| internal/format/formattest | internal/application/format/formattest | |
| internal/format/generic | internal/application/format/generic | |
| internal/format/docker | internal/application/format/docker | also serves oci and helmoci |
| internal/format/terraform | internal/application/format/terraform | |
| internal/format/terraform/terraformtest | internal/application/format/terraform/terraformtest | |
| internal/format/terraformbackend | internal/application/format/terraformbackend | |
| internal/format/gitlfs | internal/application/format/gitlfs | |
| internal/format/<ecosystem> (other 27) | internal/application/format/<ecosystem> | same name |
| internal/keymaterial | internal/application/keymaterial | |
| internal/layout | internal/archtest | renamed to avoid clashing with support/layout |
| internal/openapibundle | internal/tooling/openapibundle | |
| internal/reindexauth | internal/application/reindexauth | |
| internal/remote | internal/application/remote and internal/persistence/upstream | split: policy and protocol files to application, transport files to persistence |
| internal/repos | internal/application/repos | |
| internal/server (root, 65 files) | internal/transport/http root, management/*, protocol/* | see the server root table |
| internal/server/apierror | internal/transport/http/apierror | |
| internal/server/baseurl | internal/transport/http/baseurl | |
| internal/server/gen/<spec> | internal/transport/http/gen/<spec> | seven packages |
| internal/server/pkgroute | internal/transport/http/protocol/pkgroute | |
| internal/server/docker | internal/transport/http/protocol/docker | plus docker_rest.go |
| internal/server/golang | internal/transport/http/protocol/golang | |
| internal/server/vcs | internal/transport/http/protocol/vcs | plus vcs_*.go |
| internal/server/<protocol> (other 24) | internal/transport/http/protocol/<protocol> | same name |
| internal/service/build | internal/application/build | |
| internal/service/gc | internal/application/gc | |
| internal/service/identity | internal/application/identity | |
| internal/service/item | internal/application/item | |
| internal/service/multipart | internal/application/multipart | |
| internal/service/oidc | internal/application/oidc | |
| internal/service/permission | internal/application/permission | |
| internal/service/pkgindex | internal/application/pkgindex | |
| internal/service/registry | internal/application/registry | |
| internal/service/roles | internal/application/roles | |
| internal/service/terraformstate | internal/application/terraformstate | |
| internal/service/trash | internal/application/trash | |
| internal/service/usage | internal/application/usage | |
| internal/service/work | internal/application/work | |
| internal/source | internal/application/source | |
| internal/specoracle | internal/tooling/specoracle | |
| internal/sqlstore/postgres | internal/persistence/store/postgres | |
| internal/sqlstore/sqlcommon | internal/persistence/store/sqlcommon | |
| internal/sqlstore/sqlite | internal/persistence/store/sqlite | |
| internal/sqlstore/sqltest | internal/persistence/store/sqltest | |
| internal/stubgen | internal/tooling/stubgen | |
| internal/testfault | internal/support/testfault | |
| pkg/aql | internal/support/aql | no longer public |
| pkg/aql/aqltest | internal/support/aql/aqltest | |
| pkg/blob | internal/persistence/blob | no longer public |
| pkg/blob/azure, fs, gcs, s3 | internal/persistence/blob/<same> | |
| pkg/blob/blobtest | internal/persistence/blob/blobtest | |
| pkg/blob/mirror | internal/persistence/blob/mirror | |
| pkg/blob/session | internal/persistence/blob/session | |
| pkg/cdn | internal/persistence/cdn | |
| pkg/cdn/cdntest | internal/persistence/cdn/cdntest | |
| pkg/client/<spec> | pkg/client/<spec> | unchanged, seven packages |
| pkg/containertest | pkg/containertest | unchanged |
| pkg/layout | internal/support/layout | |
| pkg/repo | internal/model/repo | |
| pkg/store | internal/persistence/store | |
| pkg/store/inmem | internal/persistence/store/inmem | |
| pkg/store/storetest | internal/persistence/store/storetest | |
| pkg/types | internal/model | |
| pkg/types/wire | internal/model/wire | |
| test/acceptance | test/acceptance | unchanged |
| test/platform | test/platform | unchanged |
| test/server/<family> and support, shared, agentcommon, registryfixture | test/server/<same> | unchanged; imports updated |
| tools/* | tools/* | unchanged; imports point at internal/tooling |

## Numbers

Estimates are rough and marked as such.

| Measure | Value |
| --- | --- |
| Packages at P9c | about 180 (today 174, plus 7 management families, minus nothing) |
| Packages at P41 (estimate) | about 260 |
| Application packages at P41 (estimate) | about 75, all peers with no rule between them |
| Import rules | 7 |
| Interfaces a new ecosystem implements | 1 required (`format.Handler`), up to 8 optional (`Inspector`, `Admitter`, `WriteGuard`, `Resolver`, `Annotator`, `Reindexer`, `Worker`, P10 `Merger`) |
| Places touched to add an ecosystem | about 13 (two internal trees, model, registration, tests, inventory) |
| Places touched to add a domain | about 16, including edits to 3 existing application services |
| Largest hand-written package | `persistence/store/sqlcommon`, about 10k lines today, plausibly 18k at P41 |
| `server.Config` fields at P41 (estimate) | about 70, one per service the handlers call |

## Placement choices and friction

Placement choices this file settled:

- **Storage interfaces live in persistence, beside implementations.** The
  pattern puts the dependency on the layer below, so the interface goes with
  the layer that owns it. The alternative, interfaces in application, is
  hexagonal by another name.
- **`pkg/` shrinks to clients and containertest.** A layered design has no
  public contract tier. Keeping `pkg/store` and `pkg/blob` public would split
  persistence across two roots.
- **AQL stays whole in `support/aql`, including its SQL compiler.** Splitting
  the compiler into persistence would make sqlcommon the only caller of
  half a package; keeping it whole means support has dialect knowledge.
- **`internal/remote` splits by role.** Transport files (client, pool,
  credentials, circuit, singleflight, registry token) go to
  `persistence/upstream`; policy, listing, Smart Remote and per-format rules
  go to `application/remote`.
- **Per-ecosystem remote handlers stay in the protocol package** (for example
  `protocol/npm/remote*.go`), because they rewrite client-facing URLs.
- **Request authentication is split.** Token verification stays in
  `application/auth`; the middleware that reads headers moves to
  `transport/http/auth.go`.
- **The enforcement test is renamed `archtest`** to free the name `layout`
  for the layout pattern engine.

Where the option rubs against the system:

- **`internal/application` has no internal structure.** At P41 about 75
  packages sit side by side. `application/item` imports format, pkgindex,
  webhook, federation, workers, curation, policy and evidence; nothing says it
  may not import release or apptrust next.
- **The artifact core depends on every domain that hooks into it.**
  `application/item/item.go` and the download path in
  `transport/http/management/storage/items.go` call `application/workers`,
  `application/curation` and `application/policy` directly. A change to the
  Workers API is a change to the item service.
- **Events are producer-driven calls.** `application/item`, `application/build`
  and `application/release` each call `webhook.Record` and `federation.Append`.
  Adding a consumer (security scan triggers in P26) means editing every
  producer.
- **One `Store` interface for every domain.** `persistence/store/store.go`
  already has 31 accessors; P23 to P27 add more. Every domain's fake must
  satisfy or embed the whole interface, and `storetest` grows without bound.
- **`internal/model` is shared by every layer and every domain.** P21 to P27
  run in parallel lanes and all add files to `internal/model`,
  `persistence/store/store.go` and `internal/app/runtime.go`.
- **The protocol and the format of one ecosystem sit in two trees.**
  `application/format/npm` and `transport/http/protocol/npm` must change
  together for most ecosystem work.
- **Transport reaches persistence directly** for streaming content and for
  read-only queries (rule 4's exception). The exception is narrow but it is an
  exception the pattern needs from the start.
- **Many nodes have no structural home.** Leases and fencing
  (`persistence/lease`, P35) are a package every scheduled or queued job must
  remember to use; nothing in the layering makes it unavoidable.
