# The AWS bootstrap (`deploy/ci/terraform/aws`)

The long-lived foundation every AWS scenario stands on, in the lab account in `eu-west-2`
(decision [0030](../../../../docs/research/decisions/0030-reference-deployments.md)). It is
not a scenario: an operator applies it once, and scenarios are created and destroyed without
touching it. The account ID is not recorded in this repository; the Makefile recognises the
lab account by the delegated hosted zone instead (`deploy/ci/check-account.sh`, given the
zone's ID and name from `deploy/ci/lab.mk`).

## What it holds

- **GitHub's trust.** The account's OIDC provider for `token.actions.githubusercontent.com`
  (audience `sts.amazonaws.com`), and two roles, each trusting one environment subject in
  GitHub's immutable form
  (`repo:weaveplatform@336817058/artifactweave@1415179278:environment:<env>`), matched with
  `StringEquals`:

  | Role (path `/artifactweave/`) | Trusted subject | Rights |
  |---|---|---|
  | `artifactweave-conformance` | `cloud-conformance` | none of its own: `single-bucket`'s bucket policy grants it that bucket's objects |
  | `artifactweave-deployer` | `cloud-platform` | buckets named `artifactweave-*`, CloudFront and ACM, records in this zone only, and roles under `/artifactweave-scenarios/` only; capped by the scenario permissions boundary |

- **The scenario permissions boundary** `artifactweave-scenario-boundary`, on the deployer
  and required on every role it creates (S3 replication's, for one): scenario services in
  full, records in this zone, roles under the scenario path only and only with this
  boundary, and nothing that touches the bootstrap's state, roles, policies or OIDC
  provider. The deployer can never hand out more than it holds.
- **Remote state** in `artifactweave-tfstate-56920abd`: private, TLS only, versioned, kept
  versions pruned after 90 days, locked with S3's own conditional writes (`use_lockfile`,
  no DynamoDB). This root's state lives at `bootstrap/aws.tfstate`, and scenario runs under
  `scenarios/<cloud>/<scenario>/<instance>`.
- **The hosted zone** `aws.artifact.weaveplatform.run`, created by CLI and delegated at
  GoDaddy, then imported here. Terraform refuses to destroy it, because a new zone would get
  new name servers and break the delegation.
- **A budget** on the account, alerting `BUDGET_EMAIL` at 50%, 90% and 100% of
  `budget_amount` (USD) of actual spend. The address is given on the command line when
  the budget is created, so none is committed (a standalone account cannot read its own
  primary contact through the API); later runs need none and leave the alerts alone.

## Use

`aws login` first. From this directory (or `make ci-<target> CLOUD=aws`
from the repository root):

```bash
make plan  BUDGET_EMAIL=you@example.com   # BUDGET_EMAIL only until the budget exists
make apply BUDGET_EMAIL=you@example.com FIRST_RUN=1   # first run only: on local state, because the state bucket doesn't exist yet
make migrate-state   # once: moves the state into the bucket just created
make github-vars     # creates both GitHub environments; sets the variables, and the role ARNs as secrets
```

The role ARNs carry the account ID, so `github-vars` stores them as repository secrets,
which the public workflow logs mask, and the workflows ask `configure-aws-credentials` to
mask the account ID too.

Only with `FIRST_RUN=1` does `make init` write a git-ignored `local_override.tf`, replacing
the S3 backend with local state while the state bucket is missing; it removes it once the
bucket exists. Without `FIRST_RUN`, a state probe that fails stops the run, so an expired
login can never plan this bootstrap from empty local state. Every
target first checks that the AWS CLI is signed in to the account holding the zone.

`make destroy` refuses: CI trust, state and the delegated zone live here. To retire a part,
remove it from the configuration and apply, deliberately.

## Cost

Nothing here has a fixed or hourly charge. The OIDC provider, IAM roles and policies, and
the first two budgets are free. The state bucket holds kilobytes, billed per GB. The hosted
zone is billed per zone per month (about $0.50) and stays, because its delegation does.

<!-- BEGIN_TF_DOCS -->
### Requirements

| Name | Version |
| ---- | ------- |
| terraform | >= 1.10 |
| aws | ~> 6.67 |

### Providers

| Name | Version |
| ---- | ------- |
| aws | 6.69.1 |

### Modules

No modules.

### Resources

| Name | Type |
| ---- | ---- |
| [aws_budgets_budget.lab](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/budgets_budget) | resource |
| [aws_iam_openid_connect_provider.github](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_openid_connect_provider) | resource |
| [aws_iam_policy.scenario_boundary](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_policy) | resource |
| [aws_iam_role.conformance](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role) | resource |
| [aws_iam_role.deployer](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role) | resource |
| [aws_iam_role_policy.deployer](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role_policy) | resource |
| [aws_route53_zone.aws](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/route53_zone) | resource |
| [aws_s3_bucket.state](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket) | resource |
| [aws_s3_bucket_lifecycle_configuration.state](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_lifecycle_configuration) | resource |
| [aws_s3_bucket_ownership_controls.state](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_ownership_controls) | resource |
| [aws_s3_bucket_policy.state](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_policy) | resource |
| [aws_s3_bucket_public_access_block.state](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_public_access_block) | resource |
| [aws_s3_bucket_server_side_encryption_configuration.state](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_server_side_encryption_configuration) | resource |
| [aws_s3_bucket_versioning.state](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/s3_bucket_versioning) | resource |
| [aws_caller_identity.current](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/caller_identity) | data source |

### Inputs

| Name | Description | Type | Default | Required |
| ---- | ----------- | ---- | ------- | :------: |
| budget\_alert\_email | Address the budget alerts go to, needed only when the budget is created; the Makefile passes BUDGET\_EMAIL, so no address is committed. Null on later runs, which leave the existing alerts alone. | `string` | `null` | no |
| budget\_amount | Monthly cost budget for the account, in USD. Alerts at 50%, 90% and 100% of actual spend. | `number` | `20` | no |
| conformance\_environment | GitHub environment whose jobs assume the conformance role (cloud-conformance.yml). | `string` | `"cloud-conformance"` | no |
| dns\_zone\_id | The Route 53 hosted zone for aws.artifact.weaveplatform.run, created by CLI and delegated at GoDaddy; imported here, never recreated. | `string` | `"Z06607493A97HOZP218UN"` | no |
| github\_subject\_prefix | The OIDC subject prefix of this repository. It uses GitHub's immutable subject, which names the owner and repository with their IDs; read it with `gh api repos/{owner}/{repo}/actions/oidc/customization/sub --jq .sub_claim_prefix`. | `string` | `"repo:weaveplatform@336817058/artifactweave@1415179278"` | no |
| platform\_environment | GitHub environment whose jobs assume the deployer role (platform-auth-check.yml, and the scenario workflow in #129). | `string` | `"cloud-platform"` | no |
| region | Region of the state bucket and of every AWS scenario. | `string` | `"eu-west-2"` | no |
| scenario\_bucket\_prefix | Name prefix of every scenario bucket; the deployer administers buckets with this prefix only. | `string` | `"artifactweave-"` | no |
| scenario\_role\_path | IAM path of every role a scenario creates (S3 replication, for one). The deployer may create and change roles under this path only, and only with the scenario permissions boundary attached. | `string` | `"/artifactweave-scenarios/"` | no |

### Outputs

| Name | Description |
| ---- | ----------- |
| conformance\_role\_arn | The data-plane role (CONFORMANCE\_AWS\_ROLE\_ARN); aws/single-bucket's bucket policy grants it its bucket. |
| deployer\_role\_arn | The deployer role (PLATFORM\_AWS\_ROLE\_ARN). |
| dns\_name\_servers | Name servers for the NS records at GoDaddy (aws.artifact under weaveplatform.run). |
| dns\_zone\_id | The Route 53 hosted zone scenarios add records to (PLATFORM\_AWS\_DNS\_ZONE\_ID). |
| oidc\_provider\_arn | GitHub's OIDC provider in the account. |
| region | Region of the state bucket and the scenarios (CONFORMANCE\_AWS\_REGION and PLATFORM\_AWS\_REGION). |
| scenario\_boundary\_arn | The permissions boundary every role a scenario creates must carry. |
| state\_bucket | Remote state bucket for this bootstrap and every scenario run (PLATFORM\_AWS\_STATE\_BUCKET). |
<!-- END_TF_DOCS -->
