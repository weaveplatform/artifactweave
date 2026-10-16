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
| [`lab-vars.sh`](lab-vars.sh) | The inputs an operator run passes besides the scenario's Makefile, resolved with the operator's CLI and sourced by `scenario.mk`'s `plan`, `apply` and `destroy`: the account scope, the bootstrap's conformance identity as the app principal, on Azure the signed-in operator as an additional principal, for `aws/cdn` a CloudFront key pair kept in the scenario's `.secrets/cloudfront/`, for `gcp/cdn` a signed URL key kept in `.secrets/cloud-cdn/` and `signed_url_key_added` once the backend bucket holds it, for `azure/cdn` the operator's public address as the one network its account answers, and `force_destroy=true` |
| [`cloudfront-key.sh`](cloudfront-key.sh) | Makes the CloudFront key pair a CDN scenario signs with, outside Terraform: per instance for `scenario-ci.sh`, once per scenario directory for `lab-vars.sh` |
| [`cloud-cdn-key.sh`](cloud-cdn-key.sh) | Makes the Cloud CDN signed URL key `gcp/cdn` signs with, outside Terraform, and adds it to the backend bucket with `gcloud`: per instance for `scenario-ci.sh`, once per scenario directory for `lab-vars.sh` and `make signed-url-key` |
| [`check-account.sh`](check-account.sh), [`check-subscription.sh`](check-subscription.sh) | The guards: refuse an AWS run outside the account holding the lab's zone, and an Azure run outside the lab's subscription |
| [`github-environments.sh`](github-environments.sh) | Creates the `cloud-conformance` and `cloud-platform` GitHub environments |
| [`scenario-ci.sh`](scenario-ci.sh) | The CI driver `platform-scenarios.yml` runs: apply, test, destroy, reap, check-clean |
| [`platform-tests.sh`](platform-tests.sh) | Runs `test/platform` against an applied scenario: the test step of `scenario-ci.sh test` and of `make verify`, so a local check is the check CI makes |
| [`local.sh`](local.sh) | The local runner behind `scenario.mk`'s `render`, `credentials`, `up`, `smoke`, `verify`, `logs`, `down` and `clean` |
| [`prove-matrix.sh`](prove-matrix.sh), [`prove-matrix_test.sh`](prove-matrix_test.sh) | Which scenarios a pull request proves, and its table test (`make deploy-check` runs it) |
| [`lint-contract.sh`](lint-contract.sh) | The reference-deployment contract: checks every Terraform root under `deploy/` against decision 0030's rules for its kind (`make deploy-check` runs it and its `--self-test`) |

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
`ci-vars.sh` supplies the variables a CI run passes. Every name a run gives its resources
starts with the driver's `NAME_PREFIX`: `artifactweave-awscn-` on AWS, where the deployer may
manage only `artifactweave-*` buckets, and `awscn-` on GCP and Azure. A `multicloud` run logs
in as all three deployers, keeps its state in GCP's store and takes AWS's prefix, and its reap
sweeps all three clouds for the multicloud scenarios' resources only. How to dispatch it and which scenarios a
pull request proves are in [`deploy/platform`](../platform/README.md#running-a-scenario-in-ci)
and [`docs/testing/ci.md`](../../docs/testing/ci.md#platform-scenarios-manual).

## Running a scenario locally

[`local.sh`](local.sh) runs artifactweave natively against an applied scenario, from the
scenario's own `artifactweave_config` output, so the local run uses exactly the configuration
the reference documents and works for every scenario, mirrors included. From a scenario
directory, or `make platform-<target> SCENARIO=<cloud>/<scenario>` from the root:

| Target | What it does |
|---|---|
| `render` | Writes `.local/artifactweave.yaml` from `artifactweave_config` and `.local/outputs.json`; generates `.local/secrets.env` (master key, admin password) once and keeps it, because the database is sealed with that key |
| `credentials` | Lets this machine act as the scenario's app identity. GCS: `.local/gcp-credentials.json` impersonates the configuration's `google_access_id` from your Application Default Credentials (the lab forbids service account keys). S3: `.local/aws.env` from `aws configure export-credentials`, short-lived. Azure: nothing; the SDK chain uses your `az login` |
| `up` | Builds `.local/artifactweave` and starts it in the background on `127.0.0.1:$PORT` (8081 on GCP, 8082 on Azure, 8083 on AWS by default), then waits until it is ready |
| `smoke` | A 40 MiB round trip, then a `downloadRedirect` repository's 302, which must land on the scenario's storage host and serve the same bytes |
| `verify` | `test/platform` against the applied scenario, through [`platform-tests.sh`](platform-tests.sh) as CI runs it |
| `logs`, `down`, `clean` | Follow the log; stop artifactweave, keeping `.local/data`; stop it and remove `.local` |

Everything lands in `.local/`, which ignores itself. The Docker container these targets
replaced left volumes behind (`acceptance_data`, `acceptance-azure_data`,
`acceptance-aws_data`); remove them with `docker volume rm` once you no longer need their data.
