# azure/geo-replicated in the lab: make plan, apply and destroy pass these
# and deploy/ci/lab-vars.sh's inputs (the lab's subscription, the bootstrap's
# conformance identity as the app identity, and you as an additional
# principal). The README's "How this scenario is tested" lists them.

TF_VARS = -var name=artifactweave-geo -var location=uksouth

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
