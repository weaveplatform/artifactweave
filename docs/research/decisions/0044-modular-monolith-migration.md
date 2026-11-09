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
- **Reseal follows the canonical order.** Each module registers the sealed columns its store
  writes in one `dialect.Module`, with a hook that runs before them, and `keys rotate`
  re-seals the modules in the canonical order. That deliberately changes the order of the
  single list the SQL stores kept before: the platform columns (`config_secrets`, `instance`)
  now come first, the repository header backfill runs after them, and `webhook_secrets`
  comes before `replications`. The change cannot be observed: `keys rotate` prints only
  totals, and re-sealing is idempotent, so every value ends under the active key whatever
  the order. Only when several values fail to open can the error name a different one first.
- **No cross-module foreign keys.** A cascade one would have provided becomes an explicit
  call inside the owning transaction.
- **One unit of work for every module store:** one `*sql.DB` with `platform/sql/dialect`,
  and one in-memory database with `platform/tx/memtx`.
- **Platform-owned adapters** live in `platform/store/{sql,inmem,storetest}`, with the
  platform stream in `platform/store/sql/migrations`. The target document's `platform/sql`
  is the dialect layer and the engine drivers.
- **Engine drivers.** `platform/sql/sqlite` and `platform/sql/postgres` open a pool for
  their engine (`OpenDB`) and describe it as a `dialect.Dialect`, with no store,
  migration or keyring knowledge. Opening the composite store (the driver's pool, migrated
  and wrapped in the SQL store) stays legacy, in the `Open` of `internal/sqlstore/sqlite`
  and `internal/sqlstore/postgres`, with its signature, options and error texts, until
  STO-19 moves it into `app`.
- **The composite store** (every module store over the one unit of work) lives in
  `app/adapters/{sql,inmem}`, with its contract suite in `app/storetest` and test openers
  in `app/apptest`. Module tests reach it only through those test-support packages, and
  `formattest.Run` takes its backends as a parameter, so artifacts core never imports
  `app`.
