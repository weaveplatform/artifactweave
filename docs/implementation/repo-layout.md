# Repository layout

This guide maps the current package tree and where new code belongs. Phase status
lives in the [implementation plan](plan.md); supported repository classes and native
client evidence live in the [package-format matrix](package-format-matrix.md).
Import tiers are enforced by a test in `internal/layout`: `pkg/` never imports
`internal/` or `cmd/`, and `internal/format/*` never imports `internal/server`.

Module: `github.com/weaveplatform/artifactweave` (Go 1.27). One module,
one release binary, plus developer commands under `tools/`. Builds use `GOWORK=off`
so local workspace overrides cannot hide dependencies missing from `go.mod` in CI.
Local `go.work` files stay untracked.

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
pkg/                           # PUBLIC LIBRARY: no imports from internal/ or cmd/
  repo/                        # repository configuration model mirroring the Artifactory JSON, validation
  layout/                      # repository layout pattern engine ([org]/[module]/... tokens)
  types/                       # internal domain model, version comparators, scope parser, wire converters
    wire/                      # the only hand-written structs with JFrog JSON names
  aql/                         # AQL parser, AST, SQL compiler per dialect
  blob/                        # blob store contract (upload sessions, ranged reads, presign)
    fs/ s3/ gcs/ azure/        # backends
    session/                   # upload session shared by the object-store backends
    mirror/                    # one store over several members, with a write quorum
    blobtest/                  # contract suite
  store/                       # store contracts, sentinel errors, cursors
    inmem/                     # reference in-memory backend
    storetest/                 # contract suite
  containertest/               # gates the tests that start containers (Docker on Linux)
  cdn/                         # CloudFront, Cloud CDN and Front Door signed download URLs
  client/
    artifactory/ access/ event/ distribution/ grid/ pfed/ worker/
                               # generated Go clients (oapi-codegen), used by tests
internal/
  layout/                      # import-tier enforcement test
  app/                         # wiring: config → stores → services → router
  config/                      # strict app YAML, flag > env > file precedence
  auth/                        # request authentication, tokens, built-in principals
  authz/                       # the permission evaluator
  reindexauth/                 # current-credential and permission checks for queued NuGet reindex work
  keymaterial/                 # key pair and certificate checks for the security API
  repos/                       # repository operations shared by the API and the descriptor
  descriptor/                  # Artifactory YAML descriptor import, XML descriptor export
  source/                      # authorized reads shared by remote adapters, below HTTP routing
  remote/                      # upstream transport, retrieval/cache policies, protocol adapters and Smart Remote
  fakeupstream/                # controlled HTTP/TLS upstream fixtures and fault injection for tests
  server/                      # chi router, middleware, auth, baseurl resolver, errors table,
                               # and the hand-written item-path handlers beside the generated ones
    gen/                       # generated strict servers, one package per spec
    apierror/ baseurl/         # error envelopes and the base-URL resolver
    docker/                    # the registry protocol: /v2 routing, blobs and uploads
    golang/                    # the GOPROXY protocol under /artifactory/api/go (decision 0035)
    npm/                       # the npm registry: packuments, version manifests, tarballs, publish
                               # (decision 0034)
    pypi/ nuget/ helm/ terraform/ # package protocols and signed registry downloads
    terraformbackend/          # Terraform remote backend API and signed state uploads
    agentpackages/ skills/ luarocks/ nimmodel/
    aieditorextensions/ bazelmodules/ bower/ cargo/ cocoapods/ composer/ conan/ conda/
    gems/ gitlfs/ hex/ huggingface/ jetbrainsplugins/ pub/ swift/ vcs/
                               # additional native protocol adapters; some handlers remain in server/*.go
    pkgroute/                  # the router kit for native package protocols
                               # (decision 0032)
  sqlstore/
    sqlcommon/                 # Dialect, unit of work, migrator
    sqlite/ postgres/          # dialect packages, each with migrations/*.sql
    sqltest/                   # shared SQL store contract-test harness
  crypt/                       # keyring, sealed columns, key rotation
  archive/                     # zip and tar walker, bounded reads of one file from a package
  format/                      # the format contract: hooks, Env, optional interfaces, RejectionError
    formats/                   # picks the format of a package type
    formattest/                # contract suite every format runs with its fixture
    generic/ docker/ npm/ golang/ maven/ pypi/ nuget/ helm/ terraform/ terraformbackend/
    agentpackages/ agentplugins/ skills/ luarocks/
    aieditorextensions/ alpine/ bazelmodules/ bower/ cargo/ cocoapods/ composer/ conan/ conda/
    cran/ debian/ gems/ gitlfs/ hex/ huggingface/ opkg/ p2/ pub/ rpm/ swift/
                               # format-owned metadata, validation and lifecycle hooks
  service/                     # one package per domain service; more arrive with their phases
    build/ gc/ identity/ item/ multipart/ oidc/ permission/ pkgindex/ registry/ roles/ trash/ usage/
    terraformstate/            # the Terraform backend's workspaces, states and locks (decision 0041)
    work/                      # the persistent work queue's worker pool (decision 0033)
  testfault/                   # shared failing fakes for handler tests
  buildinfo/                   # version stamped by make build (from the template)
  openapibundle/ stubgen/      # spec tooling: fragment bundler, 501 stub generator
  specoracle/                  # cached JFrog definitions and the parity comparison
