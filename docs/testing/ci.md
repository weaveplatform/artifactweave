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
| `terraform` | `ubuntu-latest` | P3 | Applies `test/terraform/` against the binary with a pre-populated provider mirror, then checks `terraform plan` is empty, then destroys |
| `acceptance-peers` | `ubuntu-latest` | P13 | Starts two or three `-cover` instances on separate ports and data directories, pairs them, and runs the `@peers` godog features: federated repository convergence, partition and full sync (P13), Access Federation and topology (P14), release bundle distribution to an edge (P15). Kept apart from `acceptance` so the per-PR budget holds; uploads `cover-accept-peers` and joins the coverage and gate `needs` |
| `e2e` (nightly) | `ubuntu-latest` | P9 | `-tags e2e` acceptance against real upstream registries |

`acceptance`, `acceptance-peers` and `conformance` are listed in the coverage job's `needs` and in the
`gate` job's `needs`, so the merged coverage includes them and a failure blocks merge.

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
| `CONFORMANCE_GCS_BUCKET` | variable | An existing bucket |
| `GCP_SERVICE_ACCOUNT_KEY` | secret | JSON key of a service account with `roles/storage.objectAdmin` on the bucket. The key also signs URLs locally, so no `signBlob` permission is needed |
| `CONFORMANCE_AZURE_ACCOUNT` | variable | Storage account name, with Shared Key access enabled |
| `CONFORMANCE_AZURE_CONTAINER` | variable | An existing container in that account |
| `AZURE_STORAGE_ACCOUNT_KEY` | secret | An account key; the probes sign copy sources with it |

Give both stores a lifecycle rule that deletes `conformance/` objects after a day, in
case a cancelled run skips its cleanup. On GCS, a bucket with soft delete enabled bills
the deleted temporary parts for the retention period.

The same tests run from a workstation. They skip unless their variables are set:

```bash
ARTIFACTWEAVE_TEST_GCS_BUCKET=my-bucket \
GOOGLE_APPLICATION_CREDENTIALS=key.json \
  go test -count=1 -v -run 'TestContractOnRealGCS|TestRealGCSProbes' ./pkg/blob/gcs/

ARTIFACTWEAVE_TEST_AZURE_ACCOUNT=myaccount \
ARTIFACTWEAVE_TEST_AZURE_CONTAINER=conformance \
ARTIFACTWEAVE_TEST_AZURE_ACCOUNT_KEY=... \
  go test -count=1 -v -run 'TestContractOnRealAzure|TestRealAzureProbes' ./pkg/blob/azure/
```

Without `GOOGLE_APPLICATION_CREDENTIALS`, GCS uses Application Default Credentials
(`gcloud auth application-default login`). Without the Azure account key, the contract
authenticates with `DefaultAzureCredential` (`az login`, with Storage Blob Data
Contributor on the container), and the probes skip.

## Local equivalents

| Command | What |
|---|---|
| `make gate` | Everything the per-PR jobs run except acceptance |
| `make accept` | The acceptance job (needs Docker) |
| `make conformance` | The OCI conformance job (needs Docker and network for the spec clone) |
| `make generate` | Regenerate servers and clients; CI fails on drift |
