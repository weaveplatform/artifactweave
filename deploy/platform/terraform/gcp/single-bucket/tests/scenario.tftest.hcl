# How the scenario composes modules/gcp/storage into one bucket, checked on a
# plan against mocked providers (decision 0030, S10). The bucket's own
# baseline is the module's tests.

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
  location            = "europe-west2"
  app_service_account = "artifactweave@example-project.iam.gserviceaccount.com"
}

# The README's example inputs ("Inputs to set") must render the README's
# example configuration, so the documentation cannot drift from the code.
run "readme_example" {
  command = plan

  variables {
    project_id          = "example-project"
    name                = "artifactweave-example"
    location            = "europe-west2"
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
    name                = "awscn-pr1234-123456789012-sb-t"
    app_service_account = "ci-deployer@example-project.iam.gserviceaccount.com"
    force_destroy       = true
    labels = {
      artifactweave-scenario = "single-bucket"
      instance               = "pr1234-123456789012-sb-t"
      expires-at             = "1767225600"
    }
  }

  assert {
    condition     = output.bucket == "awscn-pr1234-123456789012-sb-t-0a1b2c3d" && length(output.bucket) <= 63
    error_message = "the bucket must be named <name>-<suffix>, within Cloud Storage's 63 characters"
  }

  assert {
    condition     = output.expires_at == "1767225600"
    error_message = "expires_at must carry the label reap reads from the state"
  }

  assert {
    condition = yamldecode(output.artifactweave_config).blob.gcs == {
      bucket           = "awscn-pr1234-123456789012-sb-t-0a1b2c3d"
      google_access_id = "ci-deployer@example-project.iam.gserviceaccount.com"
    }
    error_message = "the store must sign as the app service account, with no prefix when store_prefix is empty"
  }
}

# The lab's GCS acceptance environment, as its Makefile applies it: the name,
# prefix and expiry it had before the v2 contract, so its state plans as
# moves, and the service_account output make conformance reads.
run "operator_run" {
  command = plan

  variables {
    name                = "artifactweave-acc"
    store_prefix        = "local/"
    expiring_prefixes   = { "conformance/" = 1 }
    app_service_account = "conformance@example-project.iam.gserviceaccount.com"
    force_destroy       = true
  }

  assert {
    condition     = output.bucket == "artifactweave-acc-0a1b2c3d"
    error_message = "the bucket must keep the name the environment had"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.gcs.prefix == "local/"
    error_message = "store_prefix must reach blob.gcs.prefix"
  }

  assert {
    condition     = output.service_account == "conformance@example-project.iam.gserviceaccount.com"
    error_message = "service_account must name the app service account, which make conformance signs as"
  }
}

run "app_service_account_is_checked" {
  command = plan

  variables {
    app_service_account = "artifactweave"
  }

  expect_failures = [var.app_service_account]
}

run "name_is_checked" {
  command = plan

  variables {
    name = "Artifactweave"
  }

  expect_failures = [var.name]
}