test/
  acceptance/                  # godog features and steps, run on Linux
    features/
    testdata/                  # native-client fixtures, pinned clients and controlled upstream data
  server/                      # server-assembly suites by protocol, plus shared test support
  oci/                         # OCI distribution-spec conformance job wrapper
  platform/                    # real-cloud deployment scenarios, run by platform-scenarios.yml
  terraform/                   # run.sh only: applies the deploy/artifactweave scenarios; builds the provider mirror at run time
tools/
  openapi-bundle/ stubgen/ specoracle/ # spec generation and parity commands backed by internal/ packages
  citestselection/             # CI profile, package and test selection; test_ownership.json
  testnames/                  # enforces TestUnit/TestAcc names and reviewed TestMain/fuzz owners
  formatinventory/            # validates the format inventory and generates the class/client matrix
  coverexec/                  # isolates each test binary's coverage output before merging
  gpgkey/                     # generates temporary OpenPGP keys for Terraform scenarios
  acceptancepartition/        # tests the workflow's native acceptance partitions; no executable
docs/
  research/ implementation/ security/ operations/ testing/
```

Remote retrieval and its test fixtures are implemented in `internal/source`,
`internal/remote` and `internal/fakeupstream`. The cron scheduler
(`internal/scheduler`, P11) remains planned; current periodic GC and package
cleanup are wired through `internal/app` and `internal/service/work`.

## Code placement

Keep application wiring and resource lifecycles in `internal/app`, with dependencies
passed explicitly to stores, services and routers. Project-only behavior belongs
in `internal/`; `pkg/` is reserved for importable contracts, models, clients and
implementations useful to other repositories. New protocol routes belong in
`internal/server/<family>`, with format metadata and lifecycle behavior in
`internal/format/<family>`.

Release entry points belong in `cmd/<binary>`. Developer commands are an existing
exception under `tools/<name>`, invoked by the Makefile, CI or test drivers.
`make build` cross-compiles every immediate `cmd/*` directory for each release
platform, while GoReleaser currently publishes only `cmd/artifactweave`. Moving a
tool into `cmd/` therefore changes the build scope and requires updating its callers.

Keep command wrappers thin and put substantial logic in `internal/`. The current
[coverage configuration](../../.testcoverage.yml) excludes all of `cmd/` and
`tools/` from its thresholds. `tools/citestselection`, `tools/formatinventory` and
`tools/testnames` still contain substantial logic and tests in their main packages;
that logic is outside the coverage gate until it is extracted or the exclusions
are narrowed.

## Tests and fixtures

Keep package tests beside the code in `_test.go` files and package-specific fixtures
in `testdata/`. Server-assembly suites belong in `test/server/<family>`; shared
fixtures and their import constraints are described in the
[server test package guide](../testing/server-test-packages.md). Native-client
features and steps live in `test/acceptance`, and real-cloud scenarios in
`test/platform`.

Use `TestUnit...` for isolated logic and `TestAcc...` for API, protocol, storage,
application and native-client behavior. Both kinds can share a package; CI selects
them separately. Follow the [CI test-kind rules](../testing/ci.md#go-test-kinds) when
adding or moving tests, including reviewed `TestMain` and fuzz owners. Package
moves also need to preserve the test ownership inventory in
`tools/citestselection/test_ownership.json` and the format mappings used by the CI
selector.

## Naming rules

- Format packages are named after the ecosystem (`golang`, not `gomod` or `go`).
- Hand-written JFrog wire models live only in `pkg/types/wire` (and `pkg/repo`, whose
  wire and internal shapes coincide); generated ones in `internal/server/gen` and
  `pkg/client`. Services and stores use the domain types in `pkg/types` and
  `pkg/repo`.
- Generated files end in `.gen.go` so lint and coverage exclusions match.

## Domain types

`pkg/types` is the internal domain model of decision 0020: one representation per
entity in [research 15](../research/15-data-model.md), including the peer, outbox,
federation, distribution, archive, Access Federation, topology and worker entities of
decisions 0021 to 0026. It also holds the version comparators of decision 0018, the
token scope parser and the converters between the internal permission, user and group
shapes and their JFrog wire forms. The repository configuration stays `pkg/repo.Config`.
Having a domain type or generated API contract does not imply the corresponding service
is implemented: peer, federation, distribution, archive and worker behavior remains in
later phases.
`pkg/types/wire` holds the only hand-written structs with JFrog JSON names (legacy v1
and v2 permission targets, Access v2 permissions, legacy and Access users and groups);
it has no logic, depends on nothing in the project, and is exempt from the snake_case
tag rule alongside `pkg/repo`. Types that the stores persist as JSON documents carry
snake_case tags; secrets are tagged `json:"-"` and live in sealed columns.

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
