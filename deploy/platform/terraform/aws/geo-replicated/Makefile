# aws/geo-replicated: a primary S3 bucket that cross-region replication copies
# into a replica in a second region, both granted to the bootstrap's
# conformance role through their bucket policies. Apply
# deploy/ci/terraform/aws first. Plan and apply need `aws login` and refuse
# unless that login's account holds the delegated zone. make up, smoke and
# verify run artifactweave natively from the artifactweave_config output
# (deploy/ci/local.sh); the conformance target is in aws/single-bucket.

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
