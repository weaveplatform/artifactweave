# aws/multi-region: two S3 buckets in two regions as members of one
# artifactweave mirror, whose bucket policies grant the bootstrap's
# conformance role. Apply deploy/ci/terraform/aws first. Plan and apply need
# `aws login` and refuse unless that login's account holds the delegated zone.
# There is no local container or conformance target here: run artifactweave
# with `terraform output -raw artifactweave_config`, or let
# platform-scenarios.yml test it.

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
