# Platform reference deployments

Terraform for the cloud infrastructure artifactweave runs on: object storage, regions, CDNs
and the CI trust that tests them. Each scenario is a documented reference deployment that
CI checks on every pull request and that runs on real clouds only when someone dispatches
it. The layout, the scenario contract and the CI identities are in decision
[0030](../../docs/research/decisions/0030-reference-deployments.md).

What artifactweave is configured with once it runs (repositories, users, permissions,
tokens, OIDC) is the other half, in [`deploy/artifactweave`](../artifactweave/README.md),
applied through the JFrog Terraform providers.

## Catalogue

| Scenario | AWS | GCP | Azure |
|---|---|---|---|
| `bootstrap`: CI identities, remote state, DNS zone, budget; applied once by an operator, not a scenario | planned (#128) | [available](terraform/gcp/bootstrap/README.md) | [available](terraform/azure/bootstrap/README.md) |
| `single-bucket`: one private bucket or container | planned (#128) | [available](terraform/gcp/single-bucket/README.md) | [available](terraform/azure/single-bucket/README.md) |
| `multi-region`: two regional stores as artifactweave mirror members | planned (#130) | planned (#130) | planned (#130) |
| `geo-replicated`: one store with the cloud's own replication | planned (#131) | planned (#131) | planned (#131) |
| `cdn`: a store behind CloudFront, Cloud CDN or Front Door | planned (#132) | planned (#133) | planned (#134) |

`multicloud/mirror`, one mirror with a member on each cloud, is planned in #135.

The two `single-bucket` scenarios are also the acceptance environments the real-cloud blob
conformance tests run against. An operator's `make platform-apply` grants the bootstrap's
conformance identity; a CI run applies them as the deployer instead (their `ci-vars.sh`).

## Bootstraps

Each cloud's `bootstrap` holds what every scenario on that cloud relies on and outlives
them all: GitHub's trust in a data-plane identity (`cloud-conformance`) and a deployer
(`cloud-platform`), the remote state store, the cloud's DNS zone under
`artifact.weaveplatform.run`, the APIs or resource providers, and a budget. Apply it once,
before any scenario:

```bash
make platform-undelete-pool SCENARIO=gcp/bootstrap   # GCP only, once: restores the pool deleted on 2026-10-14
make platform-apply         SCENARIO=gcp/bootstrap   # first run uses local state
make platform-migrate-state SCENARIO=gcp/bootstrap   # then moves it into the bucket it made
make platform-github-vars   SCENARIO=gcp/bootstrap   # environments and repository variables
```

`make platform-destroy` refuses on a bootstrap. The trusts and environments are described in
[`docs/testing/ci.md`](../../docs/testing/ci.md#cloud-trust-and-environments), and the
`platform-auth-check.yml` workflow proves the deployer trust on demand.

## Running a scenario

Every scenario is a Terraform root with a Makefile built on
[`terraform/scenario.mk`](terraform/scenario.mk). From the scenario's directory, `make help`
lists its targets; from the repository root, `make platform-<target>
SCENARIO=<cloud>/<scenario>` runs the same target:

```bash
make platform-plan  SCENARIO=gcp/single-bucket
make platform-apply SCENARIO=gcp/single-bucket
make platform-up    SCENARIO=gcp/single-bucket   # artifactweave in a local container against it
make platform-smoke SCENARIO=gcp/single-bucket
make platform-destroy SCENARIO=gcp/single-bucket
```

`make gcp-<target>` and `make azure-<target>` remain as short forms for the two
`single-bucket` scenarios.

Applying a scenario creates real, billed resources in the account your CLI is signed in to.
Destroy it when you're done.

## Running a scenario in CI

`platform-scenarios.yml` applies, tests and destroys scenarios on real clouds as each
cloud's deployer, from the `cloud-platform` environment. Dispatch it from the Actions tab or
the CLI, then approve the run when GitHub asks (Review deployments, `cloud-platform`):

```bash
gh workflow run platform-scenarios.yml -f cloud=gcp -f scenario=single-bucket -f action=test
gh workflow run platform-scenarios.yml -f cloud=azure -f action=apply -f instance=explore1 -f ttl_minutes=120
gh workflow run platform-scenarios.yml -f cloud=azure -f action=destroy -f instance=explore1
gh workflow run platform-scenarios.yml -f cloud=gcp -f action=reap
```

| Action | Does |
|---|---|
| `test` | applies a fresh instance, starts artifactweave against it, runs [`test/platform`](../../test/platform), and destroys the instance in a step that always runs |
| `apply` | creates an instance and leaves it, labelled to expire after `ttl_minutes` (default 240); the run's summary names it |
| `destroy` | destroys a named instance from its remote state |
| `reap` | destroys every instance of the cloud whose `expires-at` has passed, and anything labelled or `awscn-`-prefixed that outlived its state |

An instance is billed while it exists. `test` cleans up after itself; after an `apply`,
dispatch `destroy`, or let a `reap` remove it once it expires. Instance state lives in the
bootstrap's state store under `scenarios/<cloud>/<scenario>/<instance>`. The driver is
[`terraform/scenario-ci.sh`](terraform/scenario-ci.sh), and each scenario's `ci-vars.sh`
holds the variables a CI run passes.

A pull request that changes the workflow, the driver, `test/platform` or a `single-bucket`
scenario runs the whole cycle for GCP and Azure behind the same approval, and checks that
nothing of the run is left.

## Checks

`make deploy-check` runs what the `deploy-terraform` CI job runs over every root:
`terraform fmt`, `validate`, tflint, trivy, the mocked `terraform test` suites in each
root's `tests/`, and a check that each README's generated section is current
(`make deploy-docs` rewrites it). See [`docs/testing/ci.md`](../../docs/testing/ci.md).
