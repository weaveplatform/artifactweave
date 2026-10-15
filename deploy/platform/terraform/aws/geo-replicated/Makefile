# aws/geo-replicated: a primary S3 bucket that cross-region replication
# copies into a replica in a second region, both granted to the bootstrap's
# conformance role through their bucket policies. Apply aws/bootstrap first.
# Plan and apply need `aws login` and refuse unless that login's account
# holds the delegated zone. There is no local container or conformance
# target: run artifactweave with the artifactweave_config output, or
# dispatch the scenario through platform-scenarios.yml.

ZONE_ID ?= Z06607493A97HOZP218UN
GUARD := check-account

.PHONY: check-account

## check-account: refuse unless the AWS CLI is signed in to the account holding the delegated zone
check-account:
	@../check-account.sh $(ZONE_ID)

include $(shell git rev-parse --show-toplevel)/deploy/platform/terraform/scenario.mk
