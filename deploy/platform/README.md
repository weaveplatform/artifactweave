# Platform reference deployments

Terraform for the cloud infrastructure artifactweave runs on: object storage, regions and
CDNs. Each scenario is a documented reference deployment that CI checks on every pull
request and that runs on real clouds only when someone dispatches it. The layout, the
scenario contract and the CI identities are in decision
[0030](../../docs/research/decisions/0030-reference-deployments.md).

The lab harness that tests them, each cloud's bootstrap (CI trust, remote state, DNS zone,
budget), the CI driver, the guards and the lab's constants, lives apart in
[`deploy/ci`](../ci/README.md): nothing there is a reference.

What artifactweave is configured with once it runs (repositories, users, permissions,
tokens, OIDC) is the other half, in [`deploy/artifactweave`](../artifactweave/README.md),
applied through the JFrog Terraform providers.

## Catalogue

| Scenario | AWS | GCP | Azure |
|---|---|---|---|
| `single-bucket`: one private bucket or container | [available](terraform/aws/single-bucket/README.md) | [available](terraform/gcp/single-bucket/README.md) | [available](terraform/azure/single-bucket/README.md) |
| `multi-region`: regional stores as artifactweave mirror members | [available](terraform/aws/multi-region/README.md) | [available](terraform/gcp/multi-region/README.md) | [available](terraform/azure/multi-region/README.md) |
| `geo-replicated`: one store with the cloud's own replication | [available](terraform/aws/geo-replicated/README.md) | [available](terraform/gcp/geo-replicated/README.md) | [available](terraform/azure/geo-replicated/README.md) |
| `cdn`: a store behind CloudFront, Cloud CDN or Front Door | planned (#132) | planned (#133) | planned (#134) |

`multicloud/mirror`, one mirror with a member on each cloud, is planned in #135.

The three `single-bucket` scenarios are also the acceptance environments the real-cloud blob
conformance tests run against. An operator's `make platform-apply` grants the bootstrap's
conformance identity; a CI run applies them as the deployer instead (their `ci-vars.sh`).
Every scenario needs its cloud's bootstrap applied first ([`deploy/ci`](../ci/README.md)).

The `multi-region` scenarios follow the scenario contract v2 (decision 0030): they compose
the storage modules in [`terraform/modules`](terraform/modules), one private bucket or storage
account per call with its security invariants tested once in the module's own `tests/`; every
lab value is an input; and each README, written from
[`SCENARIO-TEMPLATE.md`](SCENARIO-TEMPLATE.md), shows the configuration its example inputs
render, which a test keeps true. To use one, copy its directory with `terraform/modules/<cloud>/`
and its lock file, or source the modules from this repository at a release; the README's
"Getting the code" has the line. The `single-bucket` and `geo-replicated` scenarios move onto
the same contract with epic #189 (#184, #185, #186).

Every scenario runs locally the same way, from its own `artifactweave_config` output (below).
The `geo-replicated` READMEs cover how the cloud's replica relates to artifactweave and how to
fail over.

## Running a scenario

Every scenario is a Terraform root with a Makefile built on
[`deploy/ci/scenario.mk`](../ci/scenario.mk), which takes the lab's values from
[`deploy/ci/lab.mk`](../ci/lab.mk) and runs the cloud's guard first: on AWS, that the CLI is
signed in to the account holding the lab's zone; on Azure, that the CLI is on the lab's
subscription. `plan`, `apply` and `destroy` pass the scenario's own inputs from its Makefile
and the lab's from [`deploy/ci/lab-vars.sh`](../ci/lab-vars.sh), resolved with your CLI: the
account scope, the bootstrap's conformance identity as the app principal, on Azure you as an
additional principal, and `force_destroy`. From the scenario's directory, `make help` lists its targets; from the
repository root, `make platform-<target> SCENARIO=<cloud>/<scenario>` runs the same target:

```bash
make platform-plan    SCENARIO=gcp/single-bucket
make platform-apply   SCENARIO=gcp/single-bucket
make platform-up      SCENARIO=gcp/single-bucket   # artifactweave natively, from its artifactweave_config
make platform-smoke   SCENARIO=gcp/single-bucket   # a round trip and a downloadRedirect 302
make platform-verify  SCENARIO=gcp/single-bucket   # test/platform, exactly as CI runs it
make platform-down    SCENARIO=gcp/single-bucket
make platform-destroy SCENARIO=gcp/single-bucket
```

`up`, `smoke` and `verify` work for every scenario, mirrors included, because they use the
configuration the scenario renders rather than one written by hand. They need the identity the
scenario grants, which `credentials` (run by `up` and `verify`) sets up: it impersonates the
app service account on GCP, exports your `aws login` on AWS, and uses your `az login` on Azure. The local runner, its files and the Docker volumes the old local
container left behind are in [`deploy/ci`](../ci/README.md#running-a-scenario-locally).

`make gcp-<target>`, `make azure-<target>` and `make aws-<target>` are short forms for the
three `single-bucket` scenarios.

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
[`deploy/ci/scenario-ci.sh`](../ci/scenario-ci.sh), and each scenario's `ci-vars.sh`
holds the variables a CI run passes.

A pull request that changes the workflow, the driver, `test/platform`, a scenario or a
module runs the whole cycle behind the same approval, for the scenarios
[`deploy/ci/prove-matrix.sh`](../ci/prove-matrix.sh) chooses from its changed paths:
every cloud's `single-bucket` when the driver changes, the scenarios a `test/platform` file
names, every scenario that sources a changed module, and each scenario whose Terraform or
`ci-vars.sh` changes. READMEs, Makefiles and mocked tests prove nothing, and the `prove-all`
label proves every scenario ([the full rules](../../docs/testing/ci.md#platform-scenarios-manual)).
Each proof has its own run tag, so proofs run in
parallel and each one's `reap` and final check see only its own instances. On AWS, state
lives in the S3 state bucket and buckets are found by their tags in whichever region they
are; reap empties a versioned bucket (geo-replicated's) of its versions before deleting it.

## Checks

`make deploy-check` runs what the `deploy-terraform` CI job runs over every root:
`terraform fmt`, `validate`, tflint, trivy, the mocked `terraform test` suites in each
root's `tests/`, and a check that each README's generated section is current
(`make deploy-docs` rewrites it). See [`docs/testing/ci.md`](../../docs/testing/ci.md).
