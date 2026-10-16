# How the scenario composes modules/gcp/storage and modules/gcp/cdn, checked
# on a plan against mocked providers (decision 0030, S10). The bucket's and
# the CDN's own baselines are the modules' tests.

mock_provider "google" {}
mock_provider "random" {}

# Every name, and so the rendered configuration, comes from the random
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
  domain_name         = "cdn.example.com"
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
    domain_name         = "cdn.example.com"
    dns_zone            = "example-com"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config) == yamldecode(one(regex("(?s)<!-- BEGIN_CONFIG_EXAMPLE -->\\n```yaml\\n(.*?)```\\n<!-- END_CONFIG_EXAMPLE -->", file("README.md"))))
    error_message = "the README's configuration example must be what its example inputs render"
  }
}

# A proof's instance as platform-scenarios.yml names it, longer than any it
# produces today (pr<number>-<run id>-<initials>-<step>), with
# scenario-ci.sh's NAME_PREFIX, the labels it sets, the key file it writes
# and the host ci-vars.sh gives it in the bootstrap's zone, after the key
# has been added.
run "ci_run" {
  command = plan

  variables {
    project_id           = "example-project"
    name                 = "awscn-pr1234-123456789012-gc-t"
    location             = "europe-west2"
    app_service_account  = "deployer@example-project.iam.gserviceaccount.com"
    cloud_cdn_key_file   = "/home/runner/work/_temp/scenario-gcp-cdn-pr1234-123456789012-gc-t/secrets/cloud-cdn-key"
    dns_zone             = "example-zone"
    domain_name          = "cdn-pr1234-123456789012-gc-t.gcp.example.com"
    signed_url_key_added = true
    force_destroy        = true
    labels = {
      artifactweave-scenario = "cdn"
      instance               = "pr1234-123456789012-gc-t"
      expires-at             = "1767225600"
    }
  }

  # Every name is the bucket's (modules/gcp/cdn names each piece name), so
  # this keeps the load balancer's and the certificate's within 63
  # characters too, and every name starts with the instance's, which reap
  # finds a lost instance's unlabelled pieces by.
  assert {
    condition     = output.bucket == "awscn-pr1234-123456789012-gc-t-0a1b2c3d" && length(output.bucket) <= 63 && output.backend_bucket == output.bucket
    error_message = "the bucket and the load balancer's pieces must be named <name>-<suffix> and fit 63 characters"
  }

  assert {
    condition     = output.storage_host == "cdn-pr1234-123456789012-gc-t.gcp.example.com"
    error_message = "storage_host must be the instance's own CDN host, which test/platform checks redirects against"
  }

  assert {
    condition     = output.origin_host == "storage.googleapis.com"
    error_message = "origin_host must be Cloud Storage's endpoint, where test/platform checks X-JFrog-Download-Redirect-To: S3 lands"
  }

  assert {
    condition = yamldecode(output.artifactweave_config).blob.gcs.cdn.cloud_cdn == {
      domain   = "cdn-pr1234-123456789012-gc-t.gcp.example.com"
      key_name = "artifactweave"
      key_file = "/home/runner/work/_temp/scenario-gcp-cdn-pr1234-123456789012-gc-t/secrets/cloud-cdn-key"
    }
    error_message = "the configuration must sign for the instance's host, with the key the job generated"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.gcs.google_access_id == "deployer@example-project.iam.gserviceaccount.com"
    error_message = "the app service account must sign the storage URLs X-JFrog-Download-Redirect-To: S3 asks for"
  }

  assert {
    condition     = output.expires_at == "1767225600" && output.signed_url_key_name == "artifactweave"
    error_message = "the harness outputs must carry the label reap reads and the key name scenario-ci.sh adds"
  }

  assert {
    condition     = endswith(module.cdn.cdn_fill_member, "@cloud-cdn-fill.iam.gserviceaccount.com")
    error_message = "once the key is added, the fill account must be granted the bucket"
  }
}

run "the_fill_account_waits_for_the_key" {
  command = plan

  assert {
    condition     = module.cdn.cdn_fill_member == null
    error_message = "until signed_url_key_added, nothing may be granted: Google creates Cloud CDN's fill account only with a project's first key"
  }
}

run "the_cdn_fronts_this_bucket" {
  command = plan

  assert {
    condition     = module.cdn.backend_bucket == "artifactweave-example-0a1b2c3d" && output.bucket == "artifactweave-example-0a1b2c3d"
    error_message = "the CDN must front the scenario's own bucket"
  }
}

run "the_store_prefix_reaches_the_configuration" {
  command = plan

  variables {
    store_prefix = "/artifacts"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.gcs.prefix == "/artifacts"
    error_message = "store_prefix must reach blob.gcs.prefix"
  }
}

run "without_a_zone_the_records_are_handed_back" {
  command = plan

  assert {
    condition     = length(output.dns_records) == 2
    error_message = "without dns_zone, the scenario must output the two records domain_name needs"
  }
}

run "name_must_leave_room_for_the_suffix" {
  command = plan

  variables {
    name = "artifactweave-a-name-that-is-too-long-for-a-bucket-name"
  }

  expect_failures = [var.name]
}

run "app_service_account_is_checked" {
  command = plan

  variables {
    app_service_account = "artifactweave"
  }

  expect_failures = [var.app_service_account]
}

run "signed_url_key_name_is_checked" {
  command = plan

  variables {
    signed_url_key_name = "Artifactweave_Key"
  }

  expect_failures = [var.signed_url_key_name]
}
