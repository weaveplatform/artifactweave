# Every constant of the lab that the CI harness and the bootstraps run
# against (decision 0030). scenario.mk and bootstrap.mk include this file,
# so no Makefile or guard script repeats a lab value; each one can be
# overridden on the command line or in the environment. The scenarios'
# Terraform still defaults some of them until each scenario moves to the v2
# contract, which takes them as inputs instead.

# GCP: the project every gcp root and bootstrap uses, and the bootstrap's
# Cloud DNS zone, by its managed zone's name and its DNS name.
LAB_GCP_PROJECT ?= project-0c0b2d7a-9ad7-4221-9a8
LAB_GCP_DNS_ZONE ?= gcp-artifact-weaveplatform-run
LAB_GCP_DNS_NAME ?= gcp.artifact.weaveplatform.run.

# Azure: the subscription every azure root uses, refused by
# check-subscription.sh unless the CLI is on it, and the bootstrap's own
# resource group (its identities, state account and DNS zone).
LAB_AZURE_SUBSCRIPTION ?= c33dbc35-f86d-4b7c-a81e-2a33eb3f7946
LAB_AZURE_BOOTSTRAP_RG ?= artifactweave-bootstrap-rg

# AWS: the region, and the delegated hosted zone check-account.sh recognises
# the lab account by, so the account ID stays out of this public repository.
LAB_AWS_REGION ?= eu-west-2
LAB_AWS_ZONE_ID ?= Z06607493A97HOZP218UN
LAB_AWS_ZONE_NAME ?= aws.artifact.weaveplatform.run.
