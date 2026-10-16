# multicloud/mirror in the lab: make plan, apply and destroy pass these and
# deploy/ci/lab-vars.sh's inputs for all three clouds (the lab's project and
# subscription, each bootstrap's conformance identity as that cloud's app
# identity, and you as an additional principal on Azure). The README's "How
# this scenario is tested" lists them.

TF_VARS = -var name=artifactweave-mc -var home_cloud=aws -var aws_region=$(LAB_AWS_REGION) \
  -var gcp_location=europe-west2 -var azure_location=uksouth

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
