# The bucket's security invariants, checked on a plan against a mocked
# provider: no credentials and no calls to Google (decision 0030). Scenarios
# that call this module check how they compose it, not these again.

mock_provider "google" {}

variables {
  name                = "awscn-r1-0a1b2c3d-europe-west2"
  location            = "europe-west2"
  store_prefix        = "local/"
  app_service_account = "artifactweave-deployer@lab.iam.gserviceaccount.com"
}

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

run "bucket_is_one_region" {
  command = plan

  assert {
    condition     = google_storage_bucket.blobs.location == "EUROPE-WEST2"
    error_message = "the bucket must be the single region given: the mirror keeps the other copy, and a dual-region bucket's replicas never count toward its quorum (decision 0028)"
  }
}

run "abandoned_parts_expire" {
  command = plan

  assert {
    condition = anytrue([
      for rule in google_storage_bucket.blobs.lifecycle_rule :
      contains(one(rule.condition).matches_prefix, "local/_uploads/") && one(rule.action).type == "Delete"
    ])
    error_message = "a lifecycle rule must delete the upload parts an interrupted run leaves under _uploads/"
  }
}

run "app_reaches_this_bucket_only" {
  command = plan

  assert {
    condition     = google_storage_bucket_iam_member.app.role == "roles/storage.objectAdmin"
    error_message = "the service account gets object rights on this bucket, not bucket or project administration"
  }

  assert {
    condition     = google_storage_bucket_iam_member.app.member == "serviceAccount:artifactweave-deployer@lab.iam.gserviceaccount.com"
    error_message = "the grant must go to the service account artifactweave runs as"
  }

  assert {
    condition     = output.signed_url_prefix == "storage.googleapis.com/awscn-r1-0a1b2c3d-europe-west2/"
    error_message = "signed_url_prefix must be the path-style prefix the backend signs"
  }
}

run "labels_reach_the_bucket" {
  command = plan

  variables {
    labels = { artifactweave-scenario = "multi-region", instance = "r1", expires-at = "1767225600" }
  }

  assert {
    condition     = google_storage_bucket.blobs.labels["expires-at"] == "1767225600" && google_storage_bucket.blobs.labels["instance"] == "r1"
    error_message = "the bucket must carry the scenario labels reap matches"
  }
}
