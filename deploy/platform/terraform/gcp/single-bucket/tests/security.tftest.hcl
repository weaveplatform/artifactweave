# The acceptance bucket's security invariants, checked on a plan against
# mocked providers: no credentials and no calls to Google (decision 0030).

mock_provider "google" {}
mock_provider "random" {}

run "bucket_is_private" {
  command = plan

  assert {
    condition     = google_storage_bucket.blobs.public_access_prevention == "enforced"
    error_message = "the bucket must enforce public access prevention, so no binding can make a blob public"
  }

  assert {
    condition     = google_storage_bucket.blobs.uniform_bucket_level_access
    error_message = "the bucket must use uniform bucket-level access, so no object ACL can make a blob public"
  }
}

run "abandoned_parts_expire" {
  command = plan

  assert {
    condition = anytrue([
      for rule in google_storage_bucket.blobs.lifecycle_rule :
      contains(one(rule.condition).matches_prefix, "${var.store_prefix}_uploads/") && one(rule.action).type == "Delete"
    ])
    error_message = "a lifecycle rule must delete the upload parts an interrupted run leaves under _uploads/"
  }

  assert {
    condition = anytrue([
      for rule in google_storage_bucket.blobs.lifecycle_rule :
      contains(one(rule.condition).matches_prefix, "conformance/") && one(rule.action).type == "Delete"
    ])
    error_message = "a lifecycle rule must delete what an interrupted conformance run leaves under conformance/"
  }
}

run "app_reaches_this_bucket_only" {
  command = plan

  assert {
    condition     = google_storage_bucket_iam_member.app.role == "roles/storage.objectAdmin"
    error_message = "the service account gets object rights on this bucket, not bucket or project administration"
  }
}
