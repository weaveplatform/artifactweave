SHELL := /bin/bash
.DEFAULT_GOAL := help

# Built outside any parent go.work: local runs match CI.
export GOWORK := off

GO ?= go
GOLANGCI_LINT ?= golangci-lint
MODULE := $(shell GOWORK=off $(GO) list -m)
COVER_DIR := cover
BIN_DIR := bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo devel)
COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X $(MODULE)/internal/buildinfo.version=$(VERSION) -X $(MODULE)/internal/buildinfo.commit=$(COMMIT) -X $(MODULE)/internal/buildinfo.buildDate=$(BUILD_DATE)
PLATFORMS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
UNIT_PKGS = $(shell GOWORK=off $(GO) list ./... | grep -v /test/acceptance)

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## //' | column -t -s ':'

## init: rename the module after creating a repository from the template (make init NAME=<repo>)
init:
	@test -n "$(NAME)" || { echo "usage: make init NAME=<repository name>"; exit 1; }
	@grep -rl --exclude-dir=.git artifactweave . | xargs sed -i.bak 's/artifactweave/$(NAME)/g'
	@find . -name '*.bak' -not -path './.git/*' -delete
	$(GO) mod tidy
	@echo "renamed to github.com/weaveplatform/$(NAME); review README.md and CODEOWNERS"

## fmt: apply the formatters configured in .golangci.yml
fmt:
	$(GOLANGCI_LINT) fmt --config .golangci.yml ./...

## lint: golangci-lint over the whole module
lint:
	$(GOLANGCI_LINT) run --config .golangci.yml --new=false --fix=false ./...

## vet: go vet
vet:
	$(GO) vet ./...

## test: unit tests (race, shuffle); coverage to cover/unit
test:
	@rm -rf $(COVER_DIR)/unit && mkdir -p $(COVER_DIR)/unit
	$(GO) test -race -shuffle=on -count=1 -cover -coverpkg=$(MODULE)/... $(UNIT_PKGS) -args -test.gocoverdir=$(CURDIR)/$(COVER_DIR)/unit

## accept: godog acceptance features against the coverage-instrumented binary (Linux; needs Docker for @storage)
accept:
	@rm -rf $(COVER_DIR)/accept && mkdir -p $(COVER_DIR)/accept
	ARTIFACTWEAVE_GOCOVERDIR=$(CURDIR)/$(COVER_DIR)/accept $(GO) test -count=1 ./test/acceptance/...

# The Terraform scenarios applied against artifactweave: the app-layer
# references in deploy/artifactweave/terraform.
TERRAFORM_SCENARIOS ?= deploy/artifactweave/terraform/repositories \
	deploy/artifactweave/terraform/identity deploy/artifactweave/terraform/oidc

## terraform: validate the Terraform scenarios against the pinned providers from a filesystem mirror (TERRAFORM_MODE=apply also applies, plans and destroys each against one -cover server)
# Apply writes the server's coverage to cover/terraform for make cover; CI
# uploads it as cover-terraform.
terraform:
ifeq ($(TERRAFORM_MODE),apply)
	@rm -rf $(COVER_DIR)/terraform && mkdir -p $(COVER_DIR)/terraform
	GOCOVERDIR=$(CURDIR)/$(COVER_DIR)/terraform ./test/terraform/run.sh apply $(TERRAFORM_SCENARIOS)
else
	./test/terraform/run.sh $(or $(TERRAFORM_MODE),validate) $(TERRAFORM_SCENARIOS)
endif

## deploy-check: fmt, validate, tflint, trivy, mocked terraform test and the terraform-docs drift check over deploy/ and test/terraform, with pinned tools in .tools/
deploy-check:
	./deploy/check.sh

## deploy-docs: rewrite the terraform-docs sections of the deploy/ READMEs
deploy-docs:
	./deploy/check.sh docs

## capture-aql: refresh pkg/aql/aqltest/testdata with the AQL the pinned jf sends (needs ARTIFACTWEAVE_JF_BIN)
capture-aql:
	./test/acceptance/capture-aql.sh

## cover: merge every cover/* directory and enforce .testcoverage.yml (>=95% total, >=90% per package)
cover:
	@dirs=$$(find $(COVER_DIR) -mindepth 1 -maxdepth 1 -type d ! -name '.merged' | paste -sd, -); \
	if [ -z "$$dirs" ]; then echo "no coverage data; run make test first"; exit 1; fi; \
	rm -rf $(COVER_DIR)/.merged && mkdir -p $(COVER_DIR)/.merged && \
	$(GO) tool covdata merge -i=$$dirs -o=$(COVER_DIR)/.merged && \
	$(GO) tool covdata textfmt -i=$(COVER_DIR)/.merged -o=$(COVER_DIR)/coverage.out && \
	$(GO) tool covdata percent -i=$(COVER_DIR)/.merged
	$(GO) tool go-test-coverage --config=.testcoverage.yml

## vuln: govulncheck (version pinned in go.mod's tool block, kept current with everything else)
vuln:
	$(GO) tool govulncheck ./...

