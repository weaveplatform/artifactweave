# aws/multi-region: two S3 buckets in two regions as members of one
# artifactweave mirror, whose bucket policies grant the bootstrap's
# conformance role. Apply deploy/ci/terraform/aws first. Plan and apply need
# `aws login` and refuse unless that login's account holds the delegated zone.
# make up, smoke and verify run artifactweave natively from the
# artifactweave_config output (deploy/ci/local.sh); the conformance target is
# in aws/single-bucket.

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
