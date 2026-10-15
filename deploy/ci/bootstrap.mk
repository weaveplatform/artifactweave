# The make targets the three bootstrap roots share (decision 0030). A
# bootstrap is not a scenario: an operator applies it once per cloud, it
# outlives every scenario, and it keeps its own state in the store it creates.
# Run them from the repository root with make ci-<target> CLOUD=<cloud>, or
# from deploy/ci/terraform/<cloud>.
#
# A bootstrap's Makefile sets the variables below, then includes this file as
# its last line:
#
#   include $(shell git rev-parse --show-toplevel)/deploy/ci/bootstrap.mk
#
#   TF_VARS      extra -var flags for plan and apply; define it with = when it
#                names a LAB_* value, which lab.mk sets after the Makefile
#   STATE_PROBE  a command that succeeds once the state backend exists;
#                define it with = for the same reason
#   GITHUB_VARS  VARIABLE=output pairs github-vars sets from the outputs
#   GITHUB_SECRETS  SECRET=output pairs github-vars stores as repository
#                secrets instead: values that would otherwise appear in the
#                public workflow logs (AWS role ARNs carry the account ID)
#
# The cloud is the directory's name, and it picks the guard every target that
# touches the cloud runs first: check-account.sh on AWS, check-subscription.sh
# on Azure, none on GCP. The lab's values come from lab.mk.
#
# The first apply cannot use the backend it is about to create, so only then,
# with FIRST_RUN=1, does init write a git-ignored local_override.tf that
# replaces the backend with local state; migrate-state then removes the
# override and moves the state into the new backend. Without FIRST_RUN, a
# state probe that fails stops init: an expired login or a moved directory
# must never plan a bootstrap from empty local state.
SHELL := /bin/bash
.DEFAULT_GOAL := help

TERRAFORM ?= terraform
ROOT := $(shell git rev-parse --show-toplevel)
OVERRIDE := local_override.tf
CLOUD := $(notdir $(CURDIR))

include $(ROOT)/deploy/ci/lab.mk

GUARD := $(if $(filter aws,$(CLOUD)),check-account,$(if $(filter azure,$(CLOUD)),check-subscription))

.PHONY: help init plan apply migrate-state github-vars destroy check-account check-subscription

## help: list targets
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/^## //' | column -t -s ':'

## check-account: (aws) refuse unless the AWS CLI is signed in to the account holding the lab's zone
check-account:
	@$(ROOT)/deploy/ci/check-account.sh $(LAB_AWS_ZONE_ID) $(LAB_AWS_ZONE_NAME)

## check-subscription: (azure) refuse unless az's active subscription and tenant are the lab's
check-subscription:
	@$(ROOT)/deploy/ci/check-subscription.sh $(LAB_AZURE_SUBSCRIPTION)

## init: terraform init against the state backend; FIRST_RUN=1 uses local state until the backend exists
init: $(GUARD)
	@if $(STATE_PROBE) >/dev/null 2>&1; then \
	  rm -f $(OVERRIDE); \
	elif [ "$(FIRST_RUN)" = 1 ]; then \
	  echo "the state backend does not exist yet: using local state ($(OVERRIDE))"; \
	  printf '%s\n' '# Written by make while the state backend does not exist; git-ignored.' \
	    'terraform {' '  backend "local" {}' '}' > $(OVERRIDE); \
	else \
	  echo "error: the state backend is unreachable ($(STATE_PROBE))." >&2; \
	  echo "       Check the CLI login. Only the very first apply of this bootstrap, before" >&2; \
	  echo "       its state store exists, runs with FIRST_RUN=1 (make ci-apply CLOUD=$(CLOUD) FIRST_RUN=1)." >&2; \
	  exit 1; \
	fi
	$(TERRAFORM) init -input=false

## plan: terraform plan
plan: init
	$(TERRAFORM) plan -input=false $(TF_VARS)

## apply: create or update the bootstrap
apply: init
	$(TERRAFORM) apply -input=false $(TF_VARS)

## migrate-state: after the first apply, move the local state into the backend it created
migrate-state: $(GUARD)
	@$(STATE_PROBE) >/dev/null 2>&1 || { echo "the state backend does not exist yet: run make apply FIRST_RUN=1 first" >&2; exit 1; }
	rm -f $(OVERRIDE)
	$(TERRAFORM) init -input=false -migrate-state -force-copy
	@echo "state moved; the local terraform.tfstate* files are now stale copies and can be deleted"

## github-vars: create the cloud-conformance and cloud-platform environments and set the repository variables and secrets
github-vars:
	$(ROOT)/deploy/ci/github-environments.sh
	@for pair in $(GITHUB_VARS); do \
	  name=$${pair%%=*}; output=$${pair#*=}; \
	  echo "gh variable set $$name"; \
	  gh variable set "$$name" --body "$$($(TERRAFORM) output -raw "$$output")" || exit 1; \
	done
	@for pair in $(GITHUB_SECRETS); do \
	  name=$${pair%%=*}; output=$${pair#*=}; \
	  echo "gh secret set $$name"; \
	  $(TERRAFORM) output -raw "$$output" | gh secret set "$$name" || exit 1; \
	done

## destroy: refused; the bootstrap is long-lived (see its README for removing parts of it)
destroy:
	@echo "refusing to destroy a bootstrap: CI trust, state and the delegated DNS zone live here." >&2
	@echo "Remove individual resources deliberately, as the README describes." >&2
	@exit 1
