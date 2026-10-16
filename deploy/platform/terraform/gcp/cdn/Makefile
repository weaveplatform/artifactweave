# gcp/cdn in the lab: make plan, apply and destroy pass these and
# deploy/ci/lab-vars.sh's inputs (the lab's project, the bootstrap's
# conformance service account as the app service account, and a signed URL
# key it keeps in .secrets/cloud-cdn/). The CDN host is a sibling under the
# lab's zone. A new instance takes make apply, make signed-url-key, then
# make apply again, which lab-vars.sh turns into the fill account's grant
# once the key is on the backend bucket. The README's "How this scenario is
# tested" lists them. Plan and apply need
# `gcloud auth application-default login`.

TF_VARS = -var name=artifactweave-cdn -var location=europe-west2 \
  -var dns_zone=$(LAB_GCP_DNS_ZONE) -var domain_name=cdn.$(patsubst %.,%,$(LAB_GCP_DNS_NAME))

.PHONY: signed-url-key

## signed-url-key: (gcp/cdn) add .secrets/cloud-cdn's key to the applied backend bucket; then make apply again
signed-url-key:
	$(ROOT)/deploy/ci/cloud-cdn-key.sh add $(CURDIR)/.secrets/cloud-cdn/cloud-cdn-key $(LAB_GCP_PROJECT) \
	  "$$($(TERRAFORM) output -raw backend_bucket)" "$$($(TERRAFORM) output -raw signed_url_key_name)"

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
