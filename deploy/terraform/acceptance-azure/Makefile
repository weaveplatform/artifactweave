SHELL := /bin/bash
.DEFAULT_GOAL := help

# The acceptance environment for the azure blob backend: a storage account
# without Shared Key, a managed identity federated with GitHub Actions
# (Terraform), artifactweave in a local container against the account, and
# the real-cloud conformance tests.
TERRAFORM ?= terraform
GO ?= go
# Every target that touches Azure checks that the CLI's active subscription
# is this one first; Terraform is pinned to it too.
SUBSCRIPTION ?= c33dbc35-f86d-4b7c-a81e-2a33eb3f7946
TF_VARS := -var subscription_id=$(SUBSCRIPTION)
ROOT := $(abspath ../../..)
VERSION ?= $(shell git -C $(ROOT) describe --tags --always --dirty 2>/dev/null || echo devel)
COMMIT ?= $(shell git -C $(ROOT) rev-parse HEAD 2>/dev/null)
export VERSION COMMIT

.PHONY: help check-subscription plan apply credentials up smoke conformance github-vars logs down clean destroy

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## //' | column -t -s ':'

## check-subscription: refuse unless az's active subscription and tenant are SUBSCRIPTION's
check-subscription:
	@./check-subscription.sh $(SUBSCRIPTION)

## plan: terraform plan (needs az login and az account set --subscription SUBSCRIPTION)
plan: check-subscription
	$(TERRAFORM) init -input=false
	$(TERRAFORM) plan -input=false $(TF_VARS)

## apply: create the storage account, the container and the GitHub identity
apply: check-subscription
	$(TERRAFORM) init -input=false
	$(TERRAFORM) apply -input=false $(TF_VARS)

## credentials: mint a user delegation SAS and write the container configuration
credentials:
	./credentials.sh

## up: build the image and start artifactweave against the account (port 8082)
up:
	./up.sh

## smoke: deploy and download 40 MiB through the running container
smoke:
	./smoke.sh

## conformance: run the real-Azure blob contract and probes locally, as you (LARGE_RANGES=1 adds ranges up to 4000MiB)
conformance: check-subscription
	cd $(ROOT) && \
	  ARTIFACTWEAVE_TEST_AZURE_ACCOUNT=$$($(TERRAFORM) -chdir=$(CURDIR) output -raw account) \
	  ARTIFACTWEAVE_TEST_AZURE_CONTAINER=$$($(TERRAFORM) -chdir=$(CURDIR) output -raw container) \
	  ARTIFACTWEAVE_TEST_AZURE_LARGE_RANGES=$(LARGE_RANGES) \
	  GOWORK=off $(GO) test -count=1 -timeout 25m -v -run 'TestContractOnRealAzure|TestRealAzureProbes' ./pkg/blob/azure/

## github-vars: create the cloud-conformance environment and set the workflow's repository variables
github-vars:
	gh api -X PUT repos/{owner}/{repo}/environments/cloud-conformance >/dev/null
	gh variable set CONFORMANCE_AZURE_ACCOUNT --body "$$($(TERRAFORM) output -raw account)"
	gh variable set CONFORMANCE_AZURE_CONTAINER --body "$$($(TERRAFORM) output -raw container)"
	gh variable set CONFORMANCE_AZURE_CLIENT_ID --body "$$($(TERRAFORM) output -raw client_id)"
	gh variable set CONFORMANCE_AZURE_TENANT_ID --body "$$($(TERRAFORM) output -raw tenant_id)"
	gh variable set CONFORMANCE_AZURE_SUBSCRIPTION_ID --body "$$($(TERRAFORM) output -raw subscription_id)"

## logs: follow the container's logs
logs:
	docker compose logs --follow

## down: stop the container, keeping its data volume
down:
	docker compose down

## clean: stop the container and remove its data volume and the generated files
clean:
	docker compose down --volumes
	rm -rf .local

## destroy: remove everything Terraform created (the account's blobs included)
destroy: check-subscription clean
	$(TERRAFORM) destroy -input=false $(TF_VARS)
