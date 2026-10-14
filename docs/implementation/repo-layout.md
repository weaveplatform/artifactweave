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
    fs/ s3/                    # backends
    blobtest/                  # contract suite
  store/                       # store contracts, sentinel errors, cursors
    inmem/                     # reference in-memory backend
    storetest/                 # contract suite
  client/
    artifactory/ access/ event/  # generated Go clients (oapi-codegen), used by tests
internal/
  layout/                      # import-tier enforcement test
  app/                         # wiring: config → stores → services → router
  config/                      # strict app YAML, flag > env > file precedence
  descriptor/                  # Artifactory YAML descriptor import, XML descriptor export
  wire/
    artifactory/ access/ legacy/  # JSON wire models with JFrog's field names (tagliatelle ignored here only)
  server/                      # chi router, middleware, auth, baseurl resolver, errors table,
                               # and the hand-written item-path handlers beside the generated ones
    gen/                       # generated strict servers, one package per spec
    docker/                    # /v2 routing for the repository-path access method
  sqlstore/
    sqlcommon/                 # Dialect, unit of work, migrator
    sqlite/ postgres/          # dialect packages + migrations/<dialect>/*.sql
  crypt/                       # keyring, sealed columns, key rotation
  remote/                      # upstream fetcher, cache policy, negative cache, singleflight
  format/
    generic/ docker/ npm/ golang/ maven/ pypi/ nuget/ helm/ terraform/
  service/
    repo/ item/ pkgindex/ search/ security/ token/ project/ webhook/ retention/ build/ system/
  scheduler/                   # Quartz-syntax cron runner for cleanup, replication, GC
  fakeupstream/                # chi fakes of upstream registries with fault modes
  testfault/                   # shared failing fakes for handler tests
  buildinfo/                   # version stamped by make build (from the template)
test/
  acceptance/                  # godog features and steps, run on Linux
    features/
  conformance/                 # OCI distribution-spec conformance job wrapper
  terraform/                   # jfrog provider fixtures and the pre-populated provider mirror
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
that CI checks on every pull request and applies on real clouds by manual dispatch.

```text
deploy/
  platform/
    README.md                         # scenario catalogue, costs, how CI runs them
    terraform/
      scenario.mk                     # shared make targets for every scenario
      modules/<cloud>/<capability>/   # child modules (storage, cdn, ci-access), no provider blocks
      aws/ gcp/ azure/
        bootstrap/                    # long-lived CI identities, remote state, DNS zone, budget
        single-bucket/ multi-region/ geo-replicated/ cdn/   # scenarios
      multicloud/
        mirror/                       # one mirror with a member on each cloud
  artifactweave/
    README.md                         # managing artifactweave with the JFrog Terraform providers
    terraform/<scenario>/             # repositories, identity, cdn-repositories, ...
test/
  terraform/run.sh                    # driver: applies a deploy/artifactweave scenario to a -cover server
  platform/                           # real-cloud scenario suite (_test.go only), run by platform-scenarios.yml
```
