# azure/geo-replicated: one RA-GZRS storage account, without Shared Key, that
# Azure replicates to the paired region, granted to the bootstrap's
# conformance identity. Apply deploy/ci/terraform/azure first. Plan and apply
# need az login and az account set --subscription to the lab's
# (LAB_AZURE_SUBSCRIPTION in deploy/ci/lab.mk). There is no local container or
# conformance target: run artifactweave with the artifactweave_config output,
# or dispatch the scenario through platform-scenarios.yml.

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
