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
| `terraform` | `ubuntu-latest` | P3 | Installs the pinned `jfrog/artifactory` provider from a filesystem mirror of its release zip (checked against the release SHA-256 and the committed lock file) and validates `test/terraform/`. Then it starts a `-cover` binary, mints an admin access token, applies the fixture, requires an empty `terraform plan` and destroys (`make terraform TERRAFORM_MODE=apply`, from P4, #69); uploads `cover-terraform` |
| `acceptance-peers` | `ubuntu-latest` | P13 | Starts two or three `-cover` instances on separate ports and data directories, pairs them, and runs the `@peers` godog features: federated repository convergence, partition and full sync (P13), Access Federation and topology (P14), release bundle distribution to an edge (P15). Kept apart from `acceptance` so the per-PR budget holds; uploads `cover-accept-peers` and joins the coverage and gate `needs` |
| `e2e` (nightly) | `ubuntu-latest` | P9 | `-tags e2e` acceptance against real upstream registries |

`acceptance`, `acceptance-peers`, `conformance` and `terraform` are listed in the coverage job's `needs`
and in the `gate` job's `needs`, so the merged coverage includes them and a failure blocks merge.

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
| `CONFORMANCE_GCS_BUCKET` | variable | The bucket (`terraform output bucket` in `deploy/terraform/acceptance`) |
| `CONFORMANCE_GCP_SERVICE_ACCOUNT` | variable | The service account the job acts as (`terraform output service_account`). It has `roles/storage.objectAdmin` on the bucket and signs URLs through IAM `signBlob` as itself |
| `CONFORMANCE_GCP_WIF_PROVIDER` | variable | The Workload Identity Federation provider (`terraform output workload_identity_provider`). The job exchanges its GitHub OIDC token for the service account, so no key is stored; organisations enforcing `iam.disableServiceAccountKeyCreation` allow no keys anyway |
| `CONFORMANCE_AZURE_ACCOUNT` | variable | The storage account (`terraform output account` in `deploy/terraform/acceptance-azure`). It allows no Shared Key |
| `CONFORMANCE_AZURE_CONTAINER` | variable | The container in that account |
| `CONFORMANCE_AZURE_CLIENT_ID`, `CONFORMANCE_AZURE_TENANT_ID`, `CONFORMANCE_AZURE_SUBSCRIPTION_ID` | variables | The managed identity the job logs in as with `azure/login` (OpenID Connect), with Storage Blob Data Contributor on the account. Its federated credential trusts this repository's `cloud-conformance` environment, which the job declares |

Give both stores a lifecycle rule that deletes `conformance/` objects after a day, in
case a cancelled run skips its cleanup. On GCS, a bucket with soft delete enabled bills
the deleted temporary parts for the retention period.

The same tests run from a workstation. They skip unless their variables are set:

```bash
cd deploy/terraform/acceptance && ./up.sh && cd -   # writes .secrets/container-credentials.json
ARTIFACTWEAVE_TEST_GCS_BUCKET=$(terraform -chdir=deploy/terraform/acceptance output -raw bucket) \
ARTIFACTWEAVE_TEST_GCS_ACCESS_ID=$(terraform -chdir=deploy/terraform/acceptance output -raw service_account) \
GOOGLE_APPLICATION_CREDENTIALS=deploy/terraform/acceptance/.secrets/container-credentials.json \
  go test -count=1 -v -run 'TestContractOnRealGCS|TestRealGCSProbes' ./pkg/blob/gcs/

make azure-conformance   # or, by hand, after az login:
ARTIFACTWEAVE_TEST_AZURE_ACCOUNT=$(terraform -chdir=deploy/terraform/acceptance-azure output -raw account) \
ARTIFACTWEAVE_TEST_AZURE_CONTAINER=artifactweave \
  go test -count=1 -v -run 'TestContractOnRealAzure|TestRealAzureProbes' ./pkg/blob/azure/
```

The GCS run acts as the acceptance service account through the impersonated credentials
`up.sh` writes from your own Application Default Credentials
(`gcloud auth application-default login`); `deploy/terraform/acceptance` creates the
bucket, the account and the federation for the workflow.

The Azure run authenticates with `DefaultAzureCredential`: your `az login` locally, the
`azure/login` OIDC session in the workflow. Both hold Storage Blob Data Contributor on the
account, and the probes sign copy sources with a user delegation SAS from it. An
`ARTIFACTWEAVE_TEST_AZURE_ACCOUNT_KEY`, for an account that still allows Shared Key, switches
the contract and probes to that key instead. `make azure-conformance` refuses to run unless
the Azure CLI's active subscription is the acceptance one
(`deploy/terraform/acceptance-azure`).

## Terraform fixture

`make terraform` runs `test/terraform/run.sh`:
- It downloads `terraform-provider-artifactory_<version>_<os>_<arch>.zip` once into a
  filesystem mirror, checking it against the SHA-256 pinned in the script.
- It points `TF_CLI_CONFIG_FILE` at a CLI configuration that installs the provider only from
  that mirror.
- It runs `terraform init -lockfile=readonly` and `terraform validate`.

The committed `.terraform.lock.hcl` holds the provider's hashes for linux and darwin on
amd64 and arm64. To move the provider to a new version:
1. Update the version in `main.tf`.
2. Update the checksums in `run.sh` from the release's `SHA256SUMS`.
3. Regenerate the lock from a mirror holding the four zips:
   `terraform providers lock -fs-mirror=<mirror> -platform=...`.

`make terraform TERRAFORM_MODE=apply` adds the full run, which the `terraform` CI job runs:
1. Build and start a `-cover` binary, writing its coverage to `cover/terraform`.
2. Mint an admin access token with the bootstrap admin's password
   (`POST /access/api/v1/tokens`). The provider authenticates only with a Bearer token.
   Set `TERRAFORM_ACCESS_TOKEN` to supply one instead.
3. Apply the repository.
4. Deploy `fixture.txt`.
5. Apply its properties.
6. Require `terraform plan -detailed-exitcode` to exit 0.
7. Destroy.

Terraform runs on a copy of the fixture in the work directory, so its state never lands
in the source tree. The empty plan is the check that matters: the provider reads back
every attribute it wrote. It found two gaps in the repository API, both fixed in #69. The
server echoed the boolean flags JFrog documents as strings in that string form, which the
provider cannot decode. It also left out `environments` when a repository has none, which
the provider reads as null against its default of an empty set.

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
| `make capture-aql` | Refresh the AQL fixtures from the pinned `jf` (needs `ARTIFACTWEAVE_JF_BIN`) |
