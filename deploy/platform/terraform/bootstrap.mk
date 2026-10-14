# The make targets both bootstrap roots share (decision 0030). A bootstrap is
# not a scenario: an operator applies it once per cloud, it outlives every
# scenario, and it keeps its own state in the bucket it creates.
#
# A bootstrap's Makefile sets the variables below, then includes this file as
# its last line:
#
#   include $(shell git rev-parse --show-toplevel)/deploy/platform/terraform/bootstrap.mk
#
#   GUARD        target run first by every target that touches the cloud
#                (Azure's subscription check); empty for none
#   TF_VARS      extra -var flags for plan and apply
#   STATE_PROBE  a command that succeeds once the state backend exists
#   GITHUB_VARS  VARIABLE=output pairs github-vars sets from the outputs
#
# The first apply cannot use the backend it is about to create, so while
# STATE_PROBE fails, init runs against a git-ignored local_override.tf that
# replaces the backend with local state. migrate-state then removes the
# override and moves the state into the new backend.
SHELL := /bin/bash
.DEFAULT_GOAL := help

TERRAFORM ?= terraform
ROOT := $(shell git rev-parse --show-toplevel)
OVERRIDE := local_override.tf

.PHONY: help init plan apply migrate-state github-vars destroy

## help: list targets
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/^## //' | column -t -s ':'

## init: terraform init, on local state until the state backend exists
init: $(GUARD)
	@if $(STATE_PROBE) >/dev/null 2>&1; then \
	  rm -f $(OVERRIDE); \
	else \
	  echo "the state backend does not exist yet: using local state ($(OVERRIDE))"; \
	  printf '%s\n' '# Written by make while the state backend does not exist; git-ignored.' \
	    'terraform {' '  backend "local" {}' '}' > $(OVERRIDE); \
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
	@$(STATE_PROBE) >/dev/null 2>&1 || { echo "the state backend does not exist yet: run make apply first" >&2; exit 1; }
	rm -f $(OVERRIDE)
	$(TERRAFORM) init -input=false -migrate-state -force-copy
	@echo "state moved; the local terraform.tfstate* files are now stale copies and can be deleted"

## github-vars: create the cloud-conformance and cloud-platform environments and set the repository variables
github-vars:
	$(ROOT)/deploy/platform/terraform/github-environments.sh
	@for pair in $(GITHUB_VARS); do \
	  name=$${pair%%=*}; output=$${pair#*=}; \
	  echo "gh variable set $$name"; \
	  gh variable set "$$name" --body "$$($(TERRAFORM) output -raw "$$output")" || exit 1; \
	done

## destroy: refused; the bootstrap is long-lived (see its README for removing parts of it)
destroy:
	@echo "refusing to destroy a bootstrap: CI trust, state and the delegated DNS zone live here." >&2
	@echo "Remove individual resources deliberately, as the README describes." >&2
	@exit 1
