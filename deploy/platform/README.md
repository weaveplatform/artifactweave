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

The two `single-bucket` scenarios are the acceptance environments the real-cloud blob
conformance tests run against. They predate the scenario contract: their server
configuration is rendered by `credentials.sh` rather than a Terraform output, and they carry
no `expires-at` label yet. Both arrive with the scenario workflow (#129).

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

## Checks

`make deploy-check` runs what the `deploy-terraform` CI job runs over every root:
`terraform fmt`, `validate`, tflint, trivy, the mocked `terraform test` suites in each
root's `tests/`, and a check that each README's generated section is current
(`make deploy-docs` rewrites it). See [`docs/testing/ci.md`](../../docs/testing/ci.md).
