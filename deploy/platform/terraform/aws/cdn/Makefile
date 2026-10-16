# aws/cdn in the lab: make plan, apply and destroy pass these and
# deploy/ci/lab-vars.sh's inputs (the bootstrap's conformance role as the app
# role, and a CloudFront key pair it keeps in .secrets/cloudfront/). The CDN
# host is a sibling under the lab's zone. The README's "How this scenario is
# tested" lists them.

TF_VARS = -var name=artifactweave-cdn -var region=$(LAB_AWS_REGION) \
  -var zone_id=$(LAB_AWS_ZONE_ID) -var domain_name=cdn.$(patsubst %.,%,$(LAB_AWS_ZONE_NAME))

include $(shell git rev-parse --show-toplevel)/deploy/ci/scenario.mk
