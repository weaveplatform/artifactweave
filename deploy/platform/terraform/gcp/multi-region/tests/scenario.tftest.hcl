# How the scenario composes modules/gcp/storage into a mirror, checked on a
# plan against mocked providers (decision 0030, S10). The buckets' own
# baseline is the module's tests.

mock_provider "google" {}
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

variables {
  project_id          = "example-project"
  name                = "artifactweave-example"
  locations           = ["europe-west2", "europe-west1"]
  app_service_account = "artifactweave@example-project.iam.gserviceaccount.com"
}

# The README's example inputs ("Inputs to set") must render the README's
# example configuration, so the documentation cannot drift from the code.
run "readme_example" {
  command = plan

  variables {
    project_id          = "example-project"
    name                = "artifactweave-example"
    locations           = ["europe-west2", "europe-west1"]
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
    name                = "awscn-pr1234-123456789012-mr-t"
    app_service_account = "ci-deployer@example-project.iam.gserviceaccount.com"
    force_destroy       = true
    labels = {
      artifactweave-scenario = "multi-region"
      instance               = "pr1234-123456789012-mr-t"
      expires-at             = "1767225600"
    }
  }

  assert {
    condition = output.buckets == {
      europe-west2 = "awscn-pr1234-123456789012-mr-t-0a1b2c3d-europe-west2"
      europe-west1 = "awscn-pr1234-123456789012-mr-t-0a1b2c3d-europe-west1"
    }
    error_message = "each bucket must be named <name>-<suffix>-<location>"
  }

  assert {
    condition     = alltrue([for b in values(output.buckets) : length(b) <= 63])
    error_message = "a CI run's bucket names must fit Cloud Storage's 63 characters"
  }

  assert {
    condition     = output.expires_at == "1767225600"
    error_message = "expires_at must carry the label reap reads from the state"
  }

  assert {
    condition = output.mirror_members == [
      { store = "gcs_europe_west2", zone = "europe-west2", signed_url_prefix = "storage.googleapis.com/awscn-pr1234-123456789012-mr-t-0a1b2c3d-europe-west2/" },
      { store = "gcs_europe_west1", zone = "europe-west1", signed_url_prefix = "storage.googleapis.com/awscn-pr1234-123456789012-mr-t-0a1b2c3d-europe-west1/" },
    ]
    error_message = "mirror_members must list the members in order, with the prefix each one's signed URLs start with, which test/platform reads"
  }

  assert {
    condition     = alltrue([for s in values(yamldecode(output.artifactweave_config).blob.stores) : s.gcs.google_access_id == "ci-deployer@example-project.iam.gserviceaccount.com"])
    error_message = "every store must sign as the app service account"
  }
}

run "inputs_reach_every_store" {
  command = plan

  variables {
    store_prefix = "artifacts/"
    write_quorum = 2
  }

  assert {
    condition = (
      [for s in values(yamldecode(output.artifactweave_config).blob.stores) : s.gcs.prefix] == ["artifacts/", "artifacts/"] &&
      yamldecode(output.artifactweave_config).blob.mirror.write_quorum == 2
    )
    error_message = "store_prefix must reach every store, and write_quorum the mirror"
  }
}

run "locations_must_differ" {
  command = plan

  variables {
    locations = ["europe-west2", "europe-west2"]
  }

  expect_failures = [var.locations]
}

run "write_quorum_cannot_exceed_the_members" {
  command = plan

  variables {
    write_quorum = 3
  }

  expect_failures = [var.write_quorum]
}

run "app_service_account_is_checked" {
  command = plan

  variables {
    app_service_account = "artifactweave"
  }

  expect_failures = [var.app_service_account]
}
