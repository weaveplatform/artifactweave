# aws/multi-region in the lab: make plan, apply and destroy pass these and
# deploy/ci/lab-vars.sh's inputs (the bootstrap's conformance role as the
# app role). The README's "How this scenario is tested" lists them.

TF_VARS = -var name=artifactweave-mr -var regions='["$(LAB_AWS_REGION)", "eu-west-1"]'

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
