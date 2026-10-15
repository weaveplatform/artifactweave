# The GCP bootstrap (`deploy/ci/terraform/gcp`)

The long-lived foundation every GCP scenario stands on, in the lab project
`project-0c0b2d7a-9ad7-4221-9a8` (decision
[0030](../../../../docs/research/decisions/0030-reference-deployments.md)). It is not a
scenario: an operator applies it once, and scenarios are created and destroyed without
touching it.

## What it holds

- **APIs** every scenario needs, so the deployer never administers service usage.
  Destroying the root leaves them enabled.
- **GitHub's trust.** The `github-actions` workload identity pool and its `artifactweave`
  provider. The provider admits exactly two subjects, this repository's
  `cloud-conformance` and `cloud-platform` environments, in GitHub's immutable form
  (`repo:weaveplatform@336817058/artifactweave@1415179278:environment:<env>`). Each
  service account trusts one of them:

  | Service account | Trusted subject | Rights |
  |---|---|---|
  | `artifactweave-conformance` | `cloud-conformance` | objects in the `single-bucket` bucket only (granted there); signs URLs as itself; your login may act as it |
  | `artifactweave-deployer` | `cloud-platform` | storage admin, load balancer admin (Cloud CDN), Certificate Manager editor, service usage consumer, DNS admin on this zone only; signs URLs as itself |

  Neither can administer the project's IAM or its APIs.
- **Remote state** in `artifactweave-tfstate-1025324209347`: private, versioned, kept
  versions pruned after 90 days. This root's own state lives under `bootstrap/gcp`, and
  scenario runs under `scenarios/<cloud>/<scenario>/<instance>`.
- **The DNS zone** `gcp.artifact.weaveplatform.run`, created by CLI and delegated at
  GoDaddy, then imported here. Terraform refuses to destroy it, because a new zone would
  get new name servers and break the delegation.
- **A budget** on the project, alerting the billing account's administrators at 50%, 90%
  and 100% of `budget_amount`.

## Use

`gcloud auth application-default login` first. From this directory (or
`make ci-<target> CLOUD=gcp` from the repository root):

```bash
make undelete-pool   # once: restores the pool and provider deleted on 2026-10-14
make plan
make apply FIRST_RUN=1   # first run only: on local state, because the state bucket doesn't exist yet
make migrate-state   # once: moves the state into the bucket just created
make github-vars     # creates both GitHub environments and sets the repository variables
```

Only with `FIRST_RUN=1` does `make init` write a git-ignored `local_override.tf`, replacing
the GCS backend with local state while the state bucket is missing; it removes it once the
bucket exists. Without `FIRST_RUN`, a state probe that fails stops the run, so an expired
login can never plan this bootstrap from empty local state.

`make destroy` refuses: CI trust, state and the delegated zone live here. To retire a
part, remove it from the configuration and apply, deliberately.

## Cost

Nothing here has a fixed or hourly charge. Workload identity, service accounts, IAM,
enabled APIs and budgets are free. The state bucket holds kilobytes, billed per GB. The
managed zone is billed per zone per month (about $0.20) and stays, because its delegation
does.

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.7 |
| google | ~> 8.6 |

### Providers

| Name | Version |
| ---- | ------- |
| google | 8.6.0 |
| google.billing | 8.6.0 |

### Resources

| Name | Type |
| ---- | ---- |
| [google_billing_budget.lab](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/billing_budget) | resource |
| [google_dns_managed_zone.gcp](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/dns_managed_zone) | resource |
| [google_dns_managed_zone_iam_member.deployer](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/dns_managed_zone_iam_member) | resource |
| [google_iam_workload_identity_pool.github](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/iam_workload_identity_pool) | resource |
| [google_iam_workload_identity_pool_provider.github](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/iam_workload_identity_pool_provider) | resource |
| [google_project_iam_member.deployer](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/project_iam_member) | resource |
| [google_project_service.this](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/project_service) | resource |
| [google_service_account.conformance](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/service_account) | resource |
| [google_service_account.deployer](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/service_account) | resource |
| [google_service_account_iam_member.conformance_github](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/service_account_iam_member) | resource |
| [google_service_account_iam_member.conformance_signs_as_itself](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/service_account_iam_member) | resource |
| [google_service_account_iam_member.deployer_github](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/service_account_iam_member) | resource |
| [google_service_account_iam_member.deployer_signs_as_itself](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/service_account_iam_member) | resource |
| [google_service_account_iam_member.operator_impersonates_conformance](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/service_account_iam_member) | resource |
| [google_storage_bucket.state](https://registry.terraform.io/providers/hashicorp/google/latest/docs/resources/storage_bucket) | resource |
| [google_client_openid_userinfo.me](https://registry.terraform.io/providers/hashicorp/google/latest/docs/data-sources/client_openid_userinfo) | data source |
| [google_project.this](https://registry.terraform.io/providers/hashicorp/google/latest/docs/data-sources/project) | data source |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| billing\_account | Billing account the project bills to, for the budget. | `string` | `"011F23-67D58D-596CD4"` | no |
| budget\_amount | Monthly budget for the project, in the billing account's currency. Alerts go to the billing account's administrators at 50%, 90% and 100%. | `number` | `20` | no |
| conformance\_environment | GitHub environment whose jobs act as the data-plane service account (cloud-conformance.yml). | `string` | `"cloud-conformance"` | no |
| dns\_zone | The Cloud DNS managed zone for gcp.artifact.weaveplatform.run, created by CLI and delegated at GoDaddy; imported here, never recreated. | `string` | `"gcp-artifact-weaveplatform-run"` | no |
| github\_subject\_prefix | The OIDC subject prefix of this repository. It uses GitHub's immutable subject, which names the owner and repository with their IDs; read it with `gh api repos/{owner}/{repo}/actions/oidc/customization/sub --jq .sub_claim_prefix`. | `string` | `"repo:weaveplatform@336817058/artifactweave@1415179278"` | no |
| platform\_environment | GitHub environment whose jobs act as the deployer service account (platform-auth-check.yml, and the scenario workflow in #129). | `string` | `"cloud-platform"` | no |
| project\_id | The lab project every GCP scenario runs in. | `string` | `"project-0c0b2d7a-9ad7-4221-9a8"` | no |
| region | Location of the state bucket. | `string` | `"europe-west2"` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| conformance\_service\_account | The data-plane service account (CONFORMANCE\_GCP\_SERVICE\_ACCOUNT); gcp/single-bucket grants it its bucket. |
| deployer\_service\_account | The deployer service account (PLATFORM\_GCP\_SERVICE\_ACCOUNT). |
| dns\_name\_servers | Name servers for the NS records at GoDaddy (gcp.artifact under weaveplatform.run). |
| dns\_zone | The Cloud DNS managed zone scenarios add records to. |
| project\_id | The lab project (PLATFORM\_GCP\_PROJECT). |
| project\_number | The lab project's number, part of every workload identity principal. |
| state\_bucket | Remote state bucket for this bootstrap and every scenario run (PLATFORM\_GCP\_STATE\_BUCKET). |
| workload\_identity\_provider | Provider for google-github-actions/auth, for both environments (CONFORMANCE\_GCP\_WIF\_PROVIDER and PLATFORM\_GCP\_WIF\_PROVIDER). |
<!-- END_TF_DOCS -->
