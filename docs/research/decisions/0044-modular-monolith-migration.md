# 0044: Modular-monolith migration

Status: Accepted

## Context

The package tree grew by technical layer. When the migration was planned the repository
had 190 Go packages. `internal/server` held 73k source lines and 101k test lines (76 root
files and 127 root test files), `internal/format` had 45 packages, and `pkg/store` (17k
lines) exposed one `Store` aggregate that 72 packages took whole. Virtual repositories,
projects, federation, release bundles, evidence and the other roadmap capabilities would
all land in those few packages.

The [architecture options](../architecture-options/) research compares five layouts. The
target is the modular monolith,
[`5-modular-monolith.md`](../architecture-options/5-modular-monolith.md): bounded contexts
as modules with `api`, `core` and `adapters` layers, shared infrastructure in `platform`,
shared values in `kernel`, one binary and one database.

Moving about 190 packages must not change behaviour, must keep the merged coverage gate
passing (95.0% total, with about three statements of headroom) and must leave every
intermediate tree buildable. This record states the target, the deviations from the
target document and the rules that hold while the tree is half moved. The
[implementation plan](../../implementation/plan.md#p9d-modular-monolith-migration) orders
the work.

## Decision

### Target and staging

- **Target.** The layout of `5-modular-monolith.md`, with the deviations this record lists.
- **Staging is the future `internal/`.** Every target `internal/<x>` is built at
  `migration/<x>` (import path `github.com/weaveplatform/artifactweave/migration/<x>`).
  A staged path has no `internal` element, so any package of the module, legacy or staged,
  may import a staged one and packages move bottom-up. The legacy `internal/` and the
  moving `pkg/` packages empty out; at the end `migration/` is renamed to `internal/` and
  every path is rewritten. `cmd/`, `api/`, `pkg/client`, `pkg/containertest`, `test/`,
  `tools/`, `deploy/` and `docs/` change in place.
- **Legacy roots.** `internal/**` and `pkg/{aql,blob,cdn,layout,repo,store,types}/**`.
  Nothing else is legacy.
- **The ledger.** `migration/ledger.json` has one package entry per legacy package
  directory (`from`, an action of `move`, `merge`, `split` or `delete`, the target `to`
  and the owning ticket), file entries for the files of split packages, and one entry
  per placeholder. Status (pending, bridged, done, broken) is derived from the tree, so
  no change edits the ledger to record progress. The ledger, its library and the move
  tool are deleted when `migration/` is promoted; `docs/implementation/p9d-path-map.json`
  keeps the final map from legacy to final paths.
- **Root-relative paths.** `platform/testkit/repopath` finds the repository root, reads
  fixtures by root-relative path and holds the walk rule that every repository walk
  shares. It survives promotion.

### Behaviour

- **Behaviour-preserving.** Routes, statuses, response envelopes, YAML configuration keys,
  CLI behaviour and database behaviour stay the same, except the migration-history reset
  under Persistence. Error, log and metric text stay byte-identical unless a change states
  otherwise.
- **No renames beyond package paths**, except the exports and package names that a new
  package boundary requires, such as the `<type>http` package names and
  `remote.PolicyError`.
- **The work queue's meter scope moves once.** Its OpenTelemetry meter scope is set
  straight to its final path, `github.com/weaveplatform/artifactweave/internal/platform/work`,
  rather than to a staged path and again at promotion.

### Public types and published `api` surface

- **Two public types become internal.** `pkg/repo` becomes `artifacts/api/repo` and
  `pkg/types/wire` becomes `kernel/wire`, as the target document says. The generated public
  clients (`pkg/client/*`, through `x-go-type-import` in `api/src`) then import those two
  internal packages and nothing else.
- **The published `api` surface is today's cross-module calls only.**
  - `access/api`: Authenticator, Authorizer and Decider, Tokens, Keys, Identity,
    Restrictions, Projects, Grants, AnonymousAccess, Settings and the permission
    vocabulary, with Keypair, Certificate, Repo and Subject. This is wider than the target
    document. `Decider` is an interface with the methods artifacts calls.
  - `artifacts/api`: `repo`, Repositories and Layouts. It has no Settings.
  - `configuration/api`: Descriptor, System and Settings.
  - An `api` package imports only `kernel`, `platform/outbox`, the standard library and its
    own module's `api` subpackages, and declares its own data types.
  - Interfaces the target document lists without a current cross-module consumer (Items,
    Packages, Builds, Search, Gate, events) are named in the owning `api` package's doc.go
    Status, not created.

### Persistence

- **Per-module migration streams replace the global history.** Each module has baseline
  migrations per dialect, recorded in `schema_migrations(module, version, ...)` and run in
  the canonical module order: platform, artifacts, access, notifications, retention,
  federation, releases, workers. A database whose history predates this is refused with
  `ErrLegacyHistory` before any write.
- **No cross-module foreign keys.** A cascade one would have provided becomes an explicit
  call inside the owning transaction.
- **One unit of work for every module store:** one `*sql.DB` with `platform/sql/dialect`,
  and one in-memory database with `platform/tx/memtx`.
- **Platform-owned adapters** live in `platform/store/{sql,inmem,storetest}`, with the
  platform stream in `platform/store/sql/migrations`. The target document's `platform/sql`
  is the dialect layer.
- **The composite store** (every module store over the one unit of work) lives in
  `app/adapters/{sql,inmem}`, with its contract suite in `app/storetest` and test openers
  in `app/apptest`. Module tests reach it only through those test-support packages, and
  `formattest.Run` takes its backends as a parameter, so artifacts core never imports
  `app`.
- **Contract suites.** Module SQL contract suites run on SQLite; PostgreSQL runs stay in
  the composite packages, with one container per test process.
- **Ownership of shared models.**
  - `PackageType` and the version comparators stay out of the kernel. They live in
    `artifacts/api/repo` and `artifacts/core/model`.
  - The kernel imports only the standard library and `platform/httpkit/apierror`.
  - The account models of `pkg/types/principal.go` (`User`, `Group`, their statuses,
    `RoleFlags`) go to `access/core/model`, not to the kernel as the target document maps
    them. The kernel holds only the request principal.
  - `ConfigStore` and `SettingsStore` live in `platform/configdoc` and `platform/settings`,
    not in `configuration/core/port`.
  - `archived_items` and `ArchivedItem` belong to artifacts. The cold blob tables stay
    artifacts-owned and retention reaches them through accessors.
- **Layout fencing** becomes a `configdoc.Watch` registered by the composition, inside the
  configuration write's transaction.
- **Narrowing.** Staged code may import a legacy split package, such as `pkg/store` or
  `pkg/types`, until the narrowing to the consumer's own interfaces removes the import.
  The layout test logs such imports instead of failing them.

### Composition and HTTP

- **Manual wiring.** One `New(Deps)` constructor per module, wired in `app`, with no
  dependency-injection library. Constructors do no I/O. Skeleton modules get no
  constructor until the phase that implements them.
- **The router** becomes `app.RouterConfig` and `app.NewRouter`. Per-module settings are
  accessor methods on `app/config.Config`, so the YAML shape and the `config validate`
  output are unchanged.
- **Partial strict servers.** Each module implements part of a generated strict server
  interface. The partials are embedded in the app composite at depth 1 and the generated
  `UnimplementedStrict` stubs at depth 2, with one compile-time assertion per
  specification. Ecosystem partials sit at depth 1. `GetAccessVersion` belongs to access;
  `CalculateYumRepositoryMetadata` joins the RPM partial.
- **The per-request decider** is cached in the request context and bound to the
  `WithPrincipal` call (`kernel.PrincipalBinding`), so the kernel `Principal` is a plain
  value. The `CredentialReference` JSON is pinned by a golden test.

### Artifacts and remote

- **The format registry is injected:** `type Registry func(repo.PackageType) Handler` in
  `core/format`, which breaks the cycle from `core/item` to the ecosystem slices.
  `formattest` may import the registry.
- **No `artifacts/adapters/sql/aqlsql`.** The SQL compiler stays in `core/aql`.
- **Remote is split fully, as the target document says.** Transport goes to
  `artifacts/adapters/upstream` behind a new `Upstream` port; Smart Remote becomes
  `artifacts/core/remote/smart` and keeps its `Smart*` names; the Docker registry rules
  go to `ecosystem/docker` and the NuGet empty feed to `ecosystem/nuget`;
  `module_location.go` stays in core. In detail:
  - `Upstream` and `Registries` live in `core/remote`, not `core/port`.
  - `protocol.go`, `listing*.go` and `smart_peer.go` stay in core; `url.go` and
    `destination.go` are split; `registry.go` and the challenge and token files go to the
    adapter.
  - New hooks: `Outbound`, `WithSmart`, `Interceptor`, `ProjectResponse`, `GitFetch`
    (CocoaPods Git fetching) and Smart Remote's registry-path parser. New package
    `upstreamtest`. The core remote tests are an external test package.
  - `core/remote/source` keeps calling `generic.CheckDownload`. The layout rules allow it,
    since core is barred only from `adapters/*` and `ecosystem/*/http`, and it forms no
    cycle.

### Package placement and names

- **Placement that differs from the target document.** Pattern helpers go to
  `kernel/antpath` and `artifacts/api/repo`; `keymaterial` goes to `platform/keymaterial`;
  `idtoken` stays a separate package, `access/core/idtoken`, with `idtokentest` below it;
  the legacy layout test package is deleted and the staged `layout` package replaces it.
- **New packages the target document does not name:** `artifacts/adapters/http/itemkit`,
  `test/server/support/harness`, `platform/testkit/{repopath,storekit,fault}`,
  `platform/idempotency`, `platform/apilimit`, `platform/archivecheck`, `kernel/antpath`
  and `access/accesstest`.
- **Package names that change:**
  - `auth` becomes `authn` (`access/core/authn`);
  - `testfault` becomes `fault` (`platform/testkit/fault`);
  - `formats` merges into `artifacts/ecosystem`, where `formats.go` becomes
    `registry.go`;
  - each protocol package becomes `<type>http` at `artifacts/ecosystem/<type>/http`, so it
    does not shadow `net/http`;
  - the module HTTP adapters are `accesshttp`, `artifactshttp` and `configurationhttp`.
- **`idtoken` keeps its name** because it and `oidc` both declare `Provider`
  (`internal/auth/idtoken/idtoken.go:114`, `internal/service/oidc/oidc.go:104`).

### Names the migration introduces

Exported names that a migration change adds, or that it declares in a module's `api`
package for other modules to share, where the target document does not name them. The
change that adds a name appends its row; the change column gives the ticket of the
[P9d backlog](https://github.com/weaveplatform/artifactweave/issues/935).

| Name | Package | What it is | Change |
| --- | --- | --- | --- |
| `TokenRequest` | `access/api` | What `Tokens.Issue` takes: every field of the token service's request, with kernel types | ACC-01 |
| `RegistryTokenRequest` | `access/api` | What `Tokens.IssueRegistry` takes: every field of the token service's registry request | ACC-01 |
| `IssuedToken` | `access/api` | A new token's secrets, lifetime, id and issue time, which callers read from the token service's result | ACC-01 |
| `UserInfo` | `access/api` | What other modules read about a user account, with `Enabled` | ACC-01 |
| `PathPair` | `access/api` | A path and the path its content takes after a move, which `Grants.AccessChange` compares | ACC-01 |
| `ErrScanLimit` | `access/api` | A permission scan that found more permissions than its limit | ACC-01 |
| `Restrictions` | `access/api` | Checks a user's temporary API restriction; not in the target document | ACC-01 |
| `Projects` | `access/api` | Reads project environments, membership and existence; the target document plans a fuller `Projects` for P11 | ACC-01 |
| `Grants` | `access/api` | Scans permissions about content: who holds what on an item, and whether a move changes who may reach it; not in the target document | ACC-01 |
| `AnonymousAccess` | `access/api` | Reads and switches the anonymous access setting; not in the target document | ACC-01 |
| `AsAPI`, `FromAPI` | `access/core/authz` | Convert between the evaluator's `*Decider` and `access/api`'s `Decider`; `AsAPI(nil)` is a nil interface, never a typed nil | ACC-01 |
| `Pattern`, `CompilePattern` | `kernel/antpath` | Ant-style path matching that access permission targets and artifacts repository patterns share: a kernel package with matching behaviour, where the target document keeps it in `access/core/authz` | ACC-04 |
| `RepoPatterns`, `PatternsOf` | `artifacts/api/repo` | A repository's compiled include and exclude patterns, which AQL, listings, searches and the generic and Maven formats check paths with; in `pkg/repo` until ART-01 moves that package, where the target document keeps them in `access/core/authz` | ACC-04 |
| `CheckPair`, `RSAPrivateKey`, `Ed25519PrivateKey`, `Ed25519PublicKey`, `ParseBundle`, `Bundle`, `ErrInvalid` | `platform/keymaterial` | Key pair and certificate checks that access's key pair API and the Alpine, Hex and Nix formats share, where the target document places the package in `access/core/keymaterial` | ACC-04 |
| `PrincipalBinding` | `kernel` | Identifies the `WithPrincipal` call that set a context's principal, so access binds the request's permission decider to that call and `Principal` stays a plain value | PLT-05 |
| `Principal.PasswordRevision`, `Principal.IsTokenAuthenticated`, `Principal.TokenSubject` | `kernel` | Fields that access sets when it verifies a credential and trusts when it rechecks one; exported only because the kernel declares `Principal`, and `TestUnitPrincipalInternals` keeps every package but access and the kernel off them | PLT-05 |
| `Authenticator` | `access/api` | Verifies a password or a token, reads the anonymous access switch, and rechecks a verified credential against current state (`Current`, `CurrentRegistry`, `RestoreDownload`); the target document lists `Basic`, `Bearer`, `APIKey` and `Anonymous` | ACC-05 |
| `Authorizer` | `access/api` | Evaluates permissions: `Authorize` once for a request, `For` for a principal the request did not authorise, `Decide` for a subject that no request authenticated; the target document gives it `For` alone | ACC-05 |
| `WithCaller`, `WithDecider`, `DeciderFrom` | `access/api` | Carry a request's principal and its permission decider in the context, the decider bound to the principal's entry (`kernel.PrincipalBinding`), so that `Principal` stays a plain value; `internal/auth`'s `WithAuthorized` and `DeciderFrom` forward to them | ACC-05 |
| `SubjectOf` | `access/api` | The subject a permission evaluation starts from for a principal, which other modules put in remote read requests; moved from `internal/auth` | ACC-05 |
| `Deps` | `access` | What app passes `access.New`: the whole store, the legacy authenticator and token service app builds over it, and the access settings; the target document's `Deps` names the platform's transaction, SQL, keyring, work and outbox, which the module takes once access core builds its own services and ACC-14 narrows the store | ACC-05 |
| `Module` | `access` | The access module as app holds it; `API()` returns one binding of each `access/api` contract over the legacy services, and the target document's `Routes()` and `Start()` come later | ACC-05 |
| `KeysOf`, `ProjectsOf`, `AnonymousAccessOf` | `access` | Store-only bindings of `Keys`, `Projects` and `AnonymousAccess`, for code such as bootstrap and reseal that holds a store before any module exists | ACC-05 |
| `accessapitest` | `access/api/accessapitest` | Hand-written fakes of every `access/api` contract for other modules' tests; a test-support package below `api`, which imports only `api`, `kernel` and the standard library | ACC-05 |
| `Registry` | `artifacts/core/format` | A package type's format `Handler`, which the item, build and registry services and the Docker registry take as a value so that core never imports the ecosystem slices; app builds it from the registry's `Settings.For`, and nil means the generic handler. The target document names only `ecosystem/registry.go`'s `Formats()` | ART-04 |
| `Options.Formats` | `artifacts/ecosystem/docker/http` | The `format.Registry` that the Docker registry's uploads, manifest pushes and mounts write through; nil means the generic handler, which indexes no image or tag | ART-04 |
| `BuildRuns` | `artifacts/core/aql` | The build-run lookups `AQLSource` reads related runs through: the runs that hold a file as an artifact or as a dependency, a run by ID and a run's promotions, with the build store's signatures. `AQLSource` takes it and `ItemSearch` in place of the store aggregate, whose package imports `core/aql`; the target document puts the source in `core/port`'s `store.go` | ART-15 |
| `ItemSearch` | `artifacts/core/aql` | The item store's AQL search, which `AQLSource` reads nested files through, so that they carry their properties and statistics as a query's own rows do | ART-15 |
| `Store` | `platform/work` | What `work.New` takes: the queue table (`WorkQueue()`) and `AfterCommit`, through which `Enqueue` wakes the node's workers once the caller's transaction commits. The metadata store satisfies it, so the queue imports no store; the target document names the package's files, not this port | PLT-14 |
| `BlobIndex` | `platform/blob/mirror` | The one blob index method the mirror's repair worker calls, `Get`, for a blob's reference count and recorded digests. The mirror declares it so that the platform imports no module, and the metadata store's `BlobIndex` satisfies it; the target document names `BlobIndex` only as the artifacts module's port in `core/port` | PLT-15 |
| `ReplicaIndex` | `platform/blob/mirror` | The eight replica index methods the mirror calls (`Put`, `ForBlob`, `List`, `Delete`, `DeleteBlob`, `Counts`, `UnderReplicated` and `SoleCopies`), without `Get`; the metadata store's `ReplicaIndex` satisfies it, and the target document names `ReplicaIndex` only as the artifacts module's port in `core/port` | PLT-15 |
| `BlobReplica`, `ReplicaState` with `ReplicaPresent`, `ReplicaPending`, `ReplicaDeleting` and `ReplicaFailed`, `ReplicaFilter`, `ReplicaCount` | `platform/blob/mirror` | The replica rows that `ReplicaIndex` takes and returns, declared beside it so that the mirror imports no store; the shared types and the metadata store alias them until PRO-02. The target document keeps them in the artifacts module's `core/model` and `core/port` | PLT-15 |
| `Blob`, `BlobState` with `BlobLive`, `BlobDeleting` and `BlobDeleted` | `platform/blob` | The blob index row that `BlobIndex.Get` returns: checksums, size, reference count and garbage-collection state. Declared in the platform so that the mirror imports no store; the shared types alias it until PRO-02. The target document keeps it in the artifacts module's `core/model` | PLT-15 |

### Placeholders

Every package the target document marks for a later phase exists from the start as a
placeholder: a directory with `doc.go` only, and no code or API until that phase. Its
Status reads `Placeholder: implemented by Pnn.`, naming the implementing phase. The
`access`, `artifacts` and `configuration` module roots are placeholders until the
migration adds their constructors, and have no ledger entry. A change that adds any file
other than `doc.go` to a placeholder directory removes the placeholder's ledger entry, if
it has one, and rewrites its Status in the same change; `TestUnitLedgerComplete` and
`TestUnitPackageDocs` fail otherwise. A placeholder is judged by its own directory;
subdirectories do not count.

### Dependency rules

`TestUnitImportTiers` checks every import of a staged package; the first matching rule
wins:

1. Test files may import any test-support package (`inmem`, `storetest`,
   `platform/testkit/**`, `fakeupstream`, `*test`, `test/**`), across modules.
2. `pkg/client` may import only `kernel/wire` and `artifacts/api/repo` among internal
   packages. `platform/gen/**` may import those two as a named exception to rule 3,
   through `x-go-type-import`.
3. `platform` and `kernel` import no module, `gateway` or `app`.
4. `gateway` imports only `<module>/api`, `kernel` and `platform/httpkit`.
5. `<module>/api` imports only `kernel`, `platform/outbox` and its own `api` subpackages.
6. A module imports another module only through `<other>/api`.
7. No module package imports a module root; only `app` wires modules. A module's test kit
   `<m>/<m>test` may import its own root `<m>` as a named exception: it is test support
   and forms no cycle.
8. A module's `core` never imports its own `adapters` or any `ecosystem/*/http`.
9. `ecosystem/<type>/http` is imported, by non-test files, only by its own `http` subtree,
   `artifacts/adapters/http` and `app`.

A legacy import is judged by its ledger target. An import of a legacy package marked
`split` is logged, not failed, until that package is emptied. A top-level directory under
`migration/` outside the fixed set (`kernel`, `platform`, `gateway`, `app`, `layout`,
`devtools` and the thirteen modules: `artifacts`, `access`, `configuration`,
`notifications`, `retention`, `federation`, `releases`, `workers`, `evidence`,
`governance`, `security`, `operations`, `estate`) fails. A self-test proves that every
rule can fail. There is no depguard configuration.

### Documentation standard

- **One `doc.go` per package**, holding the only package comment. Required headings
  `# Role`, `# Inputs`, `# Outputs`, `# References` and `# Status`; optional
  `# Invariants` and `# Testing`. The first sentence starts `Package <name>`
  (`Command <name>` for main packages). Role names the module and layer (api, core,
  adapter, ecosystem, platform, kernel, app, devtools, gateway, test support).
  References cite decisions, research, plan anchors, issues and JFrog documentation
  URLs; JFrog material is cited, never copied in. Status gives the implemented phases or
  the placeholder line. Generated packages get a hand-written `doc.go`; package comments
  in `*.gen.go` files are ignored. `TestUnitPackageDocs` enforces the standard for every
  package outside the legacy roots.
- **Function comments state what and why.** Every exported identifier has a doc comment
  that starts with its name and states the contract: behaviour, inputs, outputs, errors,
  and transaction and concurrency expectations. It adds why when the design is not the
  obvious one (JFrog compatibility, a decision record, a protocol quirk, locking or
  performance, a silent-failure risk) and cites the source. Unexported functions get a
  comment only when the reasoning cannot be recovered from the code. Comments that
  restate code are deleted, and comments carry no history such as "moved from".
- **Lint enforcement.** golangci-lint's revive runs `exported`, with its stuttering check
  off because the migration renames nothing, and `package-comments`. Both are excluded for
  the legacy roots by path, so the exclusion shrinks as packages leave them. Revive is the
  only linter that also reports outside `internal/` and `migration/`; widening the other
  linters would surface about 126 unrelated findings. Revive's `checkPublicInterface`
  stays off.
- **Guidance.** `CLAUDE.md` is unchanged, since its layout rule stays true.
  [`migration/README.md`](../../../migration/README.md) and a banner in
  [`repo-layout.md`](../../implementation/repo-layout.md) carry the migration guidance.

### Type-alias bridges

A declaration that leaves a legacy split package, such as `pkg/types`, `pkg/store`,
`internal/auth` or `internal/authz`, leaves an alias at its old path while its users are
re-pointed: one `bridge_<target>.go` per target, headed by a comment that names the
declaring package and the change that deletes the bridge (the exact form is in
[`migration/README.md`](../../../migration/README.md)), and never marked `Deprecated:`.
Sentinel errors bridge as `var ErrNotFound = kernel.ErrNotFound`, the same pointer, so
`errors.Is` keeps matching; generic functions bridge as wrappers. A `package X` test that
would form an import cycle becomes `package X_test` with an `export_test.go`. Every user
is re-pointed to the declaring package and the bridges are deleted before promotion.

## Rationale

- **A separate staging root.** A path names either a legacy or a staged package, so the
  ledger's derived status, the move tool and the layout test can tell the two apart from
  the path alone. Each staged package already has its final relative path, so promotion is
  one rename of the root. `internal/app` and `internal/layout`, which are both legacy and
  final paths, never collide while both trees exist.
- **Status derived from the tree.** If tickets recorded progress in the ledger, parallel
  pull requests would conflict on it and the record could drift from the code. The tree
  is the truth; the ledger holds only intent. One entry per line keeps the conflicts of
  entries added in parallel on their own lines.
- **Type-alias bridges** follow Go's gradual code repair: a declaration moves once while
  hundreds of call sites keep compiling, and the users move in later changes. They are
  never marked `Deprecated:` because staticcheck SA1019 would then fail lint at every
  remaining call site.
- **Behaviour preservation** keeps the existing status tables, route and principal
  matrices and acceptance features valid as the guard for every move: they must pass
  without edits to their expectations.
- **Per-module streams and the history refusal.** A module owns its tables, so it owns
  their migrations; a global sequence would couple every module's schema history. There
  is no upgrade path before the maintainer asks for one, so an old history is refused
  before any write instead of being half migrated.
- **No cross-module foreign keys** keep each module's tables its own; the owning
  transaction makes the cascade visible in code.
- **Manual wiring** keeps construction explicit and compile-checked. Constructors without
  I/O keep tests cheap and make start-up failures come from one place.
- **A wider `access/api`.** Artifacts already calls restrictions, projects, grants,
  anonymous access and key material across the boundary; publishing exactly those calls
  lets artifacts reach access only through `access/api` without inventing new seams.
- **The decider bound to the principal** keeps the kernel principal a plain value with no
  access types, while a request still builds its decider once. Because the decider is
  bound to its `WithPrincipal` call, it never answers for a principal that another call
  stored.
- **Shared stores in the platform.** Three modules read configuration documents and
  settings, and configuration is their one writer; a configuration port would make the
  readers depend on the configuration module.
- **Account models in access.** The kernel holds only values every module shares; users
  and groups are access's model, and only the request principal crosses modules.
- **`keymaterial` in the platform.** The Alpine, Hex and Nix ecosystem slices parse
  signing keys with it, so an access location would make those slices import access
  internals.
- **No `aqlsql`.** The SQL compiler is a method of the parsed query and works on the
  package's unexported join plan, which the in-memory evaluator shares; a separate adapter
  package would have to export that plan for a single caller.
- **The injected format registry** lets `core/item` call format hooks without importing the
  ecosystem slices that themselves import core.
- **`<type>http` package names** avoid shadowing `net/http` in every protocol package.
- **No depguard.** It would duplicate the layout test and cannot express module-relative
  rules such as "only through `<other>/api`".
- **Placeholders from the start** make every roadmap package visible with its owning
  phase, so the layout test can treat the top-level set as fixed and a phase finds its
  package already documented.
- **The documentation standard** makes each package state its role, inputs, outputs and
  sources where a reader looks first, and the lint exclusion by legacy root ratchets the
  standard as packages move.

Nothing in this decision departs from Artifactory's behaviour, so it has no entry in the
[deviations register](../../implementation/deviations.md).

## Constraints

- **Lint exceptions match package names.** wrapcheck's `extra-ignore-sigs` in
  `.golangci.yml` (`apierror.New(`, `format.Rejectf(`) match by package name, so the
  `apierror` and `format` packages keep their names.
- **Path length.** The longest tracked path is 129 characters (an RPM fixture under
  `test/acceptance/testdata/rpm-upstream`). Staging adds 14 characters to a format
  package's testdata paths, and the longest path under the ledger's package targets is 112
  characters (a Nix fixture), so staging stays within today's longest path. This matters
  for the Windows unit job.
- **No upgrade path.** Databases created by v0.1.x or v0.2.x must be recreated. The release
  notes of the first release that refuses them say so.
- **Coverage.** The merged gate (95% total, 90% per package, across unit runs on every
  operating system and the acceptance runs) stays in force for every intermediate tree.
  With about three statements of headroom, new code aims for 97% or more.
- **Canonical files on Windows.** `.gitattributes` checks JSON out with LF line endings,
  which keeps the byte comparison of the canonical ledger valid on every operating
  system.

## Verification

- `TestUnitLedgerComplete` (`migration/layout`): the ledger validates, is canonical, has a
  package entry for every legacy package directory, has no broken entry, and every
  placeholder holds `doc.go` only. `TestUnitLedgerCheckDetectsViolations` proves each of
  these checks can fail, and the `migration/devtools/ledger` tests prove each validation
  rule.
- `TestUnitImportTiers`: the dependency rules above, with its self-test.
- `TestUnitPackageDocs`: the documentation standard for every package outside the legacy
  roots, with its self-test.
- The coverage gate in `.testcoverage.yml`, merged across the unit and acceptance runs.

## References

- Target layout:
  [`5-modular-monolith.md`](../architecture-options/5-modular-monolith.md) (dependency
  rules, repository root and package mapping), and the other option documents in
  [`docs/research/architecture-options/`](../architecture-options/).
- [Implementation plan, modular-monolith migration section](../../implementation/plan.md#p9d-modular-monolith-migration),
  and [`migration/README.md`](../../../migration/README.md).
- Decisions that stay in force and that the moves preserve:
  [0002](0002-spec-first-flat-crud-handwritten-item-paths.md) (generated strict servers and
  handwritten item paths), [0004](0004-storage-sql-metadata-and-blobs.md) (SQL metadata and
  blobs), [0005](0005-authentication-and-tokens.md) (authentication and tokens),
  [0006](0006-authorization-native-permission-model.md) (authorisation),
  [0007](0007-remote-repositories.md) (remote repositories),
  [0012](0012-search-and-aql.md) (search and AQL),
  [0015](0015-testing-and-acceptance.md) (test kinds, contract suites and the coverage
  gate), [0017](0017-sealed-columns-and-master-key.md) (sealed columns and master key),
  [0020](0020-domain-model.md) (domain model),
  [0032](0032-package-format-handlers.md) (format handlers and their registry),
  [0033](0033-persistent-work-queue.md) (the work queue and its metrics) and
  [0043](0043-api-reliability.md) (API retry safety and bounded execution).
- Go's gradual code repair with type aliases:
  <https://go.dev/talks/2016/refactor.article>.