## build: cross-compile every cmd/* binary for each release platform into bin/ (CGO disabled)
# disable_grpc_modules drops the Cloud Storage SDK's gRPC direct-path
# imports, which the HTTP client the gcs backend uses never needs; a client
# links 29 MB instead of 47 MB (decision 0027).
BUILD_TAGS := disable_grpc_modules
build:
	@mkdir -p $(BIN_DIR)
	@for d in $$(find cmd -mindepth 1 -maxdepth 1 -type d); do \
		name=$${d#cmd/}; \
		for p in $(PLATFORMS); do \
			os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
			echo "$$name $$os/$$arch"; \
			CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -tags $(BUILD_TAGS) -ldflags "$(LDFLAGS)" \
				-o $(BIN_DIR)/$$name-$$os-$$arch$$ext ./$$d || exit 1; \
		done; \
	done

## docker: build the container image from the Dockerfile, tagged with the version
docker:
	docker build -t artifactweave:$(VERSION) --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) .

## platform-<target>: run a target of a deploy/platform scenario, e.g. make platform-plan SCENARIO=gcp/single-bucket
PLATFORM_DIR := deploy/platform/terraform
platform-%:
	@[ -n "$(SCENARIO)" ] && [ -f $(PLATFORM_DIR)/$(SCENARIO)/Makefile ] || { \
		echo "set SCENARIO to one of: $$(cd $(PLATFORM_DIR) && ls -d */*/Makefile | sed 's|/Makefile||' | tr '\n' ' ')" >&2; exit 1; }
	$(MAKE) -C $(PLATFORM_DIR)/$(SCENARIO) $*

## gcp-<target>: platform-<target> for gcp/single-bucket (the GCS acceptance environment), e.g. make gcp-up
gcp-%:
	$(MAKE) -C $(PLATFORM_DIR)/gcp/single-bucket $*

## azure-<target>: platform-<target> for azure/single-bucket (the Azure acceptance environment), e.g. make azure-up
azure-%:
	$(MAKE) -C $(PLATFORM_DIR)/azure/single-bucket $*

## bundle: merge api/src/<service>/*.yaml fragments into api/<service>.openapi.yaml
# Authors edit fragments (one per API family) so families can be written in
# parallel; the bundled file is what codegen and the parity test read.
bundle:
	$(GO) run ./tools/openapi-bundle -src api/src -out api

## generate: bundle the specs, regenerate the strict chi servers, Go clients and 501 stubs
# Pinned by version so every developer and CI produce byte-identical output; the
# drift check in verify-generate depends on that.
OAPI_CODEGEN := github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0
# One service per api/src/<service>/info.yaml; adding a service is a new directory.
SPECS := $(patsubst api/src/%/info.yaml,%,$(wildcard api/src/*/info.yaml))
generate: bundle
	@for s in $(SPECS); do \
		echo "generate $$s"; \
		$(GO) run $(OAPI_CODEGEN) -config api/oapi-server-$$s.yaml api/$$s.openapi.yaml || exit 1; \
		$(GO) run $(OAPI_CODEGEN) -config api/oapi-client-$$s.yaml api/$$s.openapi.yaml || exit 1; \
		$(GO) run ./tools/stubgen internal/server/gen/$$s/server.gen.go || exit 1; \
	done

## verify-generate: fail if make generate would change the committed generated code
verify-generate: generate
	git diff --exit-code -- api/*.openapi.yaml internal/server/gen pkg/client

## oracle-fetch: cache JFrog's published OpenAPI blocks under .specoracle (gitignored; test oracle only)
oracle-fetch:
	$(GO) run ./tools/specoracle fetch -cache .specoracle

## oracle-inventory: write api/oracle-inventory.json and docs/research/14-data-model-inventory.md from the cache
oracle-inventory:
	$(GO) run ./tools/specoracle inventory -cache .specoracle -api api

## scaffold: draft a spec fragment from one cached family (make scaffold FAMILY=<slug> SERVICE=<a directory under api/src>)
scaffold:
	@test -n "$(FAMILY)" -a -n "$(SERVICE)" || { echo "usage: make scaffold FAMILY=<cache family> SERVICE=<a directory under api/src>"; exit 1; }
	$(GO) run ./tools/specoracle scaffold -cache .specoracle -family $(FAMILY) -service $(SERVICE) \
		-out api/src/$(SERVICE)/paths-$$(echo $(FAMILY) | sed -E 's/^(artifactory|administration|projects|integrations)-//; s/-api$$//').yaml

## parity: compare api/*.openapi.yaml with the cached JFrog definitions and print the report
parity:
	$(GO) run ./tools/specoracle report -cache .specoracle -api api

## parity-baseline: rewrite api/parity-baseline.json from the current report (review the diff)
parity-baseline:
	$(GO) run ./tools/specoracle baseline -cache .specoracle -api api -update

## gate: everything CI runs, in order
gate: vet lint test cover vuln build

.PHONY: docker bundle oracle-inventory scaffold oracle-fetch parity parity-baseline help init fmt lint vet test accept cover vuln build generate verify-generate gate terraform deploy-check deploy-docs capture-aql
