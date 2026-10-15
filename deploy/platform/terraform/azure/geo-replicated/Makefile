# azure/geo-replicated: one RA-GZRS storage account, without Shared Key, that
# Azure replicates to the paired region, granted to the bootstrap's
# conformance identity. Apply deploy/ci/terraform/azure first. Plan and apply
# need az login and az account set --subscription to the lab's
# (LAB_AZURE_SUBSCRIPTION in deploy/ci/lab.mk). make up, smoke and verify run
# artifactweave natively from the artifactweave_config output
# (deploy/ci/local.sh); the conformance target is in azure/single-bucket.

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
