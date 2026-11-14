# Repository layout

This guide maps the package tree and where new code belongs. Phase status lives in the
[implementation plan](plan.md); supported repository classes and native client evidence
live in the [package-format matrix](package-format-matrix.md). The tree is the modular
monolith of [decision 0044](../research/decisions/0044-modular-monolith-migration.md),
and the tests in `internal/layout` enforce its dependency rules, its fixed set of
top-level directories and its package documentation standard. Code paths from before
P9d map to their current location through [p9d-path-map.json](p9d-path-map.json).

Module: `github.com/weaveplatform/artifactweave` (Go 1.27). One module,
one release binary, plus developer commands under `tools/`. Builds use `GOWORK=off`
so local workspace overrides cannot hide dependencies missing from `go.mod` in CI.
Local `go.work` files stay untracked.

## Tree

```text
cmd/
  artifactweave/               # cobra entry point: serve, migrate, config, keys, version (excluded from coverage)
api/
  src/<service>/*.yaml         # editable OpenAPI fragments; make generate bundles them
  artifactory.openapi.yaml     # /artifactory/api management surface (flat CRUD, spec-first)
  access.openapi.yaml          # /access/api identity, tokens, projects, roles, environments
  event.openapi.yaml           # /event/api webhooks
  distribution.openapi.yaml grid.openapi.yaml pfed.openapi.yaml worker.openapi.yaml
                               # bundled contracts; unimplemented operations return 501
  oapi-server-*.yaml           # oapi-codegen configs, one per spec
  oapi-client-*.yaml
internal/
  artifacts/                   # module: repositories, items, search, builds and the package formats
    api/                       # published surface; api/repo is the repository configuration model
    core/                      # item, repos, search, aql, build, gc, trash, multipart, pkgindex,
                               # registry, remote (with smart and source), layout, usage,
                               # terraformstate, reindexauth, model, port, format, formattest, virtual
    adapters/                  # http (management API, itemkit, pkgroute), sql, inmem, storekit,
                               # storetest, upstream (with fakeupstream and upstreamtest)
    ecosystem/                 # the format registry
      <type>/                  # one slice per package type: metadata, validation, lifecycle hooks
        http/                  # the type's native protocol routes, package <type>http
  access/                      # module: authentication, authorisation, users and groups, tokens,
                               # permissions, roles, OIDC
    api/ core/ adapters/ accesstest/
  configuration/               # module: the configuration descriptor and the system operations
    api/ core/ adapters/
  notifications/ retention/ federation/ releases/ workers/
                               # modules with stores and models; services arrive with their phases
  evidence/ governance/ security/ operations/ estate/
                               # modules that are placeholders until their phases
  kernel/                      # values every module shares: errors, paging, principal, scopes,
                               # checksums; wire (JFrog JSON forms) and antpath
  platform/                    # services no module owns
    blob/                      # blob store contract; fs, s3, gcs, azure, mirror, session, blobtest;
                               # cold is a placeholder
    sql/                       # dialect (unit of work, migrator), sqlite, postgres, sqltest;
                               # mysql is a placeholder
    store/                     # the platform's own stores: inmem, sql, storetest
    tx/                        # the unit-of-work contract and its in-memory implementation
    gen/<service>/             # generated strict servers, one package per spec
    gen/operations/            # the generated operation table and its chi lookup
    httpkit/                   # handler helpers, middleware, apierror, baseurl
    crypt/ keymaterial/        # sealed columns and the keyring; key pair and certificate checks
    work/ outbox/ idempotency/ apilimit/ settings/ instance/ configdoc/ archive/ archivecheck/
    cdn/ buildinfo/
    testkit/                   # test support: fault, repopath, storekit
    events/ kms/ observe/      # placeholders
  gateway/                     # placeholders: console, onemodel
  app/                         # composition root: configuration, stores, modules, router, run
    config/                    # strict application YAML, flag > environment > file precedence
    adapters/                  # the composite stores: inmem, sql (sqlite, postgres)
    apptest/ storetest/        # test support that needs the composite stores
  layout/                      # repository-wide layout, documentation and guard tests
  devtools/                    # openapibundle, optable, specoracle, stubgen: libraries behind tools/
pkg/
  client/<service>/            # generated Go clients (oapi-codegen), used by tests
  containertest/               # gates the tests that start containers (Docker on Linux)
test/
  acceptance/                  # godog features and steps, run on Linux
    features/
    clients/                   # pinned native-client images
    fixtures/ testdata/        # native-client fixtures and controlled upstream data
  server/                      # server-assembly suites
    access/ configuration/     # module suites
    artifacts/                 # the artifacts management suites
      <type>/                  # one suite per package type
    support/                   # shared fixtures and the harness
  fixtures/                    # generated fixtures that several suites read
  oci/                         # OCI distribution-spec conformance job wrapper
  platform/                    # real-cloud deployment scenarios, run by platform-scenarios.yml
  peers/                       # placeholder for multi-instance scenarios
  terraform/                   # run.sh and its helpers: apply the deploy/artifactweave scenarios
tools/
  openapi-bundle/ stubgen/ optable/ specoracle/  # spec generation and parity commands over internal/devtools
  citestselection/             # CI profile, package and test selection; test_ownership.json
  testnames/                   # enforces TestUnit/TestAcc names and reviewed TestMain/fuzz owners
  formatinventory/             # validates the format inventory and generates the class/client matrix
  clientrecipes/               # validates and renders the client connection recipes
  coverexec/                   # isolates each test binary's coverage output before merging
  gpgkey/                      # generates temporary OpenPGP keys for Terraform scenarios
  acceptancepartition/         # tests the workflow's native acceptance partitions; no executable
  depsync/                     # Python scripts that keep pinned client and image versions current
deploy/                        # Terraform reference deployments (below)
docs/
  research/ implementation/ security/ operations/ testing/
```

