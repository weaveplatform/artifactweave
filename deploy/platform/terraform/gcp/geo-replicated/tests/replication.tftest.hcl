# The geo-replicated bucket's invariants, checked on a plan against mocked
# providers: no credentials and no calls to Google (decision 0030).

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

  assert {
    condition     = google_storage_bucket_iam_member.app.role == "roles/storage.objectAdmin"
    error_message = "the service account gets object rights on this bucket, not bucket or project administration"
  }
}

run "dual_region_with_default_replication" {
  command = plan

  assert {
    condition     = google_storage_bucket.blobs.location == "EUR4"
    error_message = "the default must be a predefined dual-region, so Google keeps a copy in a second region"
  }

  assert {
    condition     = google_storage_bucket.blobs.rpo == "DEFAULT"
    error_message = "turbo replication is billed per GB written, so it must be opt-in"
  }

  assert {
    condition     = length(google_storage_bucket.blobs.custom_placement_config) == 0
    error_message = "a predefined dual-region takes no custom placement"
  }
}

run "turbo_and_configurable_dual_region" {
  command = plan

  variables {
    location          = "eu"
    data_locations    = ["europe-west1", "europe-west4"]
    turbo_replication = true
  }

  assert {
    condition     = google_storage_bucket.blobs.rpo == "ASYNC_TURBO"
    error_message = "turbo_replication must select the 15-minute recovery point objective"
  }

  assert {
    condition     = google_storage_bucket.blobs.location == "EU" && one(google_storage_bucket.blobs.custom_placement_config).data_locations == toset(["EUROPE-WEST1", "EUROPE-WEST4"])
    error_message = "data_locations must place a configurable dual-region in the two named regions"
  }
}

run "one_region_is_not_a_dual_region" {
  command = plan

  variables {
    data_locations = ["europe-west1"]
  }

  expect_failures = [var.data_locations]
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
}

# A scenario run applies as the deployer: it names the app service account,
# so nothing looks up the conformance account, and it labels the bucket for
# reap.
run "scenario_run_grants_the_named_account" {
  command = plan

  variables {
    app_service_account = "artifactweave-deployer@lab.iam.gserviceaccount.com"
    scenario_labels = {
      artifactweave-scenario = "geo-replicated"
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
    condition     = google_storage_bucket.blobs.labels["expires-at"] == "1767225600" && output.expires_at == "1767225600"
    error_message = "the bucket and the expires_at output must carry the label reap matches"
  }
}
