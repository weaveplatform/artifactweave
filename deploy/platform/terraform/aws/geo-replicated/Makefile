# aws/geo-replicated: a primary S3 bucket that cross-region replication copies
# into a replica in a second region, both granted to the bootstrap's
# conformance role through their bucket policies. Apply
# deploy/ci/terraform/aws first. Plan and apply need `aws login` and refuse
# unless that login's account holds the delegated zone. There is no local
# container or conformance target: run artifactweave with the
# artifactweave_config output, or dispatch the scenario through
# platform-scenarios.yml.

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
