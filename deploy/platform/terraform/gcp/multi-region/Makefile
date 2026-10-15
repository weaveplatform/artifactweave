# gcp/multi-region in the lab: make plan, apply and destroy pass these and
# deploy/ci/lab-vars.sh's inputs (the lab's project, and the bootstrap's
# conformance service account as the app service account). The README's
# "How this scenario is tested" lists them.

TF_VARS = -var name=artifactweave-mr -var locations='["europe-west2", "europe-west1"]'

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
