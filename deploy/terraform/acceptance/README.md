# GCS acceptance environment

A real Google Cloud Storage bucket for artifactweave's `gcs` blob backend. Three things use it:
- artifactweave running in a local container;
- the real-cloud conformance tests (`pkg/blob/gcs/real_test.go`), run locally;
- the manually dispatched `cloud-conformance.yml` workflow.

Nothing uses a service account key. The organisation enforces
`iam.disableServiceAccountKeyCreation`:
- **Terraform** runs with your own Application Default Credentials.
- **The local container and tests** impersonate the acceptance service account.
- **The workflow** reaches that account through Workload Identity Federation, limited to
  this repository.

## What Terraform creates

- A bucket in `europe-west2`. It has uniform access and public access prevention, and soft
  delete is off, so the temporary parts the backend composes and deletes aren't billed for a
  retention period. Lifecycle rules delete abandoned `local/_uploads/` parts after two days
  and `conformance/` prefixes after one.
- A service account with `roles/storage.objectAdmin` on that bucket only.
- Two Service Account Token Creator grants on that account: one for you, to impersonate it,
  and one for the account itself, to sign URLs through IAM `signBlob`.
- A Workload Identity pool and provider for GitHub Actions. The provider accepts only
  `weaveplatform/artifactweave`, which may act as the service account.

## Use

```bash
gcloud auth application-default login          # once
gcloud auth application-default set-quota-project <project>
make apply        # or, from the repository root: make gcp-apply
make up           # builds the image, starts artifactweave against the bucket on port 8081
make smoke        # deploys and downloads 40 MiB through the container
make conformance  # the real-GCS blob contract and probes
make github-vars  # sets the workflow's repository variables from the outputs
make down         # stops the container; make clean also drops its data
make destroy      # removes everything, the bucket's objects included
```

## Files

- **`credentials.sh`** writes `.secrets/container-credentials.json`, the
  `impersonated_service_account` file `gcloud --impersonate-service-account` would write,
  built from your ADC. It also writes the container's configuration and a generated master
  key and admin password in `.local/`. The key and password are kept across runs, because
  the database is sealed with that key.
- **Git-ignored:** `.secrets/`, `.local/` and the Terraform state.
