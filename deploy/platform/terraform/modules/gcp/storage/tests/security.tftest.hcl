# The bucket's security baseline (decision 0030), checked on a plan against a
# mocked provider: no credentials and no calls to Google. Scenarios that call
# this module check how they compose it, not these again. One assertion per
# run, so a failure names the invariant it breaks.

mock_provider "google" {}

variables {
  name     = "example-0a1b2c3d-europe-west2"
  location = "europe-west2"
  principals = {
    app      = "example-app@example-project.iam.gserviceaccount.com"
    operator = "example-operator@example-project.iam.gserviceaccount.com"
  }
}

run "public_access_is_prevented" {
  command = plan

  assert {
    condition     = google_storage_bucket.blobs.public_access_prevention == "enforced"
    error_message = "the bucket must enforce public access prevention, so no binding can make a blob public"
  }
}

run "access_is_uniform" {
  command = plan

  assert {
    condition     = google_storage_bucket.blobs.uniform_bucket_level_access
    error_message = "the bucket must use uniform bucket-level access, so IAM alone decides access and no object ACL can make a blob public"
  }
}

run "principals_get_object_rights_on_this_bucket_only" {
  command = plan

  assert {
    condition = {
      for k, m in google_storage_bucket_iam_member.app : k => [m.bucket, m.role, m.member]
      } == {
      app      = ["example-0a1b2c3d-europe-west2", "roles/storage.objectAdmin", "serviceAccount:example-app@example-project.iam.gserviceaccount.com"]
      operator = ["example-0a1b2c3d-europe-west2", "roles/storage.objectAdmin", "serviceAccount:example-operator@example-project.iam.gserviceaccount.com"]
    }
    error_message = "each principal must get object rights on this bucket, and no bucket or project administration"
  }
}

run "labels_reach_the_bucket" {
  command = plan

  variables {
    labels = { artifactweave-scenario = "multi-region", instance = "r1", expires-at = "1767225600" }
  }

  assert {
    condition     = google_storage_bucket.blobs.labels == tomap({ artifactweave-scenario = "multi-region", instance = "r1", expires-at = "1767225600" })
    error_message = "the bucket, the one resource here that takes labels, must carry the labels given"
  }
}
