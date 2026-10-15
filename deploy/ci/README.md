# The lab harness

What the project uses to test its reference deployments on real clouds. Nothing here is a
reference: the deployments consumers read and copy live in
[`deploy/platform`](../platform/README.md), and the app-layer references in
[`deploy/artifactweave`](../artifactweave/README.md). The layout and the contract are in
decision [0030](../../docs/research/decisions/0030-reference-deployments.md).

| Path | What it is |
|---|---|
| [`lab.mk`](lab.mk) | Every lab constant: the GCP project, the Azure subscription and the bootstrap's resource group, the AWS region and the delegated zone's ID and name. Included by `scenario.mk` and `bootstrap.mk`; override any value on the command line |
| [`terraform/aws`](terraform/aws/README.md), [`terraform/gcp`](terraform/gcp/README.md), [`terraform/azure`](terraform/azure/README.md) | The bootstraps |
| [`bootstrap.mk`](bootstrap.mk) | The make targets the bootstraps share |
| [`scenario.mk`](scenario.mk) | The make targets every `deploy/platform` scenario shares |
| [`check-account.sh`](check-account.sh), [`check-subscription.sh`](check-subscription.sh) | The guards: refuse an AWS run outside the account holding the lab's zone, and an Azure run outside the lab's subscription |
| [`github-environments.sh`](github-environments.sh) | Creates the `cloud-conformance` and `cloud-platform` GitHub environments |
| [`scenario-ci.sh`](scenario-ci.sh) | The CI driver `platform-scenarios.yml` runs: apply, test, destroy, reap, check-clean |
| [`prove-matrix.sh`](prove-matrix.sh), [`prove-matrix_test.sh`](prove-matrix_test.sh) | Which scenarios a pull request proves, and its table test (`make deploy-check` runs it) |

## Bootstraps

Each cloud's bootstrap holds what every scenario on that cloud relies on and outlives them
all: GitHub's trust in a data-plane identity (`cloud-conformance`) and a deployer
(`cloud-platform`), the remote state store, the cloud's DNS zone under
`artifact.weaveplatform.run`, the APIs or resource providers, and a budget. An operator
applies it once, before any scenario, from the repository root:

```bash
make ci-plan         CLOUD=gcp   # against the remote state; an existing bootstrap shows no changes
make ci-apply        CLOUD=gcp   # FIRST_RUN=1 only for the very first apply, before the state store exists
make ci-migrate-state CLOUD=gcp  # after that first apply: moves the state into the store it made
make ci-github-vars  CLOUD=gcp   # environments, repository variables and secrets
```

`azure` and `aws` take the same steps; GCP also has `make ci-undelete-pool CLOUD=gcp`, which
restored the workload identity pool deleted on 2026-10-14. From a bootstrap's own directory
the same targets run without the `ci-` prefix or `CLOUD`.

Each cloud's guard runs first: Azure checks the CLI's subscription and tenant, and AWS checks
that the CLI is signed in to the account holding the delegated hosted zone, so the account ID
stays out of this repository. A bootstrap's state lives in the store it created (its
`backend.tf`). Only a first run with `FIRST_RUN=1` uses local state; otherwise a state probe
that fails stops the run, so an expired login can never plan a bootstrap from empty state.

`make ci-destroy` refuses: CI trust, state and the delegated zones live here. The trusts and
environments are described in
[`docs/testing/ci.md`](../../docs/testing/ci.md#cloud-trust-and-environments), and the
`platform-auth-check.yml` workflow proves the deployer trust on demand.

## The CI driver

`platform-scenarios.yml` runs [`scenario-ci.sh`](scenario-ci.sh) as each cloud's deployer.
Every action works on a fresh copy of the scenario, with the state at
`scenarios/<cloud>/<scenario>/<instance>` in the bootstrap's state store, and the scenario's
`ci-vars.sh` supplies the variables a CI run passes. How to dispatch it and which scenarios a
pull request proves are in [`deploy/platform`](../platform/README.md#running-a-scenario-in-ci)
and [`docs/testing/ci.md`](../../docs/testing/ci.md#platform-scenarios-manual).