Exactly these nineteen directories sit under `internal/`, and no file sits directly under
it; `pkg/` holds only `client/`, `containertest/` and its README.
`TestUnitTopLevelDirectories` fails on anything else, so a new top-level directory needs
a decision first. A placeholder holds `doc.go` alone until its phase.

## Dependency rules

`TestUnitImportTiers` judges every import of a module package by a Go file under
`internal/`, `pkg/client` or `pkg/containertest`. The rows below are checked in order and
the first that applies decides; a failure names its row. Paths are relative to
`internal/`, except those that start with `pkg/` or `test/` and `api`, the repository's
own `api/` package.

| Row | Rule |
|---|---|
| 0 | An external test package may import its own directory |
| 1 | A test file may import test support: `*/adapters/inmem`, `platform/testkit/**`, `artifacts/adapters/upstream/fakeupstream`, any package whose name ends in `test`, and `test/**` |
| 2 | Only a test file may import `test/` |
| 3 | `pkg/client` imports only `pkg/client`, `kernel/wire` and `artifacts/api/repo` |
| 4 | `pkg/containertest` imports no package under `internal/` |
| 5 | `app`, `devtools` and `layout` may import anything |
| 6 | `platform/gen` may import `artifacts/api/repo` and `kernel/wire`, the two packages that generated code names through `x-go-type-import` |
| 7 | `platform` and `kernel` import no module, `gateway`, `app`, `devtools` or `layout` |
| 8 | `gateway` imports only module `api` packages, `kernel`, `platform/httpkit` and `gateway` |
| 9 | A module's `api` imports only `kernel`, `platform/outbox` and its own `api` packages |
| 10 | No module package imports a module root, since only `app` wires modules; a module's test kit `<m>/<m>test` may import its own root `<m>` |
| 11 | A module imports another module only through that module's `api` |
| 12 | A module's `core` never imports its own `adapters` |
| 13 | Outside test files, an `artifacts/ecosystem/<type>/http` package is imported only by its own subtree and `artifacts/adapters/http` (and by `app`, through row 5) |
| 14 | A module may import its own packages, `platform`, `kernel` and `pkg/` |
| 15 | A module's test files may import `api` |
| 16 | A module imports nothing else |

