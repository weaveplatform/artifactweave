# The make targets every deploy/platform scenario shares (decision 0030).
# Run them from the scenario's directory, or from the repository root with
# make platform-<target> SCENARIO=<cloud>/<scenario>.
#
# A scenario's Makefile sets the variables below, then includes this file as
# its last line:
#
#   include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
#
#   TF_VARS             the scenario's own -var flags for plan, apply and
#                       destroy; define it with =, so a $(LAB_*) value in it
#                       expands after lab.mk is included
#   CONFORMANCE_DEPS    prerequisites of conformance besides the guard
#   CONFORMANCE_ENV     environment for the real-cloud tests; define it with =, not
#                       :=, so the $$(terraform output ...) calls run in the recipe
#   CONFORMANCE_RUN     the go test -run pattern
#   CONFORMANCE_PKG     the package holding the real-cloud tests
#   GITHUB_ENVIRONMENT  GitHub environment github-vars creates first; empty for none
#   GITHUB_VARS         VARIABLE=output pairs github-vars sets from the outputs
#
# The cloud is the name of the scenario's parent directory. It picks the guard
# every target that touches the cloud runs first (check-account.sh on AWS,
# check-subscription.sh on Azure, none on GCP, both for multicloud) and the
# inputs lab-vars.sh adds to the Makefile's for plan, apply and destroy: the
# account scope, the bootstrap's conformance identity as the app principal,
# and force_destroy, for each cloud a multicloud scenario spans. The lab's
# values come from lab.mk.
#
# ROOT comes from git rather than a relative path, so a scenario works at any
# depth.
#
# The local targets (render, credentials, up, smoke, verify, logs, down,
# clean) are local.sh, which runs artifactweave natively from the scenario's
# own artifactweave_config output, so they work for every scenario. The ones
# that act as the app identity (credentials, up, verify) run the guard first.
# PORT picks the listen port; each cloud has its own default so scenarios on
# different clouds can run side by side.
SHELL := /bin/bash
.DEFAULT_GOAL := help

TERRAFORM ?= terraform
GO ?= go
ROOT := $(shell git rev-parse --show-toplevel)
VERSION ?= $(shell git -C $(ROOT) describe --tags --always --dirty 2>/dev/null || echo devel)
COMMIT ?= $(shell git -C $(ROOT) rev-parse HEAD 2>/dev/null)
export VERSION COMMIT

CLOUD := $(notdir $(patsubst %/,%,$(dir $(CURDIR))))
PORT ?= $(if $(filter gcp,$(CLOUD)),8081,$(if $(filter azure,$(CLOUD)),8082,$(if $(filter multicloud,$(CLOUD)),8084,8083)))
LOCAL := CLOUD=$(CLOUD) PORT=$(PORT) TERRAFORM=$(TERRAFORM) $(ROOT)/deploy/ci/local.sh

include $(ROOT)/deploy/ci/lab.mk
# lab-vars.sh reads these from the environment.
export LAB_GCP_PROJECT LAB_AZURE_SUBSCRIPTION LAB_AZURE_BOOTSTRAP_RG

GUARD := $(if $(filter aws,$(CLOUD)),check-account,$(if $(filter azure,$(CLOUD)),check-subscription,$(if $(filter multicloud,$(CLOUD)),check-account check-subscription)))
# Sourced in the recipe line that runs terraform, so its exports reach it.
LAB_VARS = . $(ROOT)/deploy/ci/lab-vars.sh $(CLOUD) &&

.PHONY: help plan apply render credentials up smoke verify conformance github-vars logs down clean destroy check-account check-subscription

## help: list targets
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/^## //' | column -t -s ':'

## check-account: (aws, multicloud) refuse unless the AWS CLI is signed in to the account holding the lab's zone
check-account:
	@$(ROOT)/deploy/ci/check-account.sh $(LAB_AWS_ZONE_ID) $(LAB_AWS_ZONE_NAME)

## check-subscription: (azure, multicloud) refuse unless az's active subscription and tenant are the lab's
check-subscription:
	@$(ROOT)/deploy/ci/check-subscription.sh $(LAB_AZURE_SUBSCRIPTION)

## plan: terraform plan
plan: $(GUARD)
	$(TERRAFORM) init -input=false
	$(LAB_VARS) $(TERRAFORM) plan -input=false $(TF_VARS)

## apply: create the scenario's resources
apply: $(GUARD)
	$(TERRAFORM) init -input=false
	$(LAB_VARS) $(TERRAFORM) apply -input=false $(TF_VARS)

## render: write .local/artifactweave.yaml from the artifactweave_config output, and the local secrets
render:
	@$(LOCAL) render

## credentials: let this machine act as the scenario's app identity (gcs impersonation, aws export)
credentials: $(GUARD) render
	@$(LOCAL) credentials

## up: build artifactweave and run it natively on 127.0.0.1:$(PORT) against the applied scenario
up: $(GUARD) render
	@$(LOCAL) up

## smoke: a 40 MiB round trip and a downloadRedirect 302 through the running artifactweave
smoke:
	@$(LOCAL) smoke

## verify: run test/platform against the applied scenario, as CI does
verify: $(GUARD) render
	@$(LOCAL) verify

## conformance: run the real-cloud blob contract and probes locally
conformance: $(GUARD) credentials $(CONFORMANCE_DEPS)
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

## logs: follow the running artifactweave's log
logs:
	@$(LOCAL) logs

## down: stop the running artifactweave, keeping .local/data
down:
	@$(LOCAL) down

## clean: stop artifactweave and remove .local
clean:
	@$(LOCAL) clean

## destroy: remove everything Terraform created, stored objects included
destroy: $(GUARD) clean
	$(LAB_VARS) $(TERRAFORM) destroy -input=false $(TF_VARS)
