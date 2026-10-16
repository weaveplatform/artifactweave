# azure/cdn in the lab: make plan, apply and destroy pass these and
# deploy/ci/lab-vars.sh's inputs (the lab's subscription, the bootstrap's
# conformance identity as the app identity, you as an additional principal,
# and your public address as the one network the account's public endpoint
# answers). The README's "How this scenario is tested" lists them. The Front
# Door Premium profile bills by the hour while it exists: destroy it when
# done. make verify approves Front Door's private endpoint connection itself;
# otherwise approve it as the README's Prerequisites say.

TF_VARS = -var name=artifactweave-cdn -var location=uksouth

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
