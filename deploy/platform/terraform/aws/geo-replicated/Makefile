# aws/geo-replicated in the lab: make plan, apply and destroy pass these and
# deploy/ci/lab-vars.sh's inputs (the bootstrap's conformance role as the
# app role). An operator may create roles anywhere, so the lab's run passes
# no boundary or role path; CI's does (ci-vars.sh). The README's "How this
# scenario is tested" lists them.

TF_VARS = -var name=artifactweave-geo -var region=$(LAB_AWS_REGION) -var replica_region=eu-west-1

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