`TestUnitImportRulesDetectViolations` proves that every row can both allow and reject an
import. There is no depguard configuration.

## Where new code goes

- **A behaviour of an existing area** goes to the module that owns the data: the logic in
  `<module>/core/<area>`, its store contract in `<module>/core/port`, its values in
  `<module>/core/model`, and its implementations in `<module>/adapters` (`sql`, `inmem`,
  with the contract suite in `storetest`). A module's HTTP handlers live in
  `<module>/adapters/http`, package `<module>http`.
- **A new package format** is a slice at `internal/artifacts/ecosystem/<type>`: the format
  hooks and metadata in the slice itself, its native routes in `ecosystem/<type>/http`
  (package `<type>http`), its entry in the format registry of `artifacts/ecosystem`, and
  its server-assembly suite in `test/server/artifacts/<type>`.
- **A call into another module** goes through that module's `api` package. When the
  contract is missing, the owning module adds it to its `api` and binds it in its
  constructor.
- **Wiring** is manual and lives in `internal/app`: each module has one constructor,
  `New(Deps)`, in its root package, and `app` builds the stores, calls the constructors
  and mounts the router. No other package imports a module root.
- **Something no module owns** goes to `platform` (a technical service) or `kernel` (a
  value every module shares). Neither may depend on a module.
- **Release entry points** belong in `cmd/<binary>`. Developer commands are an exception
  under `tools/<name>`, invoked by the Makefile, CI or test drivers. `make build`
  cross-compiles every immediate `cmd/*` directory for each release platform, while
  GoReleaser publishes only `cmd/artifactweave`, so moving a tool into `cmd/` changes the
  build scope and its callers.

Keep command wrappers thin. The [coverage configuration](../../.testcoverage.yml) excludes
all of `cmd/` and `tools/` from its thresholds, so logic that needs the gate goes to
`internal/` (for the spec tooling, `internal/devtools`). `tools/citestselection`,
`tools/formatinventory` and `tools/testnames` still keep substantial logic and tests in
their main packages, outside the gate until it is extracted or the exclusions narrow.

## Package documentation

Every package has one `doc.go`, which holds the only package comment.
`TestUnitPackageDocs` checks every package in the repository:

- the headings `# Role`, `# Inputs`, `# Outputs`, `# References` and `# Status`, in that
  order, with the optional `# Invariants` before References and `# Testing` last;
- the first sentence starts `Package <name>`, or `Command <name>` for a main package;
- under `internal/`, Role names the layer (api, core, adapter, ecosystem, platform,
  kernel, app, devtools, gateway, layout, test support or module root);
- Status gives the implemented phases, or reads `Placeholder: implemented by Pnn.` for a
  placeholder, whose directory holds `doc.go` alone.

Generated packages get a hand-written `doc.go`; package comments in `*.gen.go` files are
ignored. Every exported identifier has a doc comment that starts with its name and states
its contract. golangci-lint's revive runs the `exported` and `package-comments` rules on
every package; like every linter it skips generated files, test files and `tools/`.
Comments carry the reasoning a reader cannot recover from the code; they do not restate
it or record history.

## Generated code

`make generate` bundles `api/src/<service>` into `api/<service>.openapi.yaml`, then
generates the strict chi servers in `internal/platform/gen/<service>` and the clients in
`pkg/client/<service>` with oapi-codegen. Generated files end in `.gen.go`, so lint and
coverage exclusions match them; `stubgen` writes the 501 stubs beside each server, and
`optable` the table of every operation's route pattern and classes in
`internal/platform/gen/operations`, which admission and the `Idempotency-Key` gate read
(decision 0043).
`x-go-type-import` may name only `internal/kernel/wire` and
`internal/artifacts/api/repo`. `make verify-generate` fails when the committed output
differs from a fresh run.

## Tests and fixtures

- Keep package tests beside the code in `_test.go` files and package-specific fixtures in
  `testdata/`. A test that reads a repository file names it from the repository root
  through `internal/platform/testkit/repopath`; `TestUnitFixtureReadsAreRootRelative`
  rejects paths counted from the package directory.
