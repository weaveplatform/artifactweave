# How the scenario composes modules/gcp/storage into one dual-region bucket,
# checked on a plan against mocked providers (decision 0030, S10). The
# bucket's own baseline, its placement included, is the module's tests.

mock_provider "google" {}
mock_provider "random" {}

# The bucket name, and so the rendered configuration, comes from the random
# suffix, which a mocked provider would leave unknown at plan.
override_resource {
  target          = random_id.suffix
  override_during = plan
  values = {
    hex = "0a1b2c3d"
  }
}

variables {
  project_id          = "example-project"
  name                = "artifactweave-example"
  location            = "EUR4"
  app_service_account = "artifactweave@example-project.iam.gserviceaccount.com"
}

# The README's example inputs ("Inputs to set") must render the README's
# example configuration, so the documentation cannot drift from the code.
run "readme_example" {
  command = plan

  variables {
    project_id          = "example-project"
    name                = "artifactweave-example"
    location            = "EUR4"
    app_service_account = "artifactweave@example-project.iam.gserviceaccount.com"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config) == yamldecode(one(regex("(?s)<!-- BEGIN_CONFIG_EXAMPLE -->\\n```yaml\\n(.*?)```\\n<!-- END_CONFIG_EXAMPLE -->", file("README.md"))))
    error_message = "the README's configuration example must be what its example inputs render"
  }
}

# A proof's instance as platform-scenarios.yml names it, longer than any it
# produces today (pr<number>-<run id>-<initials>-<step>), with
# scenario-ci.sh's NAME_PREFIX and the labels it sets.
run "ci_run" {
  command = plan

  variables {
    name                = "awscn-pr1234-123456789012-gr-t"
    app_service_account = "ci-deployer@example-project.iam.gserviceaccount.com"
    force_destroy       = true
    labels = {
      artifactweave-scenario = "geo-replicated"
      instance               = "pr1234-123456789012-gr-t"
      expires-at             = "1767225600"
    }
  }

  assert {
    condition     = output.bucket == "awscn-pr1234-123456789012-gr-t-0a1b2c3d" && length(output.bucket) <= 63
    error_message = "the bucket must be named <name>-<suffix>, within Cloud Storage's 63 characters"
  }

  assert {
    condition     = output.expires_at == "1767225600"
    error_message = "expires_at must carry the label reap reads from the state"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.gcs.google_access_id == "ci-deployer@example-project.iam.gserviceaccount.com"
    error_message = "the store must sign as the app service account"
  }
}

run "default_replication_is_not_turbo" {
  command = plan

  assert {
    condition     = output.rpo == "DEFAULT" && output.location == "EUR4"
    error_message = "turbo replication is billed per GB written, so it must be opt-in; the default placement is the dual-region given"
  }
}

run "turbo_replication_selects_async_turbo" {
  command = plan

  variables {
    location          = "EU"
    data_locations    = ["europe-west1", "europe-west4"]
    turbo_replication = true
    store_prefix      = "artifacts/"
  }

  assert {
    condition     = output.rpo == "ASYNC_TURBO" && output.location == "EU"
    error_message = "turbo_replication must select the 15-minute recovery point objective, in the multi-region the data locations lie in"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.gcs.prefix == "artifacts/"
    error_message = "store_prefix must reach blob.gcs.prefix"
  }
}

run "a_region_is_not_replicated" {
  command = plan

  variables {
    location = "europe-west2"
  }

  expect_failures = [var.location]
}

run "one_region_is_not_a_dual_region" {
  command = plan

  variables {
    location       = "EU"
    data_locations = ["europe-west1"]
  }

  expect_failures = [var.data_locations]
}

run "app_service_account_is_checked" {
  command = plan

  variables {
    app_service_account = "artifactweave"
  }

  expect_failures = [var.app_service_account]
}
