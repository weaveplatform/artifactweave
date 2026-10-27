# Architecture options for artifactweave

Five candidate architectures for this codebase, each with an indicative folder
layout, how it handles the problems an artifact registry poses today and the
problems the roadmap adds, and its pros and cons.

The options are judged as if the project were being laid out fresh. No
existing decision record, import rule, coverage gate or package boundary is
treated as fixed, and the cost of moving from today's tree is left out of the
evaluation. What is taken into account is the product: what it does now and
what the [implementation plan](docs/implementation/plan.md) commits it to do
through P41. A short note on distance from today sits in the appendix, for
information only.

Options:

1. [Layered](#option-1-layered)
2. [Hexagonal (ports and adapters)](#option-2-hexagonal-ports-and-adapters)
3. [Clean architecture](#option-3-clean-architecture)
4. [Domain-driven design (tactical and strategic)](#option-4-domain-driven-design-tactical-and-strategic)
5. [Modular monolith (bounded contexts, ports and adapters inside)](#option-5-modular-monolith-bounded-contexts-ports-and-adapters-inside)

Then the [comparison matrix](#comparison-matrix), the
[recommendation](#recommendation) and the [appendix](#appendix-distance-from-todays-tree).

### Detailed layouts

Each option has a detailed layout file in [architecture-options/](architecture-options/).
All five use the same sections in the same order, so two can be read side by side:
the full package tree to P41, file-level deep dives (npm, the deploy write path,
access, storage, remote and virtual, Workers, events, the composition root,
generated code, tests), four request and change walkthroughs, a mapping of every
current package, and the numbers.

| Option | Detailed layout |
| --- | --- |
| 1 Layered | [1-layered.md](architecture-options/1-layered.md) |
| 2 Hexagonal | [2-hexagonal.md](architecture-options/2-hexagonal.md) |
| 3 Clean | [3-clean.md](architecture-options/3-clean.md) |
| 4 DDD | [4-ddd.md](architecture-options/4-ddd.md) |
| 5 Modular monolith | [5-modular-monolith.md](architecture-options/5-modular-monolith.md) |

The layout files' estimates, side by side. All figures are rough; each file
explains its own.

| Measure | 1 Layered | 2 Hexagonal | 3 Clean | 4 DDD | 5 Modular |
| --- | --- | --- | --- | --- | --- |
| Packages at P9c | about 180 | about 190 | about 230 | about 260 | about 190 |
| Packages at P41 | about 260 | about 300 | about 400 | about 360 | about 330 |
| Import rules in the layout test | 7 | 8, rising to 9 | 6 plus 2 exception lists | 8 | 5 plus 1 |
| Files touched to add a package type | about 13 | about 20 | about 25 | about 18 | about 18 |
| Files touched to add a domain | about 16, editing 3 existing services | about 25, in every area | about 30, across four rings | about 30 | about 29, 4 of them edits |
| Largest hand-written package at P41 | sqlcommon, about 18k lines | sqlcommon, over 25k lines | sqlcommon, about 25k lines | content http adapter, about 8k lines | artifacts http adapter, about 12k lines |
| Main friction the file found | 75 application packages with no rule between them | services need a ninth rule to stop calling each other | controllers must import generated servers, against the ring rule | one deploy becomes three transactions | one module, artifacts, holds most of today's code |

## Where the project is on its roadmap

The plan has 42 phases, P0 to P41. P0 to P9 and P9b are done; P9c is in
progress. By phase count the project is about a quarter of the way through.
By shape it is further through some axes than others, and that difference
drives the rest of this document.

| Growth axis | Done by P9c | Still to come (P10 to P41) |
| --- | --- | --- |
| Package ecosystems | 49 package types with native protocols once P9c closes (P29 to P33 were pulled forward into P9c) | Gap completion in P28; no new ecosystem families planned |
| Storage backends | SQLite, PostgreSQL; filesystem, S3, GCS, Azure, mirror; three CDNs | MySQL (P34), cold tier (P16), external KMS (P38) |
| Repository classes | Local, remote | Virtual (P10), federated convergence (P13) |
| Business domains | Storage, packages, remote retrieval, search, builds, identity basics | About fifteen new ones (below) |
| Entry points | REST management API, native protocols, CLI | Peer API, scheduler, worker host, SCIM, browser sessions, web console, GraphQL gateway, possible internal RPC |
| Integration style | Synchronous calls inside one process | Events, outboxes, webhooks, replication, interception hooks, multiple nodes |

The axis that shaped P0 to P9 (more protocols over more backends) is close to
saturated. The axes that shape P10 to P41 are new business domains, events and
synchronous interception, multiple instances and multiple nodes. An
architecture chosen for what is built so far would be optimised for the part
of the work that is mostly finished.

## What the system is

The facts about the product that any architecture has to serve, taken from
what the software does and must do, not from how it is currently built.

### Today (P0 to P9c)

- **One server, many front doors.** A management REST API with several
  OpenAPI-described services (artifactory, access, event, distribution, grid,
  pfed, worker), 49 package types each with its own native client protocol, a
  Terraform backend, a Git LFS endpoint, and a CLI for migrate, config and
  keys.
- **Metadata and content are stored apart.** Item metadata, package index,
  identities, builds, configuration and queues live in a SQL store. Content
  lives in a blob store, optionally delivered through a signed CDN URL.
- **A deploy is one logical write across several concerns.** Storing the
  content, recording the item, validating it against its ecosystem's rules,
  updating the package index, and in some cases updating build info or a
  registry upload session happen for one client request and must agree.
- **Each ecosystem has three faces.** A protocol, a format (what a valid
  package is and how it is indexed), and a native client used in tests.
- **Remote repositories compose upstreams.** Fetch, cache, revalidate, Smart
  Remote synchronisation and upstream search are their own logic.
- **Cross-cutting concerns.** Authentication, authorisation with a native
  permission model, AQL over items, builds and properties, sealed secrets,
  garbage collection and trash, a persistent work queue, generated server and
  client code.

### What the roadmap adds (P10 to P41)

New business domains, each with its own model, API surface, Terraform
resources and native client workflows:

| Domain | Phases | What it owns |
| --- | --- | --- |
| Virtual resolution | P10 | Ordered member resolution and per-format metadata merging |
| Projects and tenancy | P11 | Projects, membership, roles, environments, key prefixes, quotas |
| Notifications | P11 | Webhook event catalogue, criteria, signing, retries, delivery log |
| Retention | P11, P16 | Cleanup policies with runs and dry runs; archive policies, cold tier, restore jobs |
| Replication | P11, P37 | Push and pull, multi-push, event and checksum replication, cross-vendor adapters |
| System configuration | P12, P36 | XML and YAML descriptors, backups, mail, proxies, import and export |
| Federation and topology | P13, P14 | Peer trust and pairing, ordered outbox, federated repositories, Access federation, pfed, grid |
| Releases and distribution | P15, P24 | Release bundles v1 and v2, signing, lifecycle stages, promotion, edge distribution |
| Workers | P17 | Sandboxed TypeScript runtime in helper processes; BEFORE and AFTER hooks on item and event operations |
| Enterprise identity | P12, P21, P22 | LDAP, SAML, human OIDC, Crowd, HTTP-SSO, sessions, MFA, SCIM provisioning and deprovisioning |
| Evidence | P23 | Attestations, DSSE verification, trusted signers, immutable payloads, subject association |
| Governance | P25 | AppTrust applications and versions, Unified Policy, control points, approvals, audit history |
| Security intelligence | P26, P27 | Xray indexing, SBOMs, vulnerability and licence analysis, watches, Catalog, OneModel GraphQL, Curation admission |
| Operations | P18, P38 | Metrics, health, support bundles, Vault, IAM, log shipping, queue administration, key administration |
| Estate | P40 | Mission Control, MyJFrog, JFConnect, bridges, Pipelines replacement |

Pressures that cut across those domains:

- **Events become the backbone.** Webhooks (P11) need an exact event
  catalogue. Federation (P13) needs an ordered outbox with acknowledgements
  and idempotent apply. Replication (P37) is event-triggered. Workers (P17)
  run on AFTER events. Scanning (P26) is triggered by deploys and feed
  updates. Runtime workload events (P27) feed Workers and governance. Audit
  history (P25) is an event log by another name.
- **Synchronous interception of core operations.** A BEFORE_DOWNLOAD Worker
  blocks a download (P17). Curation blocks a prohibited dependency on cache
  miss and cached retrieval (P27). Governance gates promotion (P25). The
  artifact core must offer hook points without importing the domains that
  hook into it.
- **Cross-domain reads.** OneModel (P26) is a GraphQL gateway across packages,
  Catalog, Evidence, releases, AppTrust and Grid, with "service-specific data
  owners". The web console (P39) reads every domain through the public APIs.
  Authorisation is consulted on every read in every domain.
- **Many instances and many nodes.** Three-peer convergence (P13), edge peers
  (P15), multi-instance replication (P11, P37), and high availability with
  leases, fencing, distributed work and rolling upgrades (P35). P35 also asks
  for internal RPC and peer-version negotiation to be resolved against real
  consumers.
- **Parallel delivery lanes.** The plan runs P21 to P22, P23 to P27, P28 to
  P33 and P34 to P38 as independent lanes once P20 lands. Work in different
  lanes should land in different parts of the tree.
- **Immutability and audit.** Immutable releases, immutable evidence payloads,
  immutable policy audit history, and recovery that must preserve all of them
  (P36).

Size for scale today: roughly 190k lines of hand-written Go across 174
packages, plus about 250k lines of generated code. The roadmap plausibly
doubles the hand-written figure, mostly in new domains.

## Option 1: Layered

Three horizontal layers (transport, application, persistence) over a shared
model. Each layer may call the one below it. Packages are grouped by
technical role first and by subject second.

### Indicative tree

```text
cmd/
  artifactweave/                   entry point: cobra commands, hands off to app
internal/
  app/                             composition root: config → stores → services → router
  config/ buildinfo/
  model/ model/wire/               shared types for every domain
  transport/                       LAYER 1: everything that speaks a protocol
    http/
      router.go middleware.go gen/<spec>/
      management/<family>/         REST families
      protocol/<ecosystem>/        49 native protocols
      scim/ graphql/ web/          roadmap: SCIM, OneModel gateway, console and sessions
    cli/
    peer/                          roadmap: federation protocol
    scheduler/                     roadmap: cleanup, archive, replication schedules
    workerhost/                    roadmap: helper process entry point
  application/                     LAYER 2: business operations, transaction boundaries
    item/ trash/ gc/ multipart/ pkgindex/ registry/ build/ search/ remote/ virtual/
    format/<ecosystem>/
    identity/ permission/ roles/ oidc/ auth/ authz/ ldap/ saml/ scim/ session/
    project/ webhook/ cleanup/ archive/ replication/ federation/ topology/
    release/ distribution/ lifecycle/ workers/ evidence/ apptrust/ policy/
    scan/ catalog/ curation/ backup/ migration/ operations/ estate/
  persistence/                     LAYER 3: storage and external systems
    store/ store/{sqlcommon,sqlite,postgres,mysql,inmem}/
    blob/ blob/{fs,s3,gcs,azure,mirror,session,cold}/
    cdn/ upstream/ crypt/ archive/ outbox/ lease/
    idp/ vault/ kms/ feeds/ engines/                         roadmap: external systems
  support/                         aql parser, layout patterns, version comparators
pkg/
  client/<spec>/ containertest/
```

### How it handles today's problems

- **One write, several concerns.** The item package owns the transaction and
  calls format, pkgindex and build inside it as plain function calls in one
  layer.
- **49 ecosystems.** Two parallel directories per ecosystem, one in transport
  and one in application.
- **Several backends and entry points.** Interfaces in persistence, entry
  points as transport directories. Straightforward.

### How it handles the roadmap

- **New domains.** All of them land in `application/`, which grows from about
  twenty packages to about sixty with no rule about which may call which.
- **Events.** No natural home. An outbox lands in persistence, but publishing
  and subscribing are application concerns spread across every domain.
- **Interception.** Item calls workers, curation and policy directly, so the
  core depends on every domain that hooks into it.
- **Parallel lanes.** Every lane edits `application/` and `transport/http`,
  and usually `model/`.
- **Many nodes.** No structural help; leases and fencing are a persistence
  package everyone must remember to use.

### Pros

- The simplest mental model; any Go developer reads it without an
  introduction.
- Transactions are trivial because every participant shares one layer.
- Few interfaces, and the ones that exist are natural seams.
- Adding an ecosystem or a backend is cheap.

### Cons

- The application layer becomes a flat list of sixty peers with an arbitrary
  dependency graph between them.
- The core of the product (serving packages) ends up importing governance,
  security and Workers to call their hooks.
- One shared model package for every domain becomes a merge-conflict hotspot
  and couples unrelated domains through shared types.
- Events and interception, two of the roadmap's defining pressures, have no
  place in the pattern.

### Roadmap verdict

Adequate for P0 to P9, poor for P10 to P41. The roadmap's growth is the kind
this pattern handles worst.

## Option 2: Hexagonal (ports and adapters)

One application core that defines ports. Driving ports describe what the
outside world can ask the core to do; driven ports describe what the core
needs from outside. Adapters implement ports on either side. The core never
imports an adapter.

### Indicative tree

```text
cmd/
  artifactweave/
internal/
  app/                             composition root; the only package that imports both sides
  config/ buildinfo/
  core/                            THE HEXAGON
    model/ model/wire/
    port/
      driving/                     content.go packages.go identity.go builds.go search.go
                                   roadmap: projects.go webhooks.go retention.go replication.go
                                   federation.go releases.go workers.go evidence.go
                                   governance.go security.go operations.go estate.go ...
      driven/                      store.go blob.go cdn.go upstream.go keyring.go archive.go
                                   roadmap: outbox.go lease.go idp.go vault.go kms.go
                                   scanner.go feed.go sandbox.go peer.go
    service/<domain>/              one package per domain, about sixty by P41
    format/<ecosystem>/            49 formats
    authz/ aql/ layout/
  adapter/
    driving/
      http/ http/protocol/<ecosystem>/ http/management/<family>/ http/gen/<spec>/
      cli/ peer/ scheduler/ workerhost/ scim/ graphql/ web/
    driven/
      sqlstore/{sqlcommon,sqlite,postgres,mysql}/ inmemstore/
      blob{fs,s3,gcs,azure,mirror,cold}/ cdn{cloudfront,cloudcdn,frontdoor}/
      httpupstream/ keyring/ archive/ outbox/ lease/
      ldap/ saml/ oidc/ vault/ kms/ goja/ scanengine/ feeds/
  contract/                        storetest/ blobtest/ cdntest/ formattest/ outboxtest/
pkg/
  client/<spec>/ containertest/
```

### How it handles today's problems

- **One write, several concerns.** The unit of work is a driven port; the
  item service opens it and calls format and package index inside the core.
- **49 ecosystems.** Protocol as driving adapter, format as core.
- **Several backends and entry points.** The exact case the pattern was
  designed for; every one is an adapter verified by a contract suite.

### How it handles the roadmap

- **New entry points and backends.** Peer, SCIM, GraphQL, console, scheduler,
  worker host, MySQL, IdPs, Vault, KMS, scanners and feeds are all adapters.
  The pattern absorbs these without strain.
- **New domains.** All of them are inside one core. The hexagon says nothing
  about how governance relates to storage; both are "core". The driving port
  directory grows to twenty or more files and the service directory to sixty
  packages with no boundary rule between them.
- **Events.** An outbox is a driven port, which is clean. Subscribers are
  services inside the same core, so the core is both producer and consumer of
  its own events with no boundary to say who may react to what.
- **Interception.** Hook points are a driven port the item service calls;
  Workers, curation and policy implement it. This works and keeps the item
  service clean, but those implementations are also core services, so the
  inversion is between siblings, not across a boundary.
- **Parallel lanes.** Lanes add adapters in separate directories, which
  helps, but every lane also edits `core/port`, `core/model` and `app`.
- **Many nodes.** Leases and fencing are driven ports; the pattern accepts
  them without making them structural.

### Pros

- Still the best fit for the protocol and backend axis, which remains real
  and is mostly built.
- The core is testable with fakes of driven ports; every adapter is
  testable against a contract suite.
- Three-line dependency rule, mechanically enforceable.
- Absorbs every new entry point and external system on the roadmap without
  redesign.

### Cons

- One core for about twenty business domains. The hexagon protects the core
  from the outside world but not from itself, and the roadmap's growth is
  inside.
- `core/model` and `core/port` become shared files for every domain and every
  lane.
- Wide driving ports multiply: twenty port files, several of them very wide.
- The composition root wires about sixty services and fifty adapters in one
  place.

### Roadmap verdict

Remains the best shape for the part of the system that serves packages over
backends. As the shape of the whole system through P41 it is too coarse: it
has one boundary, and the roadmap needs about fifteen.

## Option 3: Clean architecture

Concentric rings (entities, use cases, interface adapters, frameworks) with
inward-only dependencies. Interfaces are declared by the consuming use case.
The application ring is organised by operation.

### Indicative tree

```text
cmd/
  artifactweave/
internal/
  entity/                          RING 1: pure domain for every subject
    item.go repo.go package.go build.go user.go permission.go
    roadmap: project.go webhook.go policy.go release.go evidence.go application.go
    finding.go peer.go worker.go ...
    aql/ layout/ authz/
  usecase/                         RING 2: one package per operation family, each with port.go
    deploy/ download/ delete/ restore/ gc/ multipart/ index/ registry/ search/ fetch/
    resolvevirtual/
    authenticate/ authorize/ token/ user/ group/ permission/ role/ project/ provision/
    webhookdeliver/ cleanup/ archive/ restorearchive/ replicate/ federate/ pair/
    createrelease/ promote/ distribute/ runworker/ attest/ verify/ evaluatepolicy/
    approve/ scan/ curate/ backup/ import/ export/ ...  (well over a hundred by P41)
    format/<ecosystem>/
  adapter/                         RING 3
    http/ cli/ peer/ scim/ graphql/ web/ scheduler/ workerhost/
    repository/{sqlcommon,sqlite,postgres,mysql,inmem}/ repository/aqlsql/
    gateway/{blob,cdn,upstream,keyring,archive,idp,vault,kms,scanner,feeds,sandbox}/
  infrastructure/                  RING 4: router, db open, server lifecycle, gen/<spec>/, otel
  app/
pkg/
  client/<spec>/ containertest/
```

### How it handles today's problems

- **One write, several concerns.** Deploy declares a unit-of-work port and
  narrow writer ports; formats implement a hook port it declares.
- **49 ecosystems.** Formats in the use-case ring, protocols in ring 3.
- **Several backends and entry points.** Ring-3 adapters satisfying use-case
  ports.

### How it handles the roadmap

- **New domains.** They become more entities and more use cases. Rings
  separate policy from detail but not one domain from another: a governance
  use case may freely call a storage repository port, because both are in the
  rings it may use.
- **Events.** A publish port per use case is natural; subscribers are more use
  cases. Event flow between domains is not visible in the structure.
- **Interception.** Narrow hook ports declared by download and deploy work
  well, and the per-consumer rule keeps them small.
- **Parallel lanes.** Lanes add use cases in separate packages, which helps;
  entities are shared, which does not.
- **Many nodes.** Leases are another port.

### Pros

- The strictest dependency rule and the narrowest interfaces; fakes stay tiny
  as the system grows.
- Use-case packages remain a readable catalogue of operations.
- Pure logic (AQL, layouts, authorisation decisions, policy evaluation) has a
  named home.

### Cons

- Well over a hundred use-case packages by P41, each with its own port file
  and its own small interfaces over the same stores.
- No boundary between domains; the rings are the only structure, and they cut
  the wrong way for a system that grows by domain.
- One entity ring for every domain becomes the shared hotspot.
- The deploy write's shared unit of work still pulls a common port back to
  the centre.

### Roadmap verdict

Scales in rigour but not in organisation. It keeps every domain honest about
infrastructure and does nothing to keep domains apart from each other.

## Option 4: Domain-driven design (tactical and strategic)

Full DDD: bounded contexts for strategic structure, and inside each context
the tactical building blocks (aggregates as consistency boundaries,
repositories per aggregate, value objects, domain events, application
services). Each context has the same internal layering. Contexts integrate
through published interfaces and domain events.

### Context map

| Context | Owns |
| --- | --- |
| content | items, folders, blob references, trash, garbage collection, multipart, download |
| packages | package model, index, the 49 ecosystems (format and protocol together), registry sessions |
| builds | build info, promotion, build search |
| search | AQL across items, builds and properties |
| remote | upstream retrieval, cache policy, Smart Remote |
| virtual | member resolution and metadata merging |
| access | users, groups, tokens, permissions, roles, projects, LDAP, SAML, OIDC, SCIM, sessions |
| configuration | system configuration, descriptors, backups |
| notifications | webhooks |
| retention | cleanup policies, archive policies, cold tier |
| replication | push, pull, multi-push, event replication |
| federation | peers, outbox, federated repositories, topology |
| releases | release bundles v1 and v2, lifecycle, distribution |
| workers | runtime and hooks |
| evidence | attestations, signers |
| governance | applications, policies, approvals |
| security | scanning, SBOMs, Catalog, Curation |
| operations | metrics, integrations, support |
| estate | Mission Control, MyJFrog, bridges |

### Indicative tree

```text
cmd/
  artifactweave/
internal/
  app/                             composition root
  kernel/                          shared kernel: repo key, path, checksum, principal, version, wire/
  content/
    domain/                        aggregates (Item, Folder), value objects, repository interfaces,
                                   domain events (ItemDeployed, ItemDeleted, ...)
    application/                   commands and queries
    adapters/{http,persistence,blob}/
    api.go                         published interface for other contexts
  packages/
    domain/ application/
    <ecosystem>/                   49 directories: format, protocol and client tests together
    adapters/{http,persistence}/
    api.go
  builds/ search/ remote/ virtual/ access/ configuration/ notifications/ retention/
  replication/ federation/ releases/ workers/ evidence/ governance/ security/
  operations/ estate/
    domain/ application/ adapters/ api.go
  shared/                          sqlcommon/ outbox/ eventbus/ crypt/ archive/ httpkit/ gen/<spec>/
pkg/
  client/<spec>/ containertest/
```

### How it handles today's problems

- **One write, several concerns.** This is where tactical DDD bites. Its rule
  is one aggregate per transaction, with other aggregates updated by domain
  events. A deploy changes the item (content), the package index (packages)
  and sometimes the build record (builds). Following the rule makes the index
  and build record eventually consistent with the item, which clients can
  observe (a package published but not yet listed). Breaking the rule means
  passing a transaction across contexts, which the pattern exists to prevent.
- **49 ecosystems.** One directory per ecosystem; the strongest answer of any
  option.
- **Cross-context reads.** AQL joins items, builds and properties across three
  contexts; permissions gate every read; virtual resolution spans remote,
  content and packages. Each needs a published query interface or a read
  model.

### How it handles the roadmap

- **New domains.** Each is a new context with a clear home and a clear
  boundary. The pattern's strongest point, and the roadmap's biggest axis.
- **Events.** First-class. Webhooks, federation, replication, Workers AFTER
  hooks, scanning and audit are all event consumers in their own contexts.
  The roadmap needs events anyway, so the pattern's insistence on them is a
  fit rather than a cost for these domains.
- **Interception.** A published hook interface on content (BEFORE_DOWNLOAD,
  admission) implemented by workers, security and governance.
- **Parallel lanes.** Each lane maps to one or two contexts; lanes rarely
  touch the same files.
- **OneModel.** A GraphQL gateway over per-context subgraphs is exactly the
  "published query interface" DDD prescribes; the plan's "service-specific
  data owners" is DDD language.
- **Many nodes.** Contexts are the seams that could become RPC boundaries if
  P35 finds a real consumer for internal RPC.

### Pros

- Organises the system along the axis it grows on through P41.
- Ecosystems as single directories, new domains as single directories.
- Events, interception and cross-context queries all have named mechanisms.
- Parallel lanes map onto contexts.

### Cons

- Tactical DDD's one-aggregate-per-transaction rule conflicts with the deploy
  write, the core operation of the product, and pushes content, packages and
  builds apart along the line where they most need to be together.
- Splitting the artifact core into content, packages, builds, search, remote
  and virtual turns its many internal calls into published-interface calls
  and its joins into read models.
- Uniform domain, application and adapters layering in nineteen contexts is
  heavy for small ones (estate, notifications).
- Tactical patterns (aggregates with unexported fields, repositories per
  aggregate) fit governance and releases well and fit high-volume streaming
  reads and writes poorly.

### Roadmap verdict

The right strategic shape for the roadmap. The tactical rules and the
fine-grained context map are wrong for the artifact core, and that is the
part already built and the part every other context depends on.

## Option 5: Modular monolith (bounded contexts, ports and adapters inside)

DDD's strategic design without its tactical rules, and hexagonal
architecture inside each context. One binary, one process per node, one
database, but split into modules that each own a business domain, expose a
published interface, and communicate through that interface or through
events written in the producer's transaction.

The context boundaries are drawn by consistency: anything that must change in
one transaction is inside one context. That keeps the artifact core whole and
lets every roadmap domain be its own module.

### Context map

| Context | Owns | Phases |
| --- | --- | --- |
| artifacts | repositories of every class, items, content, trash, garbage collection, package index, the 49 ecosystems, remote retrieval, virtual resolution, build info, AQL and searches | P3 to P10, P28 |
| access | users, groups, tokens, permissions, roles, projects, environments, LDAP, SAML, OIDC, sessions, SCIM | P4, P11, P12, P21, P22 |
| configuration | system configuration, descriptors, import and export, backups | P12, P36 |
| notifications | webhook catalogue, criteria, signing, delivery | P11 |
| retention | cleanup policies, archive policies, cold tier, restore jobs | P11, P16 |
| federation | peer trust, outbox delivery, federated repositories, replication, Access federation, topology | P11, P13, P14, P37 |
| releases | release bundles v1 and v2, lifecycle stages, promotion, distribution to edges | P15, P24 |
| workers | sandbox runtime, worker catalogue, executions, hook bindings | P17 |
| evidence | attestations, verification, trusted signers | P23 |
| governance | applications, Unified Policy, control points, approvals, audit history | P25 |
| security | indexing, scanning, SBOMs, findings, watches, Catalog, Curation admission | P26, P27 |
| operations | health, metrics, support bundles, external integrations, queue administration | P18, P38 |
| estate | Mission Control, MyJFrog, JFConnect, bridges | P40 |

Contexts that do not exist yet are created when their phase starts, not
before. The map above is the expected end state, not a list of empty
directories to create now.

### Indicative tree

```text
cmd/
  artifactweave/                   cobra; calls app.Run
internal/
  app/                             composition root: builds the platform, then each module with
                                   an explicit Deps struct, then the driving adapters
  platform/                        shared infrastructure every module may use; no business rules
    sql/{dialect,sqlite,postgres,mysql}/   dialects, migrator, unit of work
    outbox/                        transactional outbox: Append(tx, event); ordered, durable
    events/                        in-process subscription and dispatch from the outbox
    work/                          persistent work queue, leases, fencing (HA-ready, P35)
    blob/                          blob port and backends: fs, s3, gcs, azure, mirror, cold
    cdn/ crypt/ archive/ clock/ httpkit/ (router, middleware, apierror, baseurl)
    gen/<spec>/                    generated strict servers
  kernel/                          shared value types only: RepoKey, Path, Checksum, Principal,
    wire/                          Version, PackageType; JFrog wire structs
  artifacts/                       the big context, laid out as a hexagon
    api/                           PUBLISHED: interfaces and DTOs other modules may call
      artifacts.go                 Items, Packages, Builds, Search
      hooks.go                     Gate interface: BeforeDownload, BeforeDeploy, Admit
                                   (implemented by workers, security, governance)
      events.go                    ItemDeployed, ItemDeleted, PropertiesChanged, ...
    core/                          no import of adapters/
      model/ port/ (driven: store, upstream, gate)
      item/ trash/ gc/ multipart/ pkgindex/ registry/ build/ search/ remote/ virtual/
    ecosystem/                     one directory per package type
      npm/                         npm.go (format hooks, core) and http/ (native protocol adapter)
      maven/ docker/ pypi/ ...     49 in all; rule: nothing outside <eco>/http imports <eco>/http
    adapters/
      http/                        storage, items, searches, builds, repositories management
      sql/                         artifact tables over platform/sql
      upstream/                    remote HTTP transport, Docker Hub auth, fault fakes
  access/                          hexagon: api/ core/ adapters/{http,sql,ldap,saml,oidc,scim}/
    api/                           Authenticate, Decider (consulted by every module), Projects
  configuration/                   api/ core/ adapters/{http,descriptor}/
  notifications/                   small: api/ notifications.go adapters/http/
  retention/                       api/ core/ adapters/{http,sql}/
  federation/                      api/ core/ adapters/{peer,http,sql}/
  releases/                        api/ core/ adapters/{http,sql}/
  workers/                         api/ core/ adapters/{http,sandbox}/ (goja helper processes)
  evidence/ governance/ security/  api/ core/ adapters/   (each created in its phase)
  operations/ estate/              small modules; flat inside
  gateway/                         cross-module driving adapters that own no data
    onemodel/                      GraphQL gateway composing subgraphs each module publishes
    console/                       web console and browser sessions over the public APIs
  layout/                          the dependency rules below, as a test
pkg/
  client/<spec>/ containertest/
test/ tools/ api/ docs/ deploy/
```

### Dependency rules

1. A module imports `platform/`, `kernel/`, `pkg/` and other modules' `api/`
   packages only. Never another module's `core/` or `adapters/`.
2. Inside a module, `core/` never imports `adapters/`.
3. `gateway/` imports modules' `api/` packages only and owns no tables.
4. `platform/` and `kernel/` import no module.
5. Only `app/` imports every module's constructor.

All five are import-path rules, so a test can hold them.

### How it handles today's problems

- **One write, several concerns.** Item, package index, format hooks and
  build record are all inside artifacts; the deploy transaction never crosses
  a module boundary. Events for other modules are appended to the outbox in
  that same transaction, so they are exactly as durable as the write.
- **49 ecosystems.** Each is one directory under `artifacts/ecosystem/`, with
  the format in the directory and the protocol in its `http/` subdirectory.
  The hexagon's edge runs through the ecosystem directory rather than between
  two trees.
- **Several backends and entry points.** Blob, CDN and SQL dialects are
  platform adapters; each module has its own driving adapters. Contract suites
  stay per port.
- **Cross-cutting reads.** AQL and virtual resolution stay inside artifacts.
  Authorisation is the access module's published `Decider`, a synchronous
  in-process call.

### How it handles the roadmap

- **New domains.** Each phase that introduces a domain creates one module.
  Small modules are flat; large ones are hexagons. The rules are the same.
- **Events.** One mechanism for all of them: the producer appends to the
  outbox inside its transaction; notifications, federation, workers, security,
  evidence and audit subscribe. Federation's ordered outbox with
  acknowledgements (P13) is the same outbox with a remote consumer.
- **Interception.** Artifacts declares the `Gate` interface in its `api/`;
  workers, security (Curation) and governance implement it; `app/` registers
  the implementations. Artifacts never imports them.
- **OneModel and the console.** Gateways over published interfaces, owning no
  data, which is what the plan describes.
- **Parallel lanes.** Identity lanes edit access; supply-chain lanes edit
  evidence, releases, governance and security; operations lanes edit
  platform and operations. Overlap is limited to `app/` wiring.
- **Many nodes.** Modules hold no process state; the outbox, work queue and
  leases in `platform/` are the HA surface. If P35 finds a consumer for
  internal RPC, a module's `api/` package is the contract to put on the wire.

### Pros

- Organised along the roadmap's growth axis without breaking the product's
  core transaction.
- Keeps hexagonal's strengths where they matter most: the artifacts module is
  a hexagon with contract-tested ports and backends.
- One event mechanism, one interception mechanism, one authorisation
  mechanism, each named and owned.
- Ecosystems and domains each get a single directory.
- The dependency rules are five import-path checks.
- Modules can be built and reviewed in parallel; the plan's lanes map onto
  them.
- Keeps a single deployable while leaving a clean split path if one is ever
  wanted.

### Cons

- The most up-front structure of the five options, and more rules for
  contributors to learn.
- The artifacts module remains large: 49 ecosystems plus storage, remote,
  virtual, builds and search. It needs its own internal discipline.
- Event consumers bring eventual consistency into notifications, federation,
  security and evidence: idempotent handlers, ordering, replay and
  dead-letter tooling become platform work.
- Boundaries for evidence, releases, governance and security are drawn before
  their research phases (P23 to P27). Some will move once those contracts are
  known.
- Published `api/` interfaces can become chatty. A module that calls another
  many times per request is a sign the boundary is wrong, and that is only
  visible in use.
- The composition root wires about thirteen modules, each with a Deps struct.
  Manual wiring stays workable but is long.

### Roadmap verdict

The best fit for the system as the plan describes it at P41, at the cost of
the most structure and the most event infrastructure.

## Comparison matrix

Each option rated on its own terms for this system. Good, Fair, Poor.

### Today's shape (P0 to P9c)

| Criterion | 1 Layered | 2 Hexagonal | 3 Clean | 4 DDD | 5 Modular |
| --- | --- | --- | --- | --- | --- |
| Many entry points over one core | Fair | Good | Good | Fair | Good |
| Many storage backends | Good | Good | Good | Good | Good |
| 49 ecosystems, each protocol plus format | Fair | Fair | Fair | Good | Good |
| One atomic deploy write | Good | Good | Fair | Poor | Good |
| AQL, permissions and virtual reads | Good | Good | Fair | Poor | Good |
| Generated server and client code | Good | Good | Fair | Fair | Good |
| Contract suites per replaceable part | Good | Good | Fair | Fair | Good |

### Roadmap shape (P10 to P41)

| Criterion | 1 Layered | 2 Hexagonal | 3 Clean | 4 DDD | 5 Modular |
| --- | --- | --- | --- | --- | --- |
| About fifteen new business domains | Poor | Fair | Poor | Good | Good |
| Events as the integration backbone | Poor | Fair | Fair | Good | Good |
| Interception without core depending on hookers | Poor | Fair | Good | Good | Good |
| Cross-domain query gateway (OneModel) and console | Fair | Fair | Fair | Good | Good |
| Multi-instance and multi-node (P13, P35, P37) | Poor | Fair | Fair | Good | Good |
| Parallel delivery lanes without collisions | Poor | Fair | Fair | Good | Good |
| New entry points and external systems | Fair | Good | Good | Fair | Good |
| Immutability and audit histories | Fair | Fair | Good | Good | Good |

### Cost of the structure itself

| Criterion | 1 Layered | 2 Hexagonal | 3 Clean | 4 DDD | 5 Modular |
| --- | --- | --- | --- | --- | --- |
| Directory count and ceremony | Good | Fair | Poor | Poor | Fair |
| Newcomer learning curve | Good | Good | Fair | Fair | Fair |
| Composition root complexity | Good | Poor at P41 | Poor | Fair | Fair |
| Event infrastructure to build and operate | Good | Fair | Fair | Poor | Fair |
| Risk of drawing boundaries before research | Good | Good | Good | Poor | Fair |
| Dependency rule expressible as a test | Fair | Good | Good | Fair | Good |

## Recommendation

**Option 5, the modular monolith.**

Hexagonal was the best answer for the system as built through P9. It is still
the best shape for the part of the system that serves packages over storage
backends, and option 5 keeps it there: the artifacts module is a hexagon.

It is not the best shape for the whole system across the roadmap. Being at P9
means the protocol and backend work is mostly done, and almost everything
still to build is new business domains (projects, notifications, retention,
federation, releases, Workers, enterprise identity, evidence, governance,
security, operations, estate), events between them, and hook points into the
artifact core. A single hexagon has one boundary, between the core and the
outside world. The roadmap needs boundaries between domains inside the core,
and that is what bounded contexts provide.

Pure DDD (option 4) gets the strategic shape right but its tactical rules
split the artifact core along its most important transaction. Option 5 takes
DDD's contexts, draws them by consistency so the deploy write stays in one
module, uses hexagonal inside the modules that are large enough to need it,
and makes the outbox the single mechanism for everything the roadmap needs to
hear about.

### What makes the choice hold

- **Draw only the contexts that exist or are about to.** Start with
  artifacts, access, configuration and the platform as full modules. The
  stores already hold data for webhooks, cleanup, peers, bundles and workers
  (P1b and P2), so notifications, retention, federation, releases and workers
  start as storage-only skeletons that own those tables and nothing else.
  Each skeleton gains its behaviour in its phase: P11, P13, P15 and P17. The
  supply-chain modules are created when their research phases settle their
  contracts.
- **Build the outbox once, early.** P11's webhooks and P13's peer outbox are
  the first two consumers. If each builds its own event mechanism the
  architecture loses its main integration rule before it starts.
- **Define the Gate interface before P17.** Workers, Curation and governance
  all need it; defining it once prevents three different hook styles in the
  artifacts core.
- **Treat a chatty `api/` as a boundary bug.** If one module calls another
  many times per request, move the boundary rather than adding caching.

### What would change the recommendation

- **If the plan were cut back to the artifact core.** Without P11 to P41's
  domains, option 2 is the better choice: less structure, same strengths.
- **If P35 settled on separate services.** Option 5's modules become the
  service boundaries, so the choice stands, but event infrastructure would
  then need to cross processes from the start.
- **If the supply-chain research shows evidence, releases and governance
  share one consistency boundary.** Merge them into one module. The pattern
  absorbs that; it is a context-map change, not an architecture change.

The dependency-injection question follows from this choice. Option 5 favours
one constructor per module taking an explicit Deps struct, wired by hand in
`app/`; whether a wiring aid is worth it at thirteen modules is the next
decision.

## Appendix: distance from today's tree

For information only; not part of the evaluation above.

Today the module is `cmd/`, `pkg/` (public contracts, models, backends and
generated clients), `internal/` (app, server with 27 protocol packages and a
65-file root, 34 format packages, 14 services, SQL stores, auth, remote), with
manual constructor injection in `internal/app` and an import test that keeps
`pkg/` free of `internal/` and formats free of the server.

| Option | What moves | What is re-cut |
| --- | --- | --- |
| 1 Layered | pkg contracts and backends into persistence; server into transport; services and formats into application | the server root split by family |
| 2 Hexagonal | server into adapter/driving; sqlstore, blob backends, remote transport, crypt into adapter/driven; store, blob, cdn interfaces into core/port | driving-port interfaces added; a package-index port added for formats |
| 3 Clean | as option 2, plus types, aql, layout into entity and config, router, db open into infrastructure | services split by operation; per-use-case ports; AQL compiler separated from the parser |
| 4 DDD | everything, into about nineteen contexts | store split per context; formats merged with protocol packages; artifact core split into six contexts |
| 5 Modular | server, service, format, remote, sqlstore, repos and pkg/store into artifacts; auth, authz, identity, permission, roles, oidc into access; descriptor and config into configuration; sqlcommon, work, blob, cdn, crypt, archive into platform | formats and protocol packages paired under artifacts/ecosystem; artifacts api and Gate interface added; outbox added to platform |

Most of the system in option 5's end state does not exist yet. Today's code
lands in four full modules (artifacts, access, configuration, platform) and
five storage-only skeletons (notifications, retention, federation, releases,
workers) that own tables P1b and P2 already created. The remaining four
modules are created by the phases that introduce them.