- **Contract suites.** Module SQL contract suites run on SQLite through
  `platform/sql/sqltest`'s `RunModule`, which gives every store a fresh database migrated
  with the module's streams alone, and they share their assertions through
  `platform/testkit/storekit`. PostgreSQL runs stay in the composite packages, with one
  container per test process, and stay legacy, in `internal/sqlstore/postgres` and the
  composite suites of `internal/sqlstore/sqltest`, until STO-19. A module suite that asks
  for a PostgreSQL run (`WithPostgres`) gets a scratch schema of its own on the shared
  server for each store.
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
  the legacy layout test package is deleted and the staged `layout` package replaces it;
  the authentication suite, all that the server test package `shared` holds, becomes the
  access module's suite package `test/server/access`, where the target document folds
  `shared` into `test/server/support`.
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
| `accesstest` | `access/accesstest` | The real access module over a test's store, built as `app` builds it, for other modules' tests: `New` and `Over` return a `Kit` (`Store`, `API`, `Authorize`, `Decide`, `Middleware` and `Verify`), configured by the `Option`s `WithNow`, `WithTokens`, `WithTokenVerifier` and `WithSettings`, beside `HashPassword` and `ErrLoginSuspended`. A module test kit the target document does not list, which imports its own module root under dependency rule 7's named exception | ACC-15 |
| `Registry` | `artifacts/core/format` | A package type's format `Handler`, which the item, build and registry services and the Docker registry take as a value so that core never imports the ecosystem slices; app builds it from the registry's `Settings.For`, and nil means the generic handler. The target document names only `ecosystem/registry.go`'s `Formats()` | ART-04 |
| `Options.Formats` | `artifacts/ecosystem/docker/http` | The `format.Registry` that the Docker registry's uploads, manifest pushes and mounts write through; nil means the generic handler, which indexes no image or tag | ART-04 |
| `BuildRuns` | `artifacts/core/aql` | The build-run lookups `AQLSource` reads related runs through: the runs that hold a file as an artifact or as a dependency, a run by ID and a run's promotions, with the build store's signatures. `AQLSource` takes it and `ItemSearch` in place of the store aggregate, whose package imports `core/aql`; the target document puts the source in `core/port`'s `store.go` | ART-15 |
| `ItemSearch` | `artifacts/core/aql` | The item store's AQL search, which `AQLSource` reads nested files through, so that they carry their properties and statistics as a query's own rows do | ART-15 |
| `Store` | `platform/work` | What `work.New` takes: the queue table (`WorkQueue()`) and `AfterCommit`, through which `Enqueue` wakes the node's workers once the caller's transaction commits. The metadata store satisfies it, so the queue imports no store; the target document names the package's files, not this port | PLT-14 |
| `BlobIndex` | `platform/blob/mirror` | The one blob index method the mirror's repair worker calls, `Get`, for a blob's reference count and recorded digests. The mirror declares it so that the platform imports no module, and the metadata store's `BlobIndex` satisfies it; the target document names `BlobIndex` only as the artifacts module's port in `core/port` | PLT-15 |
| `ReplicaIndex` | `platform/blob/mirror` | The eight replica index methods the mirror calls (`Put`, `ForBlob`, `List`, `Delete`, `DeleteBlob`, `Counts`, `UnderReplicated` and `SoleCopies`), without `Get`; the metadata store's `ReplicaIndex` satisfies it, and the target document names `ReplicaIndex` only as the artifacts module's port in `core/port` | PLT-15 |
| `BlobReplica`, `ReplicaState` with `ReplicaPresent`, `ReplicaPending`, `ReplicaDeleting` and `ReplicaFailed`, `ReplicaFilter`, `ReplicaCount` | `platform/blob/mirror` | The replica rows that `ReplicaIndex` takes and returns, declared beside it so that the mirror imports no store; the shared types and the metadata store alias them until PRO-02. The target document keeps them in the artifacts module's `core/model` and `core/port` | PLT-15 |
| `Blob`, `BlobState` with `BlobLive`, `BlobDeleting` and `BlobDeleted` | `platform/blob` | The blob index row that `BlobIndex.Get` returns: checksums, size, reference count and garbage-collection state. Declared in the platform so that the mirror imports no store; the shared types alias it until PRO-02. The target document keeps it in the artifacts module's `core/model` | PLT-15 |
| `Store` | `platform/apilimit` | What `apilimit.New` takes: `RequestBudgets()`, the shared request budgets that `Authentication` and `Admit` charge. The metadata store satisfies it, so admission imports no store; the target document names neither the package nor this port | PLT-16 |
| `RequestBudgetStore`, `RequestBudget`, `BudgetRequest`, `BudgetBalance`, `BudgetDecision`, `ErrBudgetCapacity` | `platform/apilimit` | The request budget port that admission charges through, with its values and its capacity sentinel, declared beside the admission service so that it imports no store; the metadata store aliases them until PRO-02, and its SQL and in-memory stores implement the port. The target document does not name them; its mapping splits the store's ports among the modules' `core/port/store.go` | PLT-16 |
| `Store` | `platform/instance` | What `EnsureInstance` takes: `Instance()`, the identity store. The metadata store satisfies it, so the package imports no store; the target document names the package, not this port | PLT-17 |
| `EnsureInstance` | `platform/instance` | Creates this instance's identity on first start and keeps a stored one; a `kernel.ErrConflict` from `Init`, another instance's identity stored first, is success. The composition root calls it before it builds the token service; the target document maps the composition root's instance file to the package without naming the function | PLT-17 |
| `InstanceIdentity`, `ValidateInstanceID`, `ErrInvalidInstanceID`, `ProtocolVersion`, `InstanceStore` | `platform/instance` | This instance's identity, the check of its `jfrt@<uuid>` id, the peer protocol version and the port the identity is stored through, declared beside `EnsureInstance` so that the package imports no store; the shared types and the metadata store alias them until PRO-02. The target document keeps `InstanceStore` and the peer types in the federation module's `core/port`, but access, the system endpoints and the health probes read the identity too | PLT-17 |
| `SettingsStore` and the twelve `Setting*` keys | `platform/settings` | The singleton setting keys, whose strings are stored, and the port that stores their JSON values; the shared types and the metadata store alias them until PRO-02. The target document puts `SettingsStore` in the configuration module's `core/port`, a deviation that "Ownership of shared models" records, and does not name the keys | PLT-17 |
| `ConfigStore`, `ConfigDocument` and `ConfigKind` with its sixteen `Config*` kinds | `platform/configdoc` | Configuration documents stored and returned whole, their kinds and the port they are stored through; the shared types and the metadata store alias them until PRO-02. The target document puts `ConfigStore` in the configuration module's `core/port`, a deviation that "Ownership of shared models" records, and does not name the document or its kinds | PLT-17 |
| `ReplayStore`, `ReplayRequest`, `ReplayReceipt`, `ErrReplayMismatch` | `platform/idempotency` | The port through which a protected write runs its effect at most once per unexpired idempotency key, with its request of digests, its receipt and its mismatch sentinel, declared apart from any store so that the platform imports no module; the metadata store aliases them until PRO-02, and its SQL and in-memory stores implement the port. The target document does not name them; its mapping splits the store's ports among the modules' `core/port/store.go`, and its `platform/events/idempotency.go` is consumer-side event dedupe, a different concern | PLT-18 |
| `HLC`, `ParseHLC`, `ErrInvalidHLC`, `OutboxEntry`, `PeerCursor`, `AppliedMarker`, `FailedBlobFetch` | `platform/outbox` | The hybrid logical clock, the parser of its text form with that parser's sentinel, and the values `OutboxStore` takes and returns: an outbox entry, a peer's acknowledged position, the receiver's applied position and a failed blob fetch. Declared beside the port so that the platform imports no module; the shared types alias them until PRO-02. The target document keeps the peer types in the federation module's `core/port` and names only P11's `Event` and `Append` in `platform/outbox` | PLT-18 |
| `Store`, `New` | `platform/tx/memtx` | The in-memory database: named tables and sequences behind one cancellable gate, with `Tx` and `AfterCommit` (it implements `platform/tx`'s `Runner`) and a `Close` that does nothing. Every in-memory store built over one `Store` shares its transactions. The target document names `platform/tx` and leaves this in-memory half implicit | STO-03 |
| `Do`, `InTx`, `NextID` | `platform/tx/memtx` | A call made with the gate held or inside the context's transaction, the check whether a context is inside the store's `Tx`, and the next number of a sequence: the metadata store's in-memory store had the first and last as private methods and made the check by hand, and every module's in-memory adapter needs them | STO-03 |
| `Table` with `Rows`, `Get`, `Has`, `Put`, `Del`, `All` and `DeleteWhere`, and `TableOf` | `platform/tx/memtx` | A named in-memory table that copies rows in and out and normalises them on the way in, with its map for the stores that range over it, store a value `kernel.Normalise` cannot copy or delete while they range, and the lookup by name that every use goes through | STO-03 |
| `Paginate`, `KeyStrings`, `CompareKey` | `platform/tx/memtx` | Keyset paging of sorted rows by the same string and integer key parts as the SQL stores, so a cursor means the same position on either | STO-03 |
| `NotFound`, `Conflict` | `platform/tx/memtx` | Errors that wrap `kernel.ErrNotFound` and `kernel.ErrConflict` with what was looked up or what collided, in the text the in-memory store has always returned | STO-03 |
| `Store`, `New` | `platform/sql/dialect` | The handle every SQL store is built over: one pool, its `Dialect` and the keyring, with the unit of work (`Tx` and `AfterCommit`), `DB`, `Dialect` and `Close`. Every store built over one `Store` shares its transactions. The target document maps the SQL stores' `store.go` to the package without naming them | STO-04 |
| `InTx` | `platform/sql/dialect` | Whether a context carries a transaction on the store's pool: the builds store made the check through the unexported `inTx`, which the target document names, and a module's SQL adapter outside the package needs it | STO-04 |
| `Q`, `Wrap`, `Exec`, `ExecOne`, `InsertID`, `Exists`, `Rows`, `LockClause` | `platform/sql/dialect` | The statement helpers of `Store` that every SQL adapter calls: the pool or the context's transaction, driver errors mapped onto the kernel sentinels and named with the dialect and the operation, a statement meant to change one row, an insert that returns its id, an existence check, a loop over rows, and the row-lock suffix inside a transaction | STO-04 |
| `Seal`, `Unseal` | `platform/sql/dialect` | Sealed columns through the store's keyring, which stays unexported (decision 0017): seal and open one column value | STO-04 |
| `Micros`, `FromMicros`, `Millis`, `FromMillis`, `ToJSON`, `MustJSON`, `FromJSON`, `RawJSON`, `FromRaw`, `NullString`, `EntBytes` | `platform/sql/dialect` | The column encodings every SQL adapter keeps: times as Unix microseconds, durations as milliseconds, documents as JSON text and empty values as NULL | STO-04 |
| `Keyset`, `OrderBy`, `Itoa`, `LimitOr` | `platform/sql/dialect` | Keyset paging over `kernel.Page`: the condition that continues a list after a cursor, its `ORDER BY`, an integer cursor part, and the default and maximum list limits, so that a cursor means the same position in every SQL adapter | STO-04 |
| `EntRow`, `PeersScanner`, `OpsCols` | `platform/sql/dialect` | A `*sql.Row` or `*sql.Rows` to scan, under each of the two names the SQL stores' files use, and the split of a column list written as `a, b, c` | STO-04 |
| `Upsert`, `InsertIgnore`, `Insert`, `Placeholders` | `platform/sql/dialect` | Statement text that SQLite and PostgreSQL share: an insert that updates the row whose key is taken, one that keeps the row already there, a plain insert and a list of placeholders, all with `?` placeholders that `Dialect.Rebind` converts for PostgreSQL. The target document names their file, `dialect.go`, but not them | STO-04 |
| `Migration`, `LoadMigrations`, `Migrate`, `Rollback`, `Version`, `MustSub` | `platform/sql/dialect` | The migrator over a dialect's embedded `NNNN_name.sql` files: a parsed file and the parse of a directory, applying and reverting migrations each in one transaction with the `schema_migrations` row that records it, the applied and the latest version, and the sub-tree a driver gives its `Dialect`. The target document names `migrate.go` only for the per-module streams that STO-06 adds | STO-04 |
| `ErrTransaction`, `ErrMigration`, `ErrNoKeyring` | `platform/sql/dialect` | The layer's sentinels: a transaction that could not begin or commit, a malformed or failing migration, and a sealed value read by a store without a keyring. Their texts keep the `sqlcommon:` prefix that operators and logs have always shown | STO-04 |
| `SealedColumn`, `FirstKey` | `platform/sql/dialect` | One column of sealed values as its module registers it: the table, the column, the purpose, the key columns and the row id built from their values, which must equal what the module's store seals with, because both are authenticated with the value (decision 0017). `FirstKey` is the row id of a column whose row id is its first key column. `keys rotate` reaches only registered columns, so the values of a column left out stay under a retired key | STO-05 |
| `Module` | `platform/sql/dialect` | One module's registration with the SQL layer: its canonical name, the sealed columns its store writes and a hook that runs before them, such as the artifacts module's backfill of repository headers. STO-06 adds the migration stream and its hook | STO-05 |
| `Reseal` | `platform/sql/dialect` | The re-seal that `keys rotate` runs: the modules in the order given, each module's hook and then its columns, under the keyring's active version, returning how many values changed. A function rather than a method of `Store`, because the SQL stores embed `Store` and declare a `Reseal` of their own, which would hide a method of that name without a compile error | STO-05 |
| `Applied`, `StreamVersion` | `platform/sql/dialect` | One migration as `Migrate` and `Rollback` report it, its module and version, printed as `module:version`; and one stream as `Version` reports it, its module with the applied and the latest version | STO-06 |
| `ErrLegacyHistory`, `ErrUnknownModule`, `ErrModuleRequired` | `platform/sql/dialect` | The migrator's refusals, each wrapping `ErrMigration` and keeping its `sqlcommon: migration:` prefix: a `schema_migrations` table without a module column, which predates the streams and is refused before any write; a module that no stream has; and a rollback target other than 0 without a module, because each stream numbers its own versions | STO-06 |
| `Module.Migrations`, `Module.AfterMigrate` | `platform/sql/dialect` | A module's migration stream, its embedded files under `sqlite/` and `postgres/`, and a hook that runs when a store opens, after every stream is current, for work that DDL cannot do: the artifacts module's conversion of repository headers needs the keyring (decision 0017), and the access module's search names need Go's case folding (decision 0043) | STO-06 |
| `Dialect.Columns` | `platform/sql/dialect` | The query that lists the columns of the table its one parameter names, in the current schema only, which the migrator reads to refuse a legacy history before it writes anything. Each driver's `Dialect` sets it | STO-06 |
| `FS` | `platform/store/sql/migrations` and the `adapters/sql/migrations` package of each of the seven other modules | A stream's migration files, embedded under `sqlite/` and `postgres/` and starting at `0001_baseline.sql`, which the module's registration passes as `Migrations`. The target document places the platform stream in `platform/sql/migrations`; "Persistence" records why it is under `platform/store/sql` | STO-06 |
| `OpenDB` | `platform/sql/sqlite`, `platform/sql/postgres` | Returns a pool for one database of the engine with the driver's settings: SQLite's pragmas and IMMEDIATE transactions, and the in-memory database on one connection; PostgreSQL's pool size and connection lifetime. It does not connect, migrate or wrap the pool in a store, so the composite store's open and STO-08's module SQL test harness both build on it. The target document gives the packages "open + driver settings" without naming the function | STO-07 |
| `Options` | `platform/sql/sqlite`, `platform/sql/postgres` | The driver settings `OpenDB` takes: SQLite's busy timeout, which `DSN` also takes, and PostgreSQL's pool size and connection lifetime. The composite store's `Options`, which also carries the store's options, keeps these fields and copies them in | STO-07 |
| `Dialect`, `IsUniqueViolation` | `platform/sql/sqlite`, `platform/sql/postgres` | Each engine's `dialect.Dialect` value, and the test it carries for a unique or primary-key violation, which the SQL store turns into `kernel.ErrConflict`. The target document names the `Dialect` type, not the drivers' values | STO-07 |
| `DSN`, `Memory`, `ErrPathRequired` | `platform/sql/sqlite` | The modernc connection string with its pragmas and IMMEDIATE transactions, the path of a private in-memory database, and the sentinel for an empty path, whose text keeps its `sqlite:` prefix | STO-07 |
| `ErrDSNRequired` | `platform/sql/postgres` | The sentinel for an empty DSN, whose text keeps its `postgres:` prefix | STO-07 |
| `Opts`, `Want`, `Same`, `Must`, `WantErr`, `Get` | `platform/testkit/storekit` | The checks every store contract suite makes: a value read back against `kernel.Normalise` of the value written, compared with go-cmp options that treat empty and nil collections as equal and require times in the same location, and a call's error, expected or not. The metadata store's suite kept them unexported; the module suites (STO-12 to STO-18) share them | STO-08 |
| `Base`, `At`, `Pages` | `platform/testkit/storekit` | A fixed instant in a non-UTC zone with sub-microsecond digits and the instants after it, so a stored time also checks UTC and precision normalisation, and the reader of every page of a keyset list over `kernel.Page`, which fails on a page larger than asked for or a list that does not end | STO-08 |
| `PostgresDSN` | `platform/sql/sqltest` | The DSN of the one PostgreSQL server a test process shares: `ARTIFACTWEAVE_TEST_POSTGRES_DSN`, or a `postgres:17-alpine` container started once; it skips where Docker cannot run Linux containers. The legacy harness bridges it until PRO-02 | STO-08 |
| `ScratchDSN` | `platform/sql/sqltest` | A DSN for the same server whose connections start in an empty schema of their own, selected through `search_path` and dropped when the test ends, so each PostgreSQL store of `RunModule` holds its modules' tables alone and the composite tables in `public` are left alone | STO-08 |
| `Reset` | `platform/sql/sqltest` | Rolls every stream of the given modules back to zero and migrates it again, emptying their tables and leaving other streams' tables alone; the legacy harness's `Reset` calls it with the composite module list | STO-08 |
| `OpenSQLite` | `platform/sql/sqltest` | A fresh in-memory SQLite store migrated with the given modules, sealed with the test keyring, with each module's `AfterMigrate` run, for a test that needs one store rather than a suite | STO-08 |
| `Option`, `WithPostgres`, `WithKeyring` | `platform/sql/sqltest` | `RunModule`'s options: the PostgreSQL run, which a module suite asks for because module suites run on SQLite ("Persistence"), and a keyring in place of the test keyring, nil for none. The target document's `RunModule(t, open, suite)` takes neither, nor the modules whose streams make the schema | STO-08 |
| `Exec`, `Fails`, `Err`, `Err3`, `FailTrigger`, `DropTable` | `platform/sql/sqltest` | The fault-test helpers of the SQL stores' fault tests: statements run behind a store's back, a check that every listed call failed, the error of a call with two or three results, and the SQLite statements that make a table's writes fail or drop the table | STO-08 |
| `Config`, `Tune`, `Router` | `test/server/support/harness` | The configuration server-assembly suites build the production router from, the function that adjusts one before the router is built, and the router built from it. `Config` and `Tune` are aliases, so a suite's literals and option functions keep their types; the harness is the one place the suites name the router, which WEB-22 re-points at `app.RouterConfig` and `app.NewRouter` | TST-01 |
| `ItemWorld`, `NewItemWorld`, `ItemWorld.Do`, `ItemWorld.Item`, `WantStatus`, `AnonymousRemotes` | `test/server/support/harness` | The item world of the root server tests, exported for the route suites that leave `internal/server`: the router over a store with the users reader, deployer and user, the `deploy-libs` permission, anonymous reads of remote repositories when asked for, and eight local, remote and virtual repositories; a request as one of those users, a stored item, and a status check. The root tests keep their in-package copy, because a test in the router's own package cannot import the harness, which imports the router | TST-01 |
| `SigningBlobs`, `StorageSigner` | `test/server/support/harness` | A blob store that signs download URLs for the routes it lists, with the signing options readable back from the URL and an error that fails every signature, and the one that signs storage URLs only, which the item world uses | TST-01 |
| `RepoFile` | `test/server/support/harness` | The absolute path of a root-relative fixture, through `repopath.Path`, so suites read fixtures the same way from whichever directory they move to | TST-01 |
| `Repositories` | `artifacts/api` | The repository operations other modules call today: `Get`, `List`, `UsingKeypair`, `Create`, `Update`, `Remove` and `RequestReindex`, which join the transaction the context carries, and the checks and merge rules every entry point applies before a write (`Protected`, `PrepareCreate`, `CheckMembers`, `CheckKeypairs`, `CheckEnvironments`, `Merge`, `OwesReindex`). The target document lists `Get`, `List`, `Create`, `Update`, `Delete` and `SetProject`; `SetProject` waits for project attach (P11) | ART-07 |
| `Layouts` | `artifacts/api` | `Check`, which compiles a layout definition as the layout engine does before configuration stores it, returning the engine's error unwrapped. The target document lists `Get` and `Put` for the descriptor and P12; the descriptor stores layouts as configuration documents itself | ART-07 |
| `RepoFilter`, `LayoutDefinition` | `artifacts/api` | The api's own copies of the store's repository filter and the layout engine's `Definition`, field for field and in order, so the module converts them with plain conversions that stop compiling if either side changes | ART-07 |
| `ErrChanged`, `MsgKeyExists`, `IsNull` | `artifacts/api` | The sentinel `Repositories.Remove` returns when the content changed under the delete, the message of a create whose key is taken, and the test for a null value in an update body or descriptor; declared in the api for the callers of `Repositories`, which its operations return and use, with their texts unchanged | ART-07 |
| `NewRepositories` | `artifacts/core/repos` | Binds `Repositories` over the store and access's `Keys` and `Projects` | ART-07 |
| `Checker` | `artifacts/core/layout` | The layout engine's implementation of `Layouts` | ART-07 |
| `FetchCocoaPodsGit` | `artifacts/ecosystem/cocoapods` | The CocoaPods Git fetching that the remote policy reaches through `Policy.GitFetch`: a Git-sourced pod's revision and pinned submodules as one deterministic tar.gz upstream response; it was the policy's method `fetchCocoaPodsGit` | RMT-02 |
| `Policy.GitFetch` | `artifacts/core/remote` | The hook through which the policy answers a CocoaPods Git checkout; nil answers `source.ErrUnsupported`, as a policy without a pool does | RMT-02 |
| `ResponseError` | `artifacts/core/remote` | The typed `*source.HTTPError` of an upstream answer that is neither content nor an absence, which format rules in the slices return unwrapped; it was `responseError` | RMT-02 |
| `Request.GitUploadPack` | `artifacts/adapters/upstream` | Marks the CocoaPods Git adapter's upload-pack POST, which `Client.Do` admits only when its whole guard holds; it was `gitUploadPack` | RMT-02 |
| `EagerKey` | `artifacts/core/remote` | Marks a Maven companion job's context with the credential revision the job was queued under; the policy's reads and `Check` answer `source.ErrSuperseded` when the revision differs. It was `eagerKey` | RMT-03 |
| `PolicyError` | `artifacts/core/remote` | Classifies a failure of the policy's work as the source service's typed error or sentinel, which format rules in the slices return unwrapped; it was `policyError` | RMT-03 |
| `TransportRequest` | `artifacts/core/remote` | The upstream request for a source request and its cached representation, from which format rules build their own exchanges; it was `transportRequest` | RMT-03 |
| `Policy.Check` | `artifacts/core/remote` | The authority fence a format rule calls after its own upstream exchanges, before it uses what it fetched; it was the method `check` | RMT-03 |
| `ProjectEmptyFeed` | `artifacts/ecosystem/nuget` | The NuGet v2 empty-feed projection that the remote policy reaches through `Policy.ProjectResponse`: a bodyless upstream 204 of an opted-in feed or query becomes one fixed empty Atom feed; it was an unexported function of the policy's package | RMT-04 |
| `Policy.ProjectResponse` | `artifacts/core/remote` | The hook that may rewrite an upstream response before admission, after the module-location projection and before missing-status normalisation; nil leaves every response as sent | RMT-04 |
| `Policy.CacheKey` | `artifacts/core/remote` | The remote cache key of a request under a credential revision, which tests outside the policy's package compare, the NuGet slice's among them; it was the method `cacheKey`, and its identity terms, `nuget-empty-feed-v1` among them, are unchanged | RMT-04 |
| `Registries`, `RegistryClient` | `artifacts/core/remote` | The registry port, through which the Docker rules in `ecosystem/docker` reach upstream registries without holding the client pool or the credential resolver: `Open` returns the pooled client for a repository, source identity and credential revision, or `source.ErrSuperseded` when the revision changed, and `Foreign` sends one foreign layer request; a `RegistryClient` names an image as the upstream knows it, sends a Distribution request and gives its base URL | RMT-05 |
| `RegistryClients`, `RegistryClients.Open`, `RegistryClients.Foreign`, `Registry.Base` | `artifacts/adapters/upstream` | The transport's `Registries` over the registry pool and the resolver's registry options: `Open` returns a literal nil client on every error, never a typed-nil `*Registry`, and `Foreign` stays unpooled because the pool's fingerprint does not cover the hooks that strip a foreign layer request's credentials and headers. `Base` returns a copy of a registry client's base URL, which a listing's next page must stay under | RMT-05 |
| `ParseRegistryPath` | `artifacts/ecosystem/docker` | The registry path grammar of a remote read, `<image>/<operation>/<reference>`, which the application hands `NewSmart` as Smart Remote's registry-path parser so that Smart never imports the slice; a nil parser gives no registry property path and no registry grant. It was `registryResource` | RMT-05 |
| `Origin`, `CheckedURL` | `artifacts/core/remote` | A URL's scheme, host and port as one comparable value, and the parse of an absolute HTTP or HTTPS URL that refuses anything that could address something other than it shows; the Docker rules keep a listing's next page on the registry and check a foreign layer's URL with them. They were `origin` and `checkedURL` | RMT-05 |
| `Upstream`, `Connection` | `artifacts/core/remote` | The upstream port, through which the policy, Smart Remote and the format rules reach origins without holding the client pool: `Upstream.Connection` returns a repository's connection for its resolved settings, a nil interface on every error; a `Connection` fetches or sends a request under the transport's rules, and gives a request its caller builds the destination check, the credential preparation and a transport that follows no redirect. Every successful `Fetch`, `Do` or `Send` hands the response body to the caller. The target document names an `Upstream` port in `core/port` with `Fetch`, `Head` and `List` | RMT-06 |
| `Outbound`, `WithOutbound`, `OutboundFrom` | `artifacts/core/remote` | Request-scoped hooks that every upstream connection applies: `Header` edits a request and each redirect that stays on its origin, and `Allow` refuses a target, the first or a redirect's, with `ErrSecurity` before it is sent. Smart Remote installs them, so the transport no longer reads Smart's session | RMT-06 |
| `Policy.FlightKey` | `artifacts/core/remote` | The key under which concurrent fills of one cache key for one source identity share an upstream exchange; Smart Remote keys its discovery flights the same way. It was the method `flightKey` | RMT-06 |
| `Pool.Connection`, `Client.Guarded`, `Client.CheckURL`, `Client.Prepare`, `Client.Send` | `artifacts/adapters/upstream` | The transport's `Upstream` and `Connection`: `Pool.Connection` returns a literal nil connection on every error, never a typed-nil `*Client`, and the four methods give Smart Remote's own requests the client's destination check, credential preparation and transport without reading its fields | RMT-06 |
| `Interceptor`, `WithSmart`, `SmartActive` | `artifacts/core/remote` | The seam through which the policy reaches Smart Remote without importing it: `Policy.Smart` is an `Interceptor`, which receives every read whose context carries no `WithSmart` mark, and `SmartActive` tells Smart's header hook whether the mark is active. The policy tests the mark's presence, not its value, so the inactive mark Smart sets on entry stops it entering itself again. They replace `Policy.Smart *Smart` and the context key that core remote kept for Smart | RMT-07 |
| `Policy.State`, `Policy.Cooldown`, `Policy.RecordFailure`, `Policy.RecordResponse` | `artifacts/core/remote` | The upstream circuit, which Smart Remote's own peer requests share with the policy's fills: the availability state under a credential revision, the refusal while a Retry-After pause or an offline period runs, and the record of a failure or a response. They were the methods `state`, `cooldown`, `recordFailure` and `recordResponse` | RMT-07 |
| `Policy.ReadEntry`, `Policy.Acquire`, `MetadataContext`, `MustNotStore` | `artifacts/core/remote` | What else Smart Remote uses of the policy: a cache entry read for the permit's source identity, a slot of the policy's upstream capacity, the metadata retrieval timeout, and the no-store test of a response. They were the method `readEntry`, the admission's `acquire` behind the field `capacity`, `metadataContext`, and an expression that `fetch.go` and Smart's discovery repeated | RMT-07 |
| `FlightGroup`, `FlightGroup.Do` | `artifacts/core/remote` | One run of an operation shared by concurrent callers with one key, on a context of its own; Smart Remote's discovery uses it as the policy's fills do. They were `flightGroup` and `do`; the fields stay unexported | RMT-07 |
| `SmartPeer.Revision`, `SmartPeer.WithRevision` | `artifacts/core/remote` | The credential revision a peer was discovered under, which Smart Remote sets after discovery and compares before it uses the peer. The field stays unexported behind them, because `SmartPeer` stays in core with `ParseSmartPeer` | RMT-07 |
| `BaseURL`, `Port`, `RetryAfter` | `artifacts/core/remote` | The URL and pause vocabulary the transport shares with core: the parse of a configured repository URL, a URL's port or the one its scheme implies, and the bounded pause a Retry-After header asks for. They were `baseURL`, `port` and `retryAfter` | RMT-08 |
| `Admission`, `Admission.Acquire` | `artifacts/core/remote` | The bound on active and queued upstream work per key and in all, which the policy and the registry token exchange both hold; its zero value applies the defaults on first use. It was `admission` with `acquire` | RMT-08 |
| `FlightGroup.Pending` | `artifacts/core/remote` | The number of callers waiting on a group's active flights, which the transport's coalescing test polls rather than reading the group's fields from another package | RMT-08 |
| `NewPool`, `NewResolver` | `artifacts/adapters/upstream/upstreamtest` | A transport pool closed when the test ends, and a credential resolver with no keyring, for core remote's and Smart Remote's tests, which may not import their own module's adapter | RMT-08 |
| `Store` | `notifications/core/port`, `retention/core/port`, `federation/core/port`, `releases/core/port`, `workers/core/port` | Each module's part of the metadata store: the shared unit of work (`tx.Runner`) and the module's stores, `Webhooks`; `Cleanup` and `Archive`; `Replications`, `Peers`, `Federation` and `Platform`; `Bundles` and `Distribution`; `Workers`. The metadata store's in-memory and SQL stores satisfy all five, which their tests assert at compile time. The target document names each module's `core/port/store.go` and the stores in it, not this aggregate | STO-09 |
| `ArchivedItemStore`, `Store.ArchivedItems`, `Store.ColdBlobs`, `Store.ColdReplicas` | `pkg/store` | The archived items' port, which took `ArchiveStore`'s six item methods, and the metadata store's accessors for it and for the cold blob and replica indexes, which left `ArchiveStore`: Go has no covariant returns, so one `Archive()` could not serve both retention's port and the artifacts one. Artifacts-owned ("Ownership of shared models"); STO-11 moves the port to the artifacts module | STO-09 |
| `RunStatus` with its ten `Run*` statuses and `Terminal` | `retention/api` | The status of a cleanup or smart-archiving run, which the federation model's `FullSyncRun` reports too, so it is declared in retention's api rather than its model; the shared types alias them until PRO-02. The target document names only the api's `CleanupPolicies`, `ArchivePolicies` and `Restores` | STO-09 |
| `Store` | `access/core/port` | Every store the access module owns: the shared unit of work (`tx.Runner`) with `Users`, `Groups`, `Permissions`, `Tokens`, `Keys`, `Projects` and `APIRestrictions`. The metadata store's in-memory and SQL stores satisfy it, which a test inside access asserts at compile time, because tests outside access may not import access core. The target document names `core/port/store.go` and its stores, not this aggregate | STO-10 |
| `Store` | `artifacts/core/port` | Every store the artifacts module owns: the shared unit of work (`tx.Runner`) with `Repos`, `RemoteCache`, `Items`, `Blobs`, `Replicas`, `Trash`, `Packages`, `GitLFSLocks`, `BowerRegistrations`, `Builds`, `ArchivedItems`, `ColdBlobs` and `ColdReplicas`; the cold accessors return the hot index types, because the cold tier is indexed with the same code. The metadata store's in-memory and SQL stores satisfy it, which their tests assert at compile time. The target document names `core/port/store.go` and its stores, not this aggregate | STO-11 |

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
