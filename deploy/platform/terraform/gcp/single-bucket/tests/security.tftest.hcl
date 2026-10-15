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

# A scenario run applies as the deployer: it names the app service account,
# so nothing looks up the conformance account, and it labels the bucket for
# reap.
run "scenario_run_grants_the_named_account" {
  command = plan

  variables {
    app_service_account = "artifactweave-deployer@lab.iam.gserviceaccount.com"
    scenario_labels = {
      artifactweave-scenario = "single-bucket"
      expires-at             = "1767225600"
    }
  }

  assert {
    condition     = length(data.google_service_account.conformance) == 0
    error_message = "a named app service account must skip the conformance lookup, which the deployer cannot read"
  }

  assert {
    condition     = google_storage_bucket_iam_member.app.member == "serviceAccount:artifactweave-deployer@lab.iam.gserviceaccount.com"
    error_message = "the bucket grant must go to the named app service account"
  }

  assert {
    condition     = google_storage_bucket.blobs.labels["expires-at"] == "1767225600"
    error_message = "the bucket must carry the scenario labels reap matches"
  }

  assert {
    condition     = output.expires_at == "1767225600"
    error_message = "the expires_at output must carry the label reap reads from state"
  }
}
