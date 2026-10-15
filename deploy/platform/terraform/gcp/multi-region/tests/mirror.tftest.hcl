# How the scenario composes the bucket module into a mirror, checked on a
# plan against mocked providers (decision 0030). The buckets' own security
# invariants are modules/gcp/storage's tests.

mock_provider "google" {
  mock_data "google_service_account" {
    defaults = {
      email = "artifactweave-conformance@lab.iam.gserviceaccount.com"
    }
  }
}
mock_provider "random" {}

# Bucket names, and so the rendered configuration, come from the random
# suffix, which a mocked provider would leave unknown at plan.
override_resource {
  target          = random_id.suffix
  override_during = plan
  values = {
    hex = "0a1b2c3d"
  }
}

run "two_regions_one_mirror" {
  command = plan

  assert {
    condition     = toset(keys(module.member)) == toset(["europe-west2", "europe-west1"])
    error_message = "there must be one member bucket in each of the two regions"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.backend == "mirror"
    error_message = "artifactweave must run the two buckets as a mirror"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.mirror.members == ["gcs_europe_west2", "gcs_europe_west1"]
    error_message = "the mirror's members must be the two stores, the primary region first"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.mirror.zone == "europe-west2"
    error_message = "this instance's zone must be the primary region, so reads prefer it"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.mirror.write_quorum == 1
    error_message = "the default write quorum is one copy, so uploads continue with a region down"
  }

  assert {
    condition = alltrue([
      for name, store in yamldecode(output.artifactweave_config).blob.stores :
      store.backend == "gcs" && store.zone == replace(trimprefix(name, "gcs_"), "_", "-")
    ])
    error_message = "each store must be a gcs store whose zone is its bucket's region"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.stores.gcs_europe_west1.gcs.bucket == "artifactweave-mr-0a1b2c3d-europe-west1"
    error_message = "each store must name its own region's bucket"
  }

  assert {
    condition     = output.mirror_members[0].signed_url_prefix == "storage.googleapis.com/artifactweave-mr-0a1b2c3d-europe-west2/"
    error_message = "mirror_members must list the primary member first, with the prefix its signed URLs start with"
  }
}

run "app_account_signs_and_reaches_both" {
  command = plan

  assert {
    condition = alltrue([
      for name, store in yamldecode(output.artifactweave_config).blob.stores :
      store.gcs.google_access_id == "artifactweave-conformance@lab.iam.gserviceaccount.com"
    ])
    error_message = "both stores must sign as the service account artifactweave runs as"
  }
}

# A scenario run applies as the deployer: it names the app service account,
# so nothing looks up the conformance account, and both buckets carry the
# labels reap matches.
run "scenario_run_grants_the_named_account" {
  command = plan

  variables {
    name                = "awscn-pr160-38712345678-mr-t"
    app_service_account = "artifactweave-deployer@lab.iam.gserviceaccount.com"
    scenario_labels = {
      artifactweave-scenario = "multi-region"
      instance               = "pr160-38712345678-mr-t"
      expires-at             = "1767225600"
    }
  }

  assert {
    condition     = length(data.google_service_account.conformance) == 0
    error_message = "a named app service account must skip the conformance lookup, which the deployer cannot read"
  }

  assert {
    condition = alltrue([
      for name, store in yamldecode(output.artifactweave_config).blob.stores :
      store.gcs.google_access_id == "artifactweave-deployer@lab.iam.gserviceaccount.com"
    ])
    error_message = "both stores must sign as the named app service account"
  }

  assert {
    condition     = alltrue([for r, b in output.buckets : length(b) <= 63])
    error_message = "a scenario bucket name must fit GCS's 63-character limit"
  }

  assert {
    condition     = output.expires_at == "1767225600"
    error_message = "the expires_at output must carry the label reap reads from state"
  }
}

run "regions_must_differ" {
  command = plan

  variables {
    secondary_region = "europe-west2"
  }

  expect_failures = [var.secondary_region]
}
