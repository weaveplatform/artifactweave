# CI

This page describes the workflows in `.github/workflows/` and the jobs the
[implementation plan](../implementation/plan.md) adds to them.

## Implemented checks (template)

| Workflow | Checks |
|---|---|
| `quality-gate.yml` | Vet and race/shuffled unit tests on `ubuntu-latest`, `macos-latest` and `windows-latest`; blocking golangci-lint followed by `make verify-generate` (bundled specs, generated servers, clients and 501 stubs must be committed current); govulncheck; cross-compile of every `cmd/*` binary; merged coverage gate (95% total, 90% per package) |
| `pr-title-validation.yml` | Conventional PR title |
| `dependency-review.yml` | GitHub Dependency Review |
| `deps-refresh.yml`, `auto-merge.yml`, `release-please.yml` | Dependency currency, auto-merge once the gate passes, releases |
| `cloud-conformance.yml` (manual) | The blob contract and probes against real GCS and Azure; see [Real-cloud conformance](#real-cloud-conformance-manual) |
| `platform-auth-check.yml` (manual, and on pull requests that change a bootstrap) | Proves GitHub's trust in the deployer identities; see [Cloud trust and environments](#cloud-trust-and-environments) |
| `platform-scenarios.yml` (manual, and on pull requests that change it, its driver, `test/platform` or a `single-bucket` scenario) | Applies, tests, destroys and reaps reference deployments on real clouds as the deployer; see [Platform scenarios](#platform-scenarios-manual) |

`GOWORK=off` isolates builds from parent workspaces. Actions are pinned by commit SHA.

## Jobs added by this project

| Job | Runner | Added in | What |
|---|---|---|---|
| `acceptance` | `ubuntu-latest` | P0 | Builds `artifactweave` with `-cover`, runs godog features with pinned `jf`, `terraform`, `helm`, `crane`, `oras`, `cosign` binaries and container-backed Maven, .NET, Node and Python clients; uploads `cover-accept` |
| `parity` | `ubuntu-latest` | P0 | Restores or fetches the gitignored `.specoracle` cache of JFrog's published OpenAPI blocks and runs `TestSpecParity` against `api/parity-baseline.json`. Required since P1 (in the gate's `needs`): the baseline lists only out-of-scope products. docs.jfrog.com is contacted only on a cache miss |
| `conformance` | `ubuntu-latest` | P6 | Starts the `-cover` binary with a `docker-local` repo, clones `opencontainers/distribution-spec` at a pinned tag and runs its conformance module with `OCI_ROOT_URL`, `OCI_NAMESPACE=<repo>/<image>` and credentials; uploads `cover-conformance` |
| `terraform` | `ubuntu-latest` | P3 | Installs the pinned `jfrog/artifactory` and `jfrog/platform` providers from a filesystem mirror of their release zips (checked against the release SHA-256s and the committed lock files) and validates each scenario in `deploy/artifactweave/terraform`: repositories, identity and oidc. Then it starts one `-cover` binary, mints an admin access token, and for each scenario applies, requires an empty `terraform plan` and destroys (`make terraform TERRAFORM_MODE=apply`, from P4, #69, #85 and #83); uploads `cover-terraform` |
| `deploy-terraform` | `ubuntu-latest` | P4b | `make deploy-check` over every Terraform root in `deploy/` and `test/terraform`: `terraform fmt -check`, `terraform validate`, tflint with the aws, google and azurerm rulesets, `trivy config`, `terraform test` against mocked providers, and the terraform-docs drift check (#124, decision 0030). Needs no cloud credentials |
| `acceptance-peers` | `ubuntu-latest` | P13 | Starts two or three `-cover` instances on separate ports and data directories, pairs them, and runs the `@peers` godog features: federated repository convergence, partition and full sync (P13), Access Federation and topology (P14), release bundle distribution to an edge (P15). Kept apart from `acceptance` so the per-PR budget holds; uploads `cover-accept-peers` and joins the coverage and gate `needs` |
| `e2e` (nightly) | `ubuntu-latest` | P9 | `-tags e2e` acceptance against real upstream registries |

`acceptance`, `acceptance-peers`, `conformance` and `terraform` are listed in the coverage job's `needs`
and in the `gate` job's `needs`, so the merged coverage includes them and a failure blocks merge.
`deploy-terraform` is in the `gate` job's `needs` only: it runs no Go code, so it adds no
coverage.

## Why acceptance runs on Linux only

GitHub's macOS runners have no Docker daemon and the Windows runners only run Windows
containers. testcontainers-go, MinIO, PostgreSQL and the client containers therefore run
only on `ubuntu-latest`. Unit suites that need Docker call `t.Skip` when the daemon is
unreachable, so the macOS and Windows matrix legs stay green and cover the same packages
through fakes. See decision
[0015](../research/decisions/0015-testing-and-acceptance.md).

The cross-cloud blob mirror scenario (`TestCrossCloud` in `pkg/blob/mirror`) is one of
those unit suites. It runs MinIO, fake-gcs-server and Azurite side by side in the Linux
unit job and skips on macOS and Windows; the emulator images are pinned in
`pkg/containertest`.

## Transient infrastructure failures

Two failures came from the runners rather than the code, and each needed a manual rerun
(#148). CI now absorbs both.

| Failure | Response |
|---|---|
| `create container: reaper: … wait for reaper …: context deadline exceeded` | `quality-gate.yml` sets `TESTCONTAINERS_RYUK_DISABLED=true`, so testcontainers-go never starts its reaper (Ryuk). Hosted runners are discarded after each job, so nothing is left behind. The emulators terminate at the end of each test through `testcontainers.CleanupContainer`. The PostgreSQL container that `internal/sqlstore/sqltest` shares across a test binary stays up until the runner goes. Local runs keep the reaper, which removes that container when the binary exits |
| `read "https://proxy.golang.org/…": stream error: … INTERNAL_ERROR` | Every Go job runs `.github/actions/go-mod-download` straight after `actions/setup-go`. It runs `go mod download`, retrying twice with 15 s and 30 s pauses, so lint, tests, `go tool` and golangci-lint then read the module cache and never fetch. Only the download is retried: a failing test or lint step still fails |

## Real-cloud conformance (manual)

`cloud-conformance.yml` runs the blob contract (`blobtest.Run`) and the research 18
probes against a real Google Cloud Storage bucket, a real Azure Blob Storage container
and a real Amazon S3 bucket. fake-gcs-server, Azurite and MinIO do not enforce compose
preconditions, copy-source authorisation, signed URLs, S3's copy limits or service
limits, so this is the only check of them. The workflow is not part of the gate: it needs
cloud credentials and is started from the Actions tab (**Run workflow**, target `all`,
`gcs`, `azure` or `s3`). Each run
writes under `conformance/<random>/` and deletes that prefix when it ends. The step
summary lists the `PROBE` lines that answer the open questions in
[research 18 §4](../research/18-cloud-blob-storage.md#4-open-questions-for-a-real-cloud-check).
A run whose variables or secrets are missing fails rather than skipping. Both jobs run
in the `cloud-conformance` environment, because the clouds trust that environment's
subject and nothing else (see [Cloud trust and environments](#cloud-trust-and-environments)).

| Name | Kind | Value |
|---|---|---|
| `CONFORMANCE_GCS_BUCKET` | variable | The bucket (`terraform output bucket` in `deploy/platform/terraform/gcp/single-bucket`) |
| `CONFORMANCE_GCP_SERVICE_ACCOUNT` | variable | The data-plane service account the job acts as (`terraform output conformance_service_account` in `gcp/bootstrap`). `gcp/single-bucket` grants it `roles/storage.objectAdmin` on the bucket, and it signs URLs through IAM `signBlob` as itself |
| `CONFORMANCE_GCP_WIF_PROVIDER` | variable | The Workload Identity Federation provider (`terraform output workload_identity_provider` in `gcp/bootstrap`). The job exchanges its GitHub OIDC token for the service account, so no key is stored; organisations enforcing `iam.disableServiceAccountKeyCreation` allow no keys anyway |
| `CONFORMANCE_AZURE_ACCOUNT` | variable | The storage account (`terraform output account` in `deploy/platform/terraform/azure/single-bucket`). It allows no Shared Key |
| `CONFORMANCE_AZURE_CONTAINER` | variable | The container in that account |
| `CONFORMANCE_AZURE_CLIENT_ID`, `CONFORMANCE_AZURE_TENANT_ID`, `CONFORMANCE_AZURE_SUBSCRIPTION_ID` | variables | The data-plane managed identity the job logs in as with `azure/login` (OpenID Connect), from `azure/bootstrap`. `azure/single-bucket` grants it Storage Blob Data Contributor on the account |
| `CONFORMANCE_S3_BUCKET` | variable | The bucket (`terraform output bucket` in `deploy/platform/terraform/aws/single-bucket`) |
| `CONFORMANCE_AWS_ROLE_ARN` | secret | The data-plane role the job assumes with `aws-actions/configure-aws-credentials` (OpenID Connect), from `aws/bootstrap`. A secret rather than a variable because the ARN carries the account ID, which the public workflow logs must not show; the action also masks the account ID. The role has no policy of its own: `aws/single-bucket`'s bucket policy grants it that bucket's objects |
| `CONFORMANCE_AWS_REGION` | variable | The role's region |

`make platform-github-vars SCENARIO=gcp/bootstrap` (and `azure/bootstrap`,
`aws/bootstrap`) sets the identity variables; `make gcp-github-vars`,
`make azure-github-vars` and `make aws-github-vars` set the bucket and account.

Give every store a lifecycle rule that deletes `conformance/` objects after a day, in
case a cancelled run skips its cleanup; on S3, also abort incomplete multipart uploads,
which are billed until aborted. On GCS, a bucket with soft delete enabled bills
the deleted temporary parts for the retention period.

The same tests run from a workstation. They skip unless their variables are set:

```bash
make gcp-conformance     # or, by hand, after make gcp-credentials (writes .secrets/container-credentials.json):
ARTIFACTWEAVE_TEST_GCS_BUCKET=$(terraform -chdir=deploy/platform/terraform/gcp/single-bucket output -raw bucket) \
ARTIFACTWEAVE_TEST_GCS_ACCESS_ID=$(terraform -chdir=deploy/platform/terraform/gcp/single-bucket output -raw service_account) \
GOOGLE_APPLICATION_CREDENTIALS=deploy/platform/terraform/gcp/single-bucket/.secrets/container-credentials.json \
  go test -count=1 -v -run 'TestContractOnRealGCS|TestRealGCSProbes' ./pkg/blob/gcs/

make aws-conformance     # or, by hand, after aws login:
ARTIFACTWEAVE_TEST_S3_BUCKET=$(terraform -chdir=deploy/platform/terraform/aws/single-bucket output -raw bucket) \
ARTIFACTWEAVE_TEST_S3_REGION=eu-west-2 \
  go test -count=1 -v -run 'TestContractOnRealS3|TestRealS3Probes' ./pkg/blob/s3/

make azure-conformance   # or, by hand, after az login:
ARTIFACTWEAVE_TEST_AZURE_ACCOUNT=$(terraform -chdir=deploy/platform/terraform/azure/single-bucket output -raw account) \
ARTIFACTWEAVE_TEST_AZURE_CONTAINER=artifactweave \
  go test -count=1 -v -run 'TestContractOnRealAzure|TestRealAzureProbes' ./pkg/blob/azure/
```

The GCS run acts as the conformance service account through the impersonated credentials
`up.sh` writes from your own Application Default Credentials
(`gcloud auth application-default login`). `deploy/platform/terraform/gcp/bootstrap`
creates the account, lets your login act as it, and holds the federation for the workflow;
`gcp/single-bucket` creates the bucket and grants the account.

The S3 run uses the AWS default credential chain: your `aws login` session locally, the
role `configure-aws-credentials` assumes in the workflow. Locally it acts as your own login
rather than the conformance role, which only GitHub's OIDC token can assume. The contract
lowers the copy limits, so committing its large blob takes the `UploadPartCopy` path that
objects over 5 GiB take on S3.

The Azure run authenticates with `DefaultAzureCredential`: your `az login` locally, the
`azure/login` OIDC session in the workflow. Both hold Storage Blob Data Contributor on the
account, and the probes sign copy sources with a user delegation SAS from it. An
`ARTIFACTWEAVE_TEST_AZURE_ACCOUNT_KEY`, for an account that still allows Shared Key, switches
the contract and probes to that key instead. `make azure-conformance` refuses to run unless
the Azure CLI's active subscription is the acceptance one
(`deploy/platform/terraform/azure/check-subscription.sh`, shared by the azure roots).

## Cloud trust and environments

Each cloud's bootstrap (`deploy/platform/terraform/<cloud>/bootstrap`, decision 0030)
holds GitHub's trust, in two GitHub environments:

| Environment | Identity on each cloud | Rights | Used by |
|---|---|---|---|
| `cloud-conformance` | data plane: `artifactweave-conformance` | objects in the `single-bucket` bucket or account only | `cloud-conformance.yml` |
| `cloud-platform` | deployer: `artifactweave-deployer` | create and destroy scenario resources: the GCP lab project's storage, load balancing, Certificate Manager and the scenarios' DNS zone; Azure's `artifactweave-scenarios-rg` and DNS zone; AWS buckets named `artifactweave-*`, CloudFront, ACM, the scenarios' hosted zone and roles under `/artifactweave-scenarios/`, inside a permissions boundary every role it creates must carry; each cloud's state store | `platform-auth-check.yml`, `platform-scenarios.yml` |

Every trust matches this repository's immutable OIDC subject,
`repo:weaveplatform@336817058/artifactweave@1415179278:environment:<env>`, exactly: GCP's
provider admits only the two environment subjects and each service account trusts one;
each Azure identity has one federated credential for one subject; each AWS role's trust
policy matches one subject with `StringEquals`. `cloud-platform` asks
the maintainer to approve every run, from any branch, so a pull request can prove its own
scenario; self-approval is allowed. `make platform-github-vars SCENARIO=<cloud>/bootstrap`
creates both environments (`deploy/platform/terraform/github-environments.sh`) and sets
these variables:

| Name | Value |
|---|---|
| `PLATFORM_GCP_WIF_PROVIDER`, `PLATFORM_GCP_SERVICE_ACCOUNT` | The provider and the deployer service account |
| `PLATFORM_GCP_PROJECT`, `PLATFORM_GCP_STATE_BUCKET` | The lab project and the remote state bucket |
| `PLATFORM_AZURE_CLIENT_ID`, `PLATFORM_AZURE_TENANT_ID`, `PLATFORM_AZURE_SUBSCRIPTION_ID` | The deployer identity, for `azure/login` |
| `PLATFORM_AZURE_RESOURCE_GROUP`, `PLATFORM_AZURE_STATE_ACCOUNT` | The scenarios resource group and the remote state account |
| `PLATFORM_AWS_ROLE_ARN` (secret), `PLATFORM_AWS_REGION` | The deployer role, for `configure-aws-credentials`, stored as a secret because the ARN carries the account ID; and the region |
| `PLATFORM_AWS_STATE_BUCKET`, `PLATFORM_AWS_DNS_ZONE_ID` | The remote state bucket and the scenarios' hosted zone |

`platform-auth-check.yml` (`cloud-platform`; dispatched by hand, and run on pull requests
that change a bootstrap, after the maintainer approves) logs in to all three clouds as the
deployer and makes read-only calls that need its rights: the GCS state bucket and the DNS
zone's records, the scenarios resource group and the Azure state container, and the S3
state bucket and the hosted zone's records. It creates nothing.

## Platform scenarios (manual)

`platform-scenarios.yml` runs the reference deployments in `deploy/platform/terraform` on
real clouds, from the `cloud-platform` environment, so the maintainer approves every run.
Its inputs are `cloud`, `scenario`, `action` (`test`, `apply`, `destroy` or `reap`),
`instance` and `ttl_minutes`; [`deploy/platform/README.md`](../../deploy/platform/README.md#running-a-scenario-in-ci)
shows how to dispatch each. Every action goes through `deploy/platform/terraform/scenario-ci.sh`:

- Terraform 1.16.4, pinned by SHA-256 as in the `terraform` job, works on a fresh copy of
  the scenario with a backend generated for the instance's state, at
  `scenarios/<cloud>/<scenario>/<instance>` in the bootstrap's state store. On Azure the
  provider and backend exchange the job's OIDC token themselves (`ARM_USE_OIDC`). On AWS,
  `configure-aws-credentials` assumes the deployer from the `PLATFORM_AWS_ROLE_ARN` secret,
  with the account ID masked, and the S3 backend locks with `use_lockfile`.
- `test` builds artifactweave, starts it with the scenario's `artifactweave_config` output,
  and runs `test/platform`: a 40 MiB deploy and round trip, a ranged GET, the storage
  API's checksums, a `HEAD` answered by the registry, a `downloadRedirect` 302 to the
  scenario's `storage_host` whose signed URL serves the file and a range of it, and the
  same URL refused without its signature. Scenarios add their own checks in
  `test/platform`. The instance is destroyed in a step that always runs.
- `reap` destroys expired instances and deletes resources that outlived their state.
- State and plans are never uploaded, and nothing uploads coverage: scenario runs are
  evidence, not coverage, so `make test` leaves `test/platform` out.

On a pull request that changes the workflow, the driver, `test/platform` or a
`single-bucket` scenario, the same job runs a proof for GCP and Azure (`single-bucket`; AWS is
wired for dispatch and joins the proof once a dispatched run has proven it):
`test`; an `apply`, then a `destroy` that has only the remote state; an instance applied
already expired, then a `reap` that removes it; then `check-clean`, which fails if any
state, bucket or resource of the run remains. A failed step triggers a clean-up step that
destroys the run's instances.

## Terraform scenarios

`make terraform` runs `test/terraform/run.sh` over `TERRAFORM_SCENARIOS`: the repositories,
identity and OIDC references in `deploy/artifactweave/terraform/`. `test/terraform/` holds only
the driver.
- It downloads `terraform-provider-artifactory_<version>_<os>_<arch>.zip` and
  `terraform-provider-platform_<version>_<os>_<arch>.zip` once into a filesystem mirror,
  checking each against the SHA-256 pinned in the script.
- It points `TF_CLI_CONFIG_FILE` at a CLI configuration that installs both providers only
  from that mirror.
- For each scenario it runs `terraform init -lockfile=readonly` and `terraform validate`.

Each scenario's committed `.terraform.lock.hcl` holds its providers' hashes for linux and
darwin on amd64 and arm64. To move a provider to a new version:
1. Update the version in every scenario's `main.tf`.
2. Update the version and checksums in `run.sh` from the release's `SHA256SUMS`.
3. Regenerate each lock from a mirror holding the four zips of each provider:
   `terraform providers lock -fs-mirror=<mirror> -platform=...`.

`make terraform TERRAFORM_MODE=apply` adds the full run, which the `terraform` CI job runs:
1. Build and start one `-cover` binary, writing its coverage to `cover/terraform`.
2. Mint an admin access token with the bootstrap admin's password
   (`POST /access/api/v1/tokens`). The providers authenticate only with a Bearer token.
   Set `TERRAFORM_ACCESS_TOKEN` to supply one instead.
3. Apply each scenario with its own steps:
   - **repositories:** apply the repository, deploy `fixture.txt`, then apply its
     properties;
   - **identity:** generate an RSA key pair for `artifactory_keypair`, apply, then check
     that the permission written through Access v2 reads back through the legacy v2
     permission target API;
   - **oidc:** apply the OIDC providers and their identity mappings.
4. Require `terraform plan -detailed-exitcode` to exit 0.
5. Destroy.

Terraform runs on a copy of each scenario in the work directory, so its state never lands
in the source tree. The empty plan is the check that matters: the providers read back
every attribute they wrote. The repositories fixture found two gaps in the repository API,
both fixed in #69. The server echoed the boolean flags JFrog documents as strings in that
string form, which the provider cannot decode. It also left out `environments` when a
repository has none, which the provider reads as null against its default of an empty
set. The identity scenario found that the provider creates key pairs with
`POST /artifactory/api/security/keypair/`, with a trailing slash, which #85 routes.

`artifactory_permission_target` is a stub in provider 12.11.14: every operation returns
"deprecated. Use platform_permission instead". The identity scenario uses
`platform_permission` and reads the result back with the `artifactory_permission_target`
data source, which still calls `/artifactory/api/v2/security/permissions/{name}`.

## Deployment Terraform checks

`deploy/check.sh` (`make deploy-check`, and the `deploy-terraform` job) checks every
directory under `deploy/` and `test/terraform` that holds `*.tf` files. Roots are found,
not listed, so moving one needs no change to the script.

| Step | What |
|---|---|
| `terraform fmt -check -recursive` | Formatting, including the `tests/*.tftest.hcl` files |
| `terraform validate` | Each root, with `init -backend=false` against its committed lock file; state and plugins stay in a temporary directory. Roots on the jfrog providers validate through `test/terraform/run.sh`, whose filesystem mirror pins their zips |
| tflint | Each root, with the rulesets pinned in `.tflint.hcl`. `azurerm_resources_missing_prevent_destroy` is off: every root is created and destroyed on purpose |
| `trivy config` | Each root, with the checks embedded in the pinned binary (`--skip-check-update`). An accepted finding is a `#trivy:ignore:<id>` comment beside the resource with its reason, so it moves with the root |
| `terraform test` | Each root with `tests/*.tftest.hcl`. The tests use `mock_provider` and `command = plan`: no credentials and no cloud calls |
| terraform-docs | Each README with `BEGIN_TF_DOCS` markers is regenerated in a copy, using `.terraform-docs.yml`, and must match the committed file. `make deploy-docs` rewrites them in place |

The script fetches terraform (unless the one on `PATH` is the pinned version), tflint,
trivy and terraform-docs into the gitignored `.tools/` directory and checks each archive
against the SHA-256 from its release, so a laptop and CI run the same versions. To move a
tool, change its version and its four checksums (linux and darwin, amd64 and arm64) in
`deploy/check.sh`. The rulesets' versions live in `.tflint.hcl`.

Each root's `.terraform.lock.hcl` holds `h1:` hashes for linux and darwin on amd64 and
arm64. The script shares a provider plugin cache across roots, and Terraform refuses a
cached provider whose `h1:` hash the lock file lacks, so a lock written on one platform
fails in CI. After changing a root's providers, run `terraform providers lock
-platform=linux_amd64 -platform=linux_arm64 -platform=darwin_amd64 -platform=darwin_arm64`
in it.

Dependabot's `terraform` ecosystem updates the infrastructure roots' providers and lock
files, minor and patch versions only: a provider major merged on mocked tests alone could
break a real apply. `deploy/artifactweave` is left out: `run.sh` pins its provider zips by
SHA-256, so its providers move by hand, following the steps under
[Terraform scenarios](#terraform-scenarios).

## AQL fixtures from the JFrog CLI

`pkg/aql/aqltest/testdata` holds the AQL the pinned `jf` sends, one file per command shape.
With `ARTIFACTWEAVE_JF_BIN` pointing at the pinned binary, `make capture-aql`:
1. runs the acceptance scenario "the query shapes the fixtures capture", with the server
   logging every query it is sent at debug level (`ARTIFACTWEAVE_AQL_CAPTURE` names the
   directory the harness writes them to);
2. copies each query over its fixture.

Run it whenever the pinned `jf` changes, then check that the expected rows in
`pkg/aql/aqltest` still hold.

## Local equivalents

| Command | What |
|---|---|
| `make gate` | Everything the per-PR jobs run except acceptance |
| `make accept` | The acceptance job (needs Docker) |
| `make conformance` | The OCI conformance job (needs Docker and network for the spec clone) |
| `make generate` | Regenerate servers and clients; CI fails on drift |
| `make terraform TERRAFORM_MODE=apply` | The terraform job (needs `terraform`; without the mode it only validates) |
| `make deploy-check` | The deploy-terraform job (fetches pinned tools into `.tools/`; set `GITHUB_TOKEN` if tflint hits GitHub's rate limit) |
| `make deploy-docs` | Rewrite the terraform-docs sections of the READMEs |
| `make platform-<target> SCENARIO=<cloud>/<scenario>` | A target of a `deploy/platform` root on a real cloud, for example `platform-plan SCENARIO=gcp/single-bucket` or `platform-apply SCENARIO=azure/bootstrap`; `gcp-<target>` and `azure-<target>` are short forms for the two `single-bucket` scenarios. CI never runs these |
| `make capture-aql` | Refresh the AQL fixtures from the pinned `jf` (needs `ARTIFACTWEAVE_JF_BIN`) |
