# The make targets every deploy/platform scenario shares (decision 0030).
#
# A scenario's Makefile sets the variables below, then includes this file as
# its last line:
#
#   include $(shell git rev-parse --show-toplevel)/deploy/platform/terraform/scenario.mk
#
#   GUARD               target run first by every target that touches the cloud
#                       (Azure's subscription check); empty for none
#   TF_VARS             extra -var flags for plan, apply and destroy
#   CONFORMANCE_DEPS    prerequisites of conformance
#   CONFORMANCE_ENV     environment for the real-cloud tests; define it with =, not
#                       :=, so the $$(terraform output ...) calls run in the recipe
#   CONFORMANCE_RUN     the go test -run pattern
#   CONFORMANCE_PKG     the package holding the real-cloud tests
#   GITHUB_ENVIRONMENT  GitHub environment github-vars creates first; empty for none
#   GITHUB_VARS         VARIABLE=output pairs github-vars sets from the outputs
#   CLEAN_FILES         generated files clean removes
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

.PHONY: help plan apply credentials up smoke conformance github-vars logs down clean destroy

## help: list targets
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/^## //' | column -t -s ':'

## plan: terraform plan
plan: $(GUARD)
	$(TERRAFORM) init -input=false
	$(TERRAFORM) plan -input=false $(TF_VARS)

## apply: create the scenario's resources
apply: $(GUARD)
	$(TERRAFORM) init -input=false
	$(TERRAFORM) apply -input=false $(TF_VARS)

## credentials: render the container's configuration and credentials into .local/
credentials:
	./credentials.sh

## up: build the image and start artifactweave in a local container against the scenario
up:
	./up.sh

## smoke: deploy and download 40 MiB through the running container
smoke:
	./smoke.sh

## conformance: run the real-cloud blob contract and probes locally
conformance: $(CONFORMANCE_DEPS)
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
	docker compose logs --follow

## down: stop the container, keeping its data volume
down:
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
