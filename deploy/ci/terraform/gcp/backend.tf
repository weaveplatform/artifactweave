# State lives in the bucket this root creates (state.tf). The first apply
# runs on local state through a git-ignored local_override.tf, which the
# Makefile writes while the bucket does not exist; `make migrate-state` then
# removes the override and moves the state into the bucket (decision 0030).
# The bucket name is fixed, not generated, because a backend block cannot
# read a variable.
terraform {
  backend "gcs" {
    bucket = "artifactweave-tfstate-1025324209347"
    prefix = "bootstrap/gcp"
  }
}
