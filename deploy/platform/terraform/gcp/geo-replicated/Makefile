# gcp/geo-replicated: one dual-region GCS bucket that Google replicates
# across two regions, granted to the bootstrap's conformance service account.
# Apply gcp/bootstrap first. Plan and apply need
# `gcloud auth application-default login`. There is no local container or
# conformance target: run artifactweave with the artifactweave_config output,
# or dispatch the scenario through platform-scenarios.yml.

include $(shell git rev-parse --show-toplevel)/deploy/platform/terraform/scenario.mk
