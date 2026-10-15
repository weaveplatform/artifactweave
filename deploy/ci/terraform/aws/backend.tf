# State lives in the bucket this root creates (state.tf). The first apply
# runs on local state through a git-ignored local_override.tf, which the
# Makefile writes while the bucket does not exist; `make migrate-state` then
# removes the override and moves the state into the bucket (decision 0030).
# The bucket name is fixed, not generated, because a backend block cannot
# read a variable. S3's own conditional writes lock the state
# (use_lockfile), so no DynamoDB table is needed.
terraform {
  backend "s3" {
    bucket       = "artifactweave-tfstate-56920abd"
    key          = "bootstrap/aws.tfstate"
    region       = "eu-west-2"
    encrypt      = true
    use_lockfile = true
  }
}
