# gcp/geo-replicated: one dual-region GCS bucket that Google replicates across
# two regions, granted to the bootstrap's conformance service account. Apply
# deploy/ci/terraform/gcp first. Plan and apply need `gcloud auth
# application-default login`. make up, smoke and verify run artifactweave
# natively from the artifactweave_config output (deploy/ci/local.sh); the
# conformance target is in gcp/single-bucket.

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
