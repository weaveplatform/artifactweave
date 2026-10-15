# aws/bootstrap: GitHub's OIDC provider, the conformance and deployer roles,
# the deployer's permissions boundary, the remote state bucket, the DNS zone
# and a budget. Plan and apply need `aws login` (or any credentials for the
# lab account) and refuse unless that account holds the delegated zone.

ZONE_ID ?= Z06607493A97HOZP218UN
REGION ?= eu-west-2
GUARD := check-account
STATE_PROBE := aws s3api head-bucket --bucket artifactweave-tfstate-56920abd --region $(REGION)
# The address the budget alerts go to, given on the command line the first
# time (make apply BUDGET_EMAIL=you@example.com), so none enters the
# repository; a standalone account cannot read its own primary contact
# through the API. Once the budget exists the address lives in AWS and the
# state, and later runs need none (budget.tf). = defers the check to plan and
# apply.
BUDGET_EMAIL ?=
BUDGET_EXISTS = $(shell aws budgets describe-budget --budget-name artifactweave-lab \
  --account-id "$$(aws sts get-caller-identity --query Account --output text)" >/dev/null 2>&1 && echo yes)
TF_VARS = $(if $(BUDGET_EMAIL),-var 'budget_alert_email=$(BUDGET_EMAIL)',$(if $(BUDGET_EXISTS),,$(error the budget does not exist yet: set BUDGET_EMAIL to the address AWS Budgets should alert, e.g. make apply BUDGET_EMAIL=you@example.com)))
GITHUB_VARS := \
  CONFORMANCE_AWS_REGION=region \
  PLATFORM_AWS_REGION=region \
  PLATFORM_AWS_STATE_BUCKET=state_bucket \
  PLATFORM_AWS_DNS_ZONE_ID=dns_zone_id
# Secrets, not variables: role ARNs carry the account ID, which this public
# repository and its public workflow logs do not show.
GITHUB_SECRETS := \
  CONFORMANCE_AWS_ROLE_ARN=conformance_role_arn \
  PLATFORM_AWS_ROLE_ARN=deployer_role_arn

.PHONY: check-account

## check-account: refuse unless the AWS CLI is signed in to the account holding the delegated zone
check-account:
	@../check-account.sh $(ZONE_ID)

include $(shell git rev-parse --show-toplevel)/deploy/platform/terraform/bootstrap.mk
