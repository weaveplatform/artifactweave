# What expires, and when (decision 0030), on a mocked plan. Each rule is
# checked at its position in main.tf.

mock_provider "google" {}

variables {
  name         = "example-0a1b2c3d-europe-west2"
  location     = "europe-west2"
  principals   = { app = "example-app@example-project.iam.gserviceaccount.com" }
  store_prefix = "local/"
}

run "abandoned_upload_parts_expire_after_two_days" {
  command = plan

  assert {
    condition = (
      one(google_storage_bucket.blobs.lifecycle_rule[0].condition).matches_prefix == tolist(["local/_uploads/"]) &&
      one(google_storage_bucket.blobs.lifecycle_rule[0].condition).age == 2 &&
      one(google_storage_bucket.blobs.lifecycle_rule[0].action).type == "Delete"
    )
    error_message = "the first rule must delete the upload parts an interrupted upload leaves under <store_prefix>_uploads/ after 2 days"
  }
}

run "expiring_prefixes_expire_after_their_days" {
  command = plan

  variables {
    expiring_prefixes = { "conformance/" = 1, "scratch/" = 7 }
  }

  assert {
    condition = [
      for r in slice(google_storage_bucket.blobs.lifecycle_rule, 1, 3) :
      "${one(one(r.condition).matches_prefix)} ${one(r.condition).age} ${one(r.action).type}"
    ] == ["conformance/ 1 Delete", "scratch/ 7 Delete"]
    error_message = "each expiring prefix must get a rule deleting its objects after its days"
  }
}

run "deleted_objects_are_not_kept" {
  command = plan

  assert {
    condition     = one(google_storage_bucket.blobs.soft_delete_policy).retention_duration_seconds == 0
    error_message = "soft delete must be off: the backend deletes every part it composes, and each would be billed for the retention period"
  }
}
