# azure/single-bucket in the lab, and the Azure conformance account
# (cloud-conformance.yml): make plan, apply and destroy pass these and
# deploy/ci/lab-vars.sh's inputs (the lab's subscription, the bootstrap's
# conformance identity as the app identity, and you as an additional
# principal). The README's "How this scenario is tested" lists them. The
# local run writes under local/, and the conformance tests under
# conformance/, which expires after a day in case a run skips its cleanup.

TF_VARS = -var name=artifactweave-acc -var location=uksouth -var store_prefix=local/ \
  -var expiring_prefixes='{"conformance/" = 1}'

CONFORMANCE_ENV = \
  ARTIFACTWEAVE_TEST_AZURE_ACCOUNT=$$($(TERRAFORM) -chdir=$(CURDIR) output -raw account) \
  ARTIFACTWEAVE_TEST_AZURE_CONTAINER=$$($(TERRAFORM) -chdir=$(CURDIR) output -raw container) \
  ARTIFACTWEAVE_TEST_AZURE_LARGE_RANGES=$(LARGE_RANGES)
CONFORMANCE_RUN := TestAccContractOnRealAzure|TestAccRealAzureProbes
CONFORMANCE_PKG := ./pkg/blob/azure/
# The identity, tenant and subscription variables, and the environment,
# come from deploy/ci/terraform/azure.
GITHUB_VARS := \
  CONFORMANCE_AZURE_ACCOUNT=account \
  CONFORMANCE_AZURE_CONTAINER=container

## LARGE_RANGES=1: with conformance, adds Put Block From URL ranges up to 4000MiB (about a minute)

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
