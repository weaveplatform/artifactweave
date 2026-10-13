# Azure acceptance environment

A real Azure storage account for artifactweave's `azure` blob backend. It's used by
artifactweave running in a local container, by the real-cloud conformance tests run locally,
and by the manually dispatched `cloud-conformance.yml` workflow.

Nothing uses an account key: the account has Shared Key disabled.
- **Terraform** runs with your Azure CLI login.
- **The local tests** run as you through `DefaultAzureCredential`, with the Azure CLI.
- **The workflow** uses a user-assigned managed identity, through a federated credential for
  this repository's `cloud-conformance` environment.
- **The local container** runs on a short-lived user delegation SAS minted from your login,
  because `DefaultAzureCredential` can't reach the host's Azure CLI from inside the image.

## Subscription guard

Terraform is pinned to `subscription_id`, by default `c33dbc35-f86d-4b7c-a81e-2a33eb3f7946`.
Every `az` command the scripts run passes `--subscription`. `plan`, `apply`, `destroy`,
`credentials` and `conformance` first run `check-subscription.sh`, which refuses unless:
- the Azure CLI's active subscription is that one; and
- the login's tenant owns it.

A login aimed at another subscription can't be used for anything here.

## What Terraform creates

In `uksouth`, in resource group `artifactweave-acc-rg`:
- **A StorageV2 LRS account:** HTTPS only, TLS 1.2, no public blob access, Shared Key
  disabled, Entra ID the default.
- **A private container `artifactweave`.**
- **A lifecycle policy:** it deletes `local/_uploads/` blobs after 2 days and
  `conformance/` blobs after 1 day. Uncommitted blocks aren't blobs; Azure discards them
  after 7 days.
- **A user-assigned managed identity** with a federated credential for subject
  `repo:weaveplatform@336817058/artifactweave@1415179278:environment:cloud-conformance`.
  This repository uses GitHub's immutable OIDC subject, which carries the owner and
  repository IDs. Azure matches the subject exactly, so the plain `repo:<owner>/<repo>` form
  never matches. `gh api repos/{owner}/{repo}/actions/oidc/customization/sub` shows the prefix.
- **Storage Blob Data Contributor** on the account, for that identity and for you. The role
  includes `generateUserDelegationKey`, for presigned URLs and the container's SAS.

## Use

```bash
az login
az account set --subscription c33dbc35-f86d-4b7c-a81e-2a33eb3f7946
make apply        # or, from the repository root: make azure-apply
make up           # mints the SAS, builds the image, starts artifactweave on port 8082
make smoke        # deploys and downloads 40 MiB, then reads the blob's committed block list
make conformance  # the real-Azure blob contract and probes, as you
make conformance LARGE_RANGES=1  # adds Put Block From URL ranges up to 4,000 MiB (about a minute)
make github-vars  # creates the cloud-conformance environment, sets the workflow's variables
make down         # stops the container; make clean also drops its data and .local/
make destroy      # removes everything, the account's blobs included
```

## Limits of the container's SAS

- **Expiry:** the SAS lasts at most seven days, the user delegation limit. `make credentials`
  or `make up` mints a new one.
- **No presigning:** a backend on a SAS can't presign, so a repository with
  `downloadRedirect` serves downloads through artifactweave instead.
- **Copy sources:** the backend copies into place with Put Block From URL, the source
  carrying the same SAS.
- **Git-ignored:** `.local/` (configuration, master key, admin password, SAS) and the
  Terraform state.
