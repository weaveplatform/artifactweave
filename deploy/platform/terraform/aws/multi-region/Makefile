# aws/multi-region: two S3 buckets in two regions as members of one
# artifactweave mirror, whose bucket policies grant the bootstrap's
# conformance role. Apply aws/bootstrap first. Plan and apply need
# `aws login` and refuse unless that login's account holds the delegated
# zone. There is no local container or conformance target here: run
# artifactweave with `terraform output -raw artifactweave_config`, or let
# platform-scenarios.yml test it.

ZONE_ID ?= Z06607493A97HOZP218UN
GUARD := check-account

.PHONY: check-account

## check-account: refuse unless the AWS CLI is signed in to the account holding the delegated zone
check-account:
	@../check-account.sh $(ZONE_ID)

include $(shell git rev-parse --show-toplevel)/deploy/platform/terraform/scenario.mk
