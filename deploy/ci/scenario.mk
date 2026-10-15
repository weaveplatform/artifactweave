# The make targets every deploy/platform scenario shares (decision 0030).
# Run them from the scenario's directory, or from the repository root with
# make platform-<target> SCENARIO=<cloud>/<scenario>.
#
# A scenario's Makefile sets the variables below, then includes this file as
# its last line:
#
#   include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
#
#   TF_VARS             extra -var flags for plan, apply and destroy
#   CONFORMANCE_DEPS    prerequisites of conformance besides the guard
#   CONFORMANCE_ENV     environment for the real-cloud tests; define it with =, not
#                       :=, so the $$(terraform output ...) calls run in the recipe
#   CONFORMANCE_RUN     the go test -run pattern
#   CONFORMANCE_PKG     the package holding the real-cloud tests
#   GITHUB_ENVIRONMENT  GitHub environment github-vars creates first; empty for none
#   GITHUB_VARS         VARIABLE=output pairs github-vars sets from the outputs
#   CLEAN_FILES         generated files clean removes
#
# The cloud is the name of the scenario's parent directory. It picks the guard
# every target that touches the cloud runs first (check-account.sh on AWS,
# check-subscription.sh on Azure, none on GCP) and, on Azure, pins Terraform to
# the lab's subscription. The lab's values come from lab.mk.
#
# ROOT comes from git rather than a relative path, so a scenario works at any
# depth; compose.yaml reads its build context from ARTIFACTWEAVE_ROOT for the
# same reason.
SHELL := /bin/bash
.DEFAULT_GOAL := help

TERRAFORM ?= terraform
GO ?= go
ROOT := $(shell git rev-parse --show-toplevel)
VERSION ?= $(shell git -C $(ROOT) describe --tags --always --dirty 2>/dev/null || echo devel)
COMMIT ?= $(shell git -C $(ROOT) rev-parse HEAD 2>/dev/null)
ARTIFACTWEAVE_ROOT := $(ROOT)
export VERSION COMMIT ARTIFACTWEAVE_ROOT

CLEAN_FILES ?= .local
CLOUD := $(notdir $(patsubst %/,%,$(dir $(CURDIR))))

include $(ROOT)/deploy/ci/lab.mk

GUARD := $(if $(filter aws,$(CLOUD)),check-account,$(if $(filter azure,$(CLOUD)),check-subscription))
TF_VARS += $(if $(filter azure,$(CLOUD)),-var subscription_id=$(LAB_AZURE_SUBSCRIPTION))

.PHONY: help plan apply credentials up smoke conformance github-vars logs down clean destroy check-account check-subscription

## help: list targets
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/^## //' | column -t -s ':'

## check-account: (aws) refuse unless the AWS CLI is signed in to the account holding the lab's zone
check-account:
	@$(ROOT)/deploy/ci/check-account.sh $(LAB_AWS_ZONE_ID) $(LAB_AWS_ZONE_NAME)

## check-subscription: (azure) refuse unless az's active subscription and tenant are the lab's
check-subscription:
	@$(ROOT)/deploy/ci/check-subscription.sh $(LAB_AZURE_SUBSCRIPTION)

## plan: terraform plan
plan: $(GUARD)
	$(TERRAFORM) init -input=false
	$(TERRAFORM) plan -input=false $(TF_VARS)

## apply: create the scenario's resources
apply: $(GUARD)
	$(TERRAFORM) init -input=false
	$(TERRAFORM) apply -input=false $(TF_VARS)

# Only some scenarios (the single-bucket ones) ship a local container; the
# others say so instead of failing on a missing script.
NO_CONTAINER = { echo "this scenario has no local container; use its artifactweave_config output" >&2; exit 1; }

## credentials: render the container's configuration and credentials into .local/
credentials:
	@[ -x ./credentials.sh ] || $(NO_CONTAINER)
	./credentials.sh

## up: build the image and start artifactweave in a local container against the scenario
up:
	@[ -x ./up.sh ] || $(NO_CONTAINER)
	./up.sh

## smoke: deploy and download 40 MiB through the running container
smoke:
	@[ -x ./smoke.sh ] || $(NO_CONTAINER)
	./smoke.sh

## conformance: run the real-cloud blob contract and probes locally
conformance: $(GUARD) $(CONFORMANCE_DEPS)
	cd $(ROOT) && $(CONFORMANCE_ENV) \
	  GOWORK=off $(GO) test -count=1 -timeout 25m -v -run '$(CONFORMANCE_RUN)' $(CONFORMANCE_PKG)

## github-vars: set the cloud-conformance workflow's repository variables from the outputs
github-vars:
	$(if $(GITHUB_ENVIRONMENT),gh api -X PUT repos/{owner}/{repo}/environments/$(GITHUB_ENVIRONMENT) >/dev/null)
	@for pair in $(GITHUB_VARS); do \
	  name=$${pair%%=*}; output=$${pair#*=}; \
	  echo "gh variable set $$name"; \
	  gh variable set "$$name" --body "$$($(TERRAFORM) output -raw "$$output")" || exit 1; \
	done

## logs: follow the container's logs
logs:
	@[ -f compose.yaml ] || $(NO_CONTAINER)
	docker compose logs --follow

## down: stop the container, keeping its data volume
down:
	@[ -f compose.yaml ] || $(NO_CONTAINER)
	docker compose down

## clean: stop the container and remove its data volume and the generated files
# destroy runs clean first, and a scenario with no local container
# (multi-region) has no compose.yaml for docker compose to read.
clean:
	if [ -f compose.yaml ]; then docker compose down --volumes; fi
	rm -rf $(CLEAN_FILES)

## destroy: remove everything Terraform created, stored objects included
destroy: $(GUARD) clean
	$(TERRAFORM) destroy -input=false $(TF_VARS)
