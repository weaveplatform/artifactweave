# How the module reads its inputs, on a mocked plan: placement, the prefix
# normalisation the backend applies, the checks that fail at plan rather than
# at apply, and the defaults a consumer gets.

mock_provider "google" {}

variables {
  name       = "example-0a1b2c3d-europe-west2"
  location   = "europe-west2"
  principals = { app = "example-app@example-project.iam.gserviceaccount.com" }
}

run "a_location_alone_is_one_region" {
  command = plan

  assert {
    condition     = google_storage_bucket.blobs.location == "EUROPE-WEST2" && length(google_storage_bucket.blobs.custom_placement_config) == 0
    error_message = "with no data_locations the bucket must be the location given, with no custom placement"
  }
}

run "data_locations_make_a_dual_region" {
  command = plan

  variables {
    location       = "eu"
    data_locations = ["europe-west1", "europe-west4"]
    rpo            = "ASYNC_TURBO"
  }

  assert {
    condition = (
      google_storage_bucket.blobs.location == "EU" &&
      one(google_storage_bucket.blobs.custom_placement_config).data_locations == toset(["EUROPE-WEST1", "EUROPE-WEST4"]) &&
      google_storage_bucket.blobs.rpo == "ASYNC_TURBO"
    )
    error_message = "data_locations must place the bucket in those two regions of location, replicating at the rpo given"
  }
}

run "store_prefix_defaults_to_the_bucket_root" {
  command = plan

  assert {
    condition     = one(google_storage_bucket.blobs.lifecycle_rule[0].condition).matches_prefix == tolist(["_uploads/"])
    error_message = "with no store_prefix, upload parts live under _uploads/ at the bucket's root"
  }
}

run "store_prefix_gains_a_trailing_slash" {
  command = plan

  variables {
    store_prefix = "local"
  }

  assert {
    condition     = one(google_storage_bucket.blobs.lifecycle_rule[0].condition).matches_prefix == tolist(["local/_uploads/"])
    error_message = "store_prefix must be normalised as the backend does, with a trailing slash"
  }
}

run "store_prefix_loses_a_leading_slash" {
  command = plan

  variables {
    store_prefix = "/local/"
  }

  assert {
    condition     = one(google_storage_bucket.blobs.lifecycle_rule[0].condition).matches_prefix == tolist(["local/_uploads/"])
    error_message = "store_prefix must be normalised as the backend does, without a leading slash"
  }
}

run "force_destroy_is_off_by_default" {
  command = plan

  assert {
    condition     = google_storage_bucket.blobs.force_destroy == false
    error_message = "a consumer's terraform destroy must not empty a bucket unless asked"
  }
}

run "force_destroy_reaches_the_bucket" {
  command = plan

  variables {
    force_destroy = true
  }

  assert {
    condition     = google_storage_bucket.blobs.force_destroy
    error_message = "force_destroy = true must let destroy remove the bucket with its objects"
  }
}

run "outputs_name_the_bucket_and_its_signed_urls" {
  command = plan

  assert {
    condition     = [output.bucket, output.signed_url_prefix] == ["example-0a1b2c3d-europe-west2", "storage.googleapis.com/example-0a1b2c3d-europe-west2/"]
    error_message = "the outputs must name the bucket and the path-style prefix the backend signs"
  }
}

run "name_is_checked" {
  command = plan

  variables {
    name = "Not.A.Bucket"
  }

  expect_failures = [var.name]
}

run "data_locations_are_none_or_two" {
  command = plan

  variables {
    data_locations = ["europe-west1"]
  }

  expect_failures = [var.data_locations]
}

run "rpo_is_checked" {
  command = plan

  variables {
    rpo = "FAST"
  }

  expect_failures = [var.rpo]
}

run "principals_must_be_service_accounts" {
  command = plan

  variables {
    principals = { app = "someone@example.com" }
  }

  expect_failures = [var.principals]
}

run "expiring_prefix_days_are_whole_and_positive" {
  command = plan

  variables {
    expiring_prefixes = { "scratch/" = 1.5 }
  }

  expect_failures = [var.expiring_prefixes]
}