- Use `TestUnit...` for isolated logic and `TestAcc...` for API, protocol, storage,
  application and native-client behaviour. Both kinds can share a package; CI selects them
  separately. Follow the [CI test-kind rules](../testing/ci.md#go-test-kinds), including
  the reviewed `TestMain` and fuzz owners.
- Contract suites hold the expectations every implementation meets: each module's
  `adapters/storetest`, `platform/store/storetest`, `platform/blob/blobtest`,
  `artifacts/core/formattest` and the composite stores' `app/storetest`.
- Server-assembly suites live in `test/server`: module suites in
  `test/server/<module>`, ecosystem suites in `test/server/artifacts/<type>`, shared
  fixtures in `test/server/support`, as the
  [server test package guide](../testing/server-test-packages.md) describes. The server
  suites, `internal/app` and the HTTP adapter packages record a reviewed owner in
  `tools/citestselection/test_ownership.json`, as the
  [ownership guide](../testing/server-test-ownership.md) describes.
- Native-client features and steps live in `test/acceptance`, and real-cloud scenarios in
  `test/platform`.

## Naming rules and domain types

- Format packages are named after the ecosystem (`golang`, not `gomod` or `go`).
- Hand-written JFrog wire models live only in `internal/kernel/wire` (legacy v1 and v2
  permission targets, Access v2 permissions, legacy and Access users and groups, and the
  other JSON forms exchanged field for field) and `internal/artifacts/api/repo` (the
  repository configuration, whose wire and internal shapes coincide, with its
  validation). `kernel/wire` has no logic and depends on nothing in the project. The two
  are the only exceptions to the snake_case tag rule; generated models live in
  `internal/platform/gen` and `pkg/client`.
- Domain types live in each module's `core/model`: one representation per entity of
  [research 15](../research/15-data-model.md), with the version comparators of decision
  0018 in the artifacts model. Types that the stores persist as JSON documents carry
  snake_case tags; secrets are tagged `json:"-"` and live in sealed columns.
- A domain type or a generated API contract does not mean the service is implemented:
  placeholder packages and 501 operations mark what later phases build.

## Deployment tree

Terraform lives under `deploy/` (decision
[0030](../research/decisions/0030-reference-deployments.md)): infrastructure per cloud and
scenario in `deploy/platform`, and artifactweave's own configuration through the JFrog
providers in `deploy/artifactweave`. Each scenario is a documented reference deployment
that CI checks on every pull request. Platform scenarios also run real-cloud proofs for
relevant pull requests and by manual dispatch. The project's own lab harness, which no
consumer copies, lives apart in `deploy/ci`.

```text
deploy/
  ci/
    README.md                         # the lab harness: bootstraps, guards, CI driver
    lab.mk lab-vars.sh                # every lab constant, and the inputs an operator's run resolves
    scenario.mk bootstrap.mk          # shared make targets for scenarios and bootstraps
    scenario-ci.sh prove-matrix.sh    # the platform-scenarios.yml driver and its proof rules
    lint-contract.sh                  # the contract every root under deploy/ is held to
    local.sh                          # runs artifactweave against any applied scenario
    terraform/aws/ gcp/ azure/        # bootstraps: CI identities, remote state, DNS zone, budget
  platform/
    README.md                         # scenario and module catalogue, how to use and run them
    SCENARIO-TEMPLATE.md MODULE-TEMPLATE.md   # the README templates the linter checks
    terraform/
      modules/<cloud>/<capability>/   # storage and CDN modules; replication on AWS; no provider blocks
      aws/ gcp/ azure/
        single-bucket/ multi-region/ geo-replicated/ cdn/   # implemented scenarios
      multicloud/
        mirror/                       # one mirror with a member on each cloud
  artifactweave/
    README.md                         # managing artifactweave with the JFrog Terraform providers
    terraform/<scenario>/             # repositories, identity, oidc, cdn-repositories, ...
test/
  terraform/run.sh                    # driver: applies a deploy/artifactweave scenario to a -cover server
  platform/                           # real-cloud scenario suite (_test.go only), run by platform-scenarios.yml
```
