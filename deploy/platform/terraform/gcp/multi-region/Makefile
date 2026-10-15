# gcp/multi-region: two regional GCS buckets as members of one artifactweave
# mirror, granted to the bootstrap's conformance service account. Apply
# deploy/ci/terraform/gcp first. Plan and apply need `gcloud auth
# application-default login`. There is no local container or conformance
# target here: run artifactweave with `terraform output -raw
# artifactweave_config`, or let platform-scenarios.yml test it.

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
