# Target repository layout

This is the package tree the [implementation plan](plan.md) builds. Import tiers are
enforced by a test in `internal/layout` (P0): `pkg/` never imports `internal/` or
`cmd/`, and `internal/format/*` never imports `internal/server`.

Module: `github.com/weaveplatform/artifactweave` (Go 1.27). One module,
one binary.

```text
cmd/
  artifactweave/               # cobra entry point: serve, migrate, config, keys (excluded from coverage)
api/
  artifactory.openapi.yaml     # /artifactory/api management surface (flat CRUD, spec-first)
  access.openapi.yaml          # /access/api identity, tokens, projects, roles, environments
  event.openapi.yaml           # /event/api webhooks
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
  client/
    artifactory/ access/ event/  # generated Go clients (oapi-codegen), used by tests
internal/
  layout/                      # import-tier enforcement test
  app/                         # wiring: config → stores → services → router
  config/                      # strict app YAML, flag > env > file precedence
  auth/                        # request authentication, tokens, built-in principals
  authz/                       # the permission evaluator
  keymaterial/                 # key pair and certificate checks for the security API
  repos/                       # repository operations shared by the API and the descriptor
  descriptor/                  # Artifactory YAML descriptor import, XML descriptor export
  wire/
    artifactory/ access/ legacy/  # JSON wire models with JFrog's field names (tagliatelle ignored here only)
  server/                      # chi router, middleware, auth, baseurl resolver, errors table,
                               # and the hand-written item-path handlers beside the generated ones
    gen/                       # generated strict servers, one package per spec
    apierror/ baseurl/         # error envelopes and the base-URL resolver
    docker/                    # the registry protocol: /v2 routing, blobs and uploads
  sqlstore/
    sqlcommon/                 # Dialect, unit of work, migrator
    sqlite/ postgres/          # dialect packages + migrations/<dialect>/*.sql
  crypt/                       # keyring, sealed columns, key rotation
  remote/                      # upstream fetcher, cache policy, negative cache, singleflight
  format/
    generic/ docker/ npm/ golang/ maven/ pypi/ nuget/ helm/ terraform/
  service/                     # one package per domain service; more arrive with their phases
    gc/ identity/ item/ multipart/ permission/ pkgindex/ registry/ roles/ trash/ usage/
    work/                      # the persistent work queue's worker pool (decision 0033)
  scheduler/                   # Quartz-syntax cron runner for cleanup, replication, GC
  fakeupstream/                # chi fakes of upstream registries with fault modes
  testfault/                   # shared failing fakes for handler tests
  buildinfo/                   # version stamped by make build (from the template)
  openapibundle/ stubgen/      # spec tooling: fragment bundler, 501 stub generator
  specoracle/                  # cached JFrog definitions and the parity comparison
test/
  acceptance/                  # godog features and steps, run on Linux
    features/
  conformance/                 # OCI distribution-spec conformance job wrapper
  terraform/                   # run.sh only: applies the deploy/artifactweave scenarios; builds the provider mirror at run time
docs/
  research/ implementation/ security/ operations/ testing/
```

Naming rules:

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
that CI checks on every pull request and applies on real clouds by manual dispatch. The
project's own lab harness, which no consumer copies, lives apart in `deploy/ci`.

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
      modules/<cloud>/<capability>/   # child modules (storage; replication on AWS), no provider blocks
      aws/ gcp/ azure/
        single-bucket/ multi-region/ geo-replicated/   # scenarios; cdn/ planned (#132-#134)
      multicloud/
        mirror/                       # planned (#135): one mirror with a member on each cloud
  artifactweave/
    README.md                         # managing artifactweave with the JFrog Terraform providers
    terraform/<scenario>/             # repositories, identity, oidc, cdn-repositories, ...
test/
  terraform/run.sh                    # driver: applies a deploy/artifactweave scenario to a -cover server
  platform/                           # real-cloud scenario suite (_test.go only), run by platform-scenarios.yml
```
