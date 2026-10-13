SHELL := /bin/bash
.DEFAULT_GOAL := help

# The acceptance environment for the GCS backend: a bucket, a service
# account and GitHub federation (Terraform), artifactweave in a local
# container against the bucket, and the real-cloud conformance tests.
TERRAFORM ?= terraform
GO ?= go
ROOT := $(abspath ../../..)
VERSION ?= $(shell git -C $(ROOT) describe --tags --always --dirty 2>/dev/null || echo devel)
COMMIT ?= $(shell git -C $(ROOT) rev-parse HEAD 2>/dev/null)
export VERSION COMMIT

.PHONY: help plan apply credentials up smoke conformance github-vars logs down clean destroy

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## //' | column -t -s ':'

## plan: terraform plan (needs `gcloud auth application-default login`)
plan:
	$(TERRAFORM) init -input=false
	$(TERRAFORM) plan -input=false

## apply: create the bucket, the service account and the GitHub federation
apply:
	$(TERRAFORM) init -input=false
	$(TERRAFORM) apply -input=false

## credentials: write the impersonated credentials and the container configuration
credentials:
	./credentials.sh

## up: build the image and start artifactweave against the bucket (port 8081)
up:
	./up.sh

## smoke: deploy and download 40 MiB through the running container
smoke:
	./smoke.sh

## conformance: run the real-GCS blob contract and probes locally
conformance: credentials
	cd $(ROOT) && \
	  ARTIFACTWEAVE_TEST_GCS_BUCKET=$$($(TERRAFORM) -chdir=$(CURDIR) output -raw bucket) \
	  ARTIFACTWEAVE_TEST_GCS_ACCESS_ID=$$($(TERRAFORM) -chdir=$(CURDIR) output -raw service_account) \
	  GOOGLE_APPLICATION_CREDENTIALS=$(CURDIR)/.secrets/container-credentials.json \
	  GOWORK=off $(GO) test -count=1 -timeout 25m -v -run 'TestContractOnRealGCS|TestRealGCSProbes' ./pkg/blob/gcs/

## github-vars: set the cloud-conformance workflow's repository variables from the outputs
github-vars:
	gh variable set CONFORMANCE_GCS_BUCKET --body "$$($(TERRAFORM) output -raw bucket)"
	gh variable set CONFORMANCE_GCP_SERVICE_ACCOUNT --body "$$($(TERRAFORM) output -raw service_account)"
	gh variable set CONFORMANCE_GCP_WIF_PROVIDER --body "$$($(TERRAFORM) output -raw workload_identity_provider)"

## logs: follow the container's logs
logs:
	docker compose logs --follow

## down: stop the container, keeping its data volume
down:
	docker compose down

## clean: stop the container and remove its data volume and the generated files
clean:
	docker compose down --volumes
	rm -rf .local .secrets

## destroy: remove everything Terraform created (the bucket's objects included)
destroy: clean
	$(TERRAFORM) destroy -input=false
