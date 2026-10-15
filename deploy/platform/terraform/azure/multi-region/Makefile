# azure/multi-region: two storage accounts in two regions as members of one
# artifactweave mirror, granted to the bootstrap's conformance identity and
# to you. Apply azure/bootstrap first. Plan and apply need az login and
# az account set --subscription SUBSCRIPTION. There is no local container or
# conformance target here: run artifactweave with
# `terraform output -raw artifactweave_config`, or let platform-scenarios.yml
# test it.

# Every target that touches Azure checks that the CLI's active subscription
# is this one first; Terraform is pinned to it too.
SUBSCRIPTION ?= c33dbc35-f86d-4b7c-a81e-2a33eb3f7946
GUARD := check-subscription
TF_VARS := -var subscription_id=$(SUBSCRIPTION)

.PHONY: check-subscription

## check-subscription: refuse unless az's active subscription and tenant are SUBSCRIPTION's
check-subscription:
	@../check-subscription.sh $(SUBSCRIPTION)

include $(shell git rev-parse --show-toplevel)/deploy/platform/terraform/scenario.mk
