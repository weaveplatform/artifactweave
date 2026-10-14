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

`GOWORK=off` isolates builds from parent workspaces. Actions are pinned by commit SHA.

## Jobs added by this project

| Job | Runner | Added in | What |
|---|---|---|---|
| `acceptance` | `ubuntu-latest` | P0 | Builds `artifactweave` with `-cover`, runs godog features with pinned `jf`, `terraform`, `helm`, `crane`, `oras`, `cosign` binaries and container-backed Maven, .NET, Node and Python clients; uploads `cover-accept` |
| `parity` | `ubuntu-latest` | P0 | Restores or fetches the gitignored `.specoracle` cache of JFrog's published OpenAPI blocks and runs `TestSpecParity` against `api/parity-baseline.json`. Required since P1 (in the gate's `needs`): the baseline lists only out-of-scope products. docs.jfrog.com is contacted only on a cache miss |
| `conformance` | `ubuntu-latest` | P6 | Starts the `-cover` binary with a `docker-local` repo, clones `opencontainers/distribution-spec` at a pinned tag and runs its conformance module with `OCI_ROOT_URL`, `OCI_NAMESPACE=<repo>/<image>` and credentials; uploads `cover-conformance` |
| `terraform` | `ubuntu-latest` | P3 | Installs the pinned `jfrog/artifactory` and `jfrog/platform` providers from a filesystem mirror of their release zips (checked against the release SHA-256s and the committed lock files) and validates each scenario: the `test/terraform/` repositories fixture, `deploy/artifactweave/terraform/identity` and `deploy/artifactweave/terraform/oidc`. Then it starts one `-cover` binary, mints an admin access token, and for each scenario applies, requires an empty `terraform plan` and destroys (`make terraform TERRAFORM_MODE=apply`, from P4, #69, #85 and #83); uploads `cover-terraform` |
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

## Real-cloud conformance (manual)

`cloud-conformance.yml` runs the blob contract (`blobtest.Run`) and the research 18
probes against a real Google Cloud Storage bucket and a real Azure Blob Storage
container. fake-gcs-server and Azurite do not enforce compose preconditions,
copy-source authorisation, signed URLs or service limits, so this is the only check of
them. The workflow is not part of the gate: it needs cloud credentials and is started
from the Actions tab (**Run workflow**, target `both`, `gcs` or `azure`). Each run
writes under `conformance/<random>/` and deletes that prefix when it ends. The step
summary lists the `PROBE` lines that answer the open questions in
[research 18 §4](../research/18-cloud-blob-storage.md#4-open-questions-for-a-real-cloud-check).
A run whose variables or secrets are missing fails rather than skipping.

| Name | Kind | Value |
|---|---|---|
| `CONFORMANCE_GCS_BUCKET` | variable | The bucket (`terraform output bucket` in `deploy/platform/terraform/gcp/single-bucket`) |
| `CONFORMANCE_GCP_SERVICE_ACCOUNT` | variable | The service account the job acts as (`terraform output service_account`). It has `roles/storage.objectAdmin` on the bucket and signs URLs through IAM `signBlob` as itself |
| `CONFORMANCE_GCP_WIF_PROVIDER` | variable | The Workload Identity Federation provider (`terraform output workload_identity_provider`). The job exchanges its GitHub OIDC token for the service account, so no key is stored; organisations enforcing `iam.disableServiceAccountKeyCreation` allow no keys anyway |
| `CONFORMANCE_AZURE_ACCOUNT` | variable | The storage account (`terraform output account` in `deploy/platform/terraform/azure/single-bucket`). It allows no Shared Key |
| `CONFORMANCE_AZURE_CONTAINER` | variable | The container in that account |
| `CONFORMANCE_AZURE_CLIENT_ID`, `CONFORMANCE_AZURE_TENANT_ID`, `CONFORMANCE_AZURE_SUBSCRIPTION_ID` | variables | The managed identity the job logs in as with `azure/login` (OpenID Connect), with Storage Blob Data Contributor on the account. Its federated credential trusts this repository's `cloud-conformance` environment, which the job declares |

Give both stores a lifecycle rule that deletes `conformance/` objects after a day, in
case a cancelled run skips its cleanup. On GCS, a bucket with soft delete enabled bills
the deleted temporary parts for the retention period.

The same tests run from a workstation. They skip unless their variables are set:

```bash
make gcp-conformance     # or, by hand, after make gcp-credentials (writes .secrets/container-credentials.json):
ARTIFACTWEAVE_TEST_GCS_BUCKET=$(terraform -chdir=deploy/platform/terraform/gcp/single-bucket output -raw bucket) \
ARTIFACTWEAVE_TEST_GCS_ACCESS_ID=$(terraform -chdir=deploy/platform/terraform/gcp/single-bucket output -raw service_account) \
GOOGLE_APPLICATION_CREDENTIALS=deploy/platform/terraform/gcp/single-bucket/.secrets/container-credentials.json \
  go test -count=1 -v -run 'TestContractOnRealGCS|TestRealGCSProbes' ./pkg/blob/gcs/

make azure-conformance   # or, by hand, after az login:
ARTIFACTWEAVE_TEST_AZURE_ACCOUNT=$(terraform -chdir=deploy/platform/terraform/azure/single-bucket output -raw account) \
ARTIFACTWEAVE_TEST_AZURE_CONTAINER=artifactweave \
  go test -count=1 -v -run 'TestContractOnRealAzure|TestRealAzureProbes' ./pkg/blob/azure/
```

The GCS run acts as the acceptance service account through the impersonated credentials
`up.sh` writes from your own Application Default Credentials
(`gcloud auth application-default login`); `deploy/platform/terraform/gcp/single-bucket` creates the
bucket, the account and the federation for the workflow.

The Azure run authenticates with `DefaultAzureCredential`: your `az login` locally, the
`azure/login` OIDC session in the workflow. Both hold Storage Blob Data Contributor on the
account, and the probes sign copy sources with a user delegation SAS from it. An
`ARTIFACTWEAVE_TEST_AZURE_ACCOUNT_KEY`, for an account that still allows Shared Key, switches
the contract and probes to that key instead. `make azure-conformance` refuses to run unless
the Azure CLI's active subscription is the acceptance one
(`deploy/platform/terraform/azure/single-bucket`).

## Terraform scenarios

`make terraform` runs `test/terraform/run.sh` over `TERRAFORM_SCENARIOS`: the repositories
fixture in `test/terraform/` and the identity and OIDC references in
`deploy/artifactweave/terraform/identity/` and `deploy/artifactweave/terraform/oidc/`.
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
break a real apply. `deploy/artifactweave` and `test/terraform` are left to `deps-refresh`,
because `run.sh` pins their provider zips.

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
| `make platform-<target> SCENARIO=<cloud>/<scenario>` | A target of a `deploy/platform` scenario on a real cloud, for example `platform-plan SCENARIO=gcp/single-bucket`; `gcp-<target>` and `azure-<target>` are short forms for the two `single-bucket` scenarios. CI never runs these |
| `make capture-aql` | Refresh the AQL fixtures from the pinned `jf` (needs `ARTIFACTWEAVE_JF_BIN`) |
