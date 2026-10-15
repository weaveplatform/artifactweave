# azure/multi-region: two storage accounts in two regions as members of one
# artifactweave mirror, granted to the bootstrap's conformance identity and to
# you. Apply the bootstrap (deploy/ci/terraform/azure) first. Plan and apply
# need az login and az account set --subscription to the lab's
# (LAB_AZURE_SUBSCRIPTION in deploy/ci/lab.mk). There is no local container or
# conformance target here: run artifactweave with `terraform output -raw
# artifactweave_config`, or let platform-scenarios.yml test it.

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
