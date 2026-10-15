# How the scenario composes modules/aws/storage into one bucket, checked on a
# plan against mocked providers (decision 0030, S10). The bucket's own
# baseline is the module's tests.

mock_provider "aws" {}
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
  name         = "artifactweave-example"
  region       = "eu-west-2"
  app_role_arn = "arn:aws:iam::111122223333:role/artifactweave"
}

# The README's example inputs ("Inputs to set") must render the README's
# example configuration, so the documentation cannot drift from the code.
run "readme_example" {
  command = plan

  variables {
    name         = "artifactweave-example"
    region       = "eu-west-2"
    app_role_arn = "arn:aws:iam::111122223333:role/artifactweave"
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
    name          = "artifactweave-awscn-pr1234-123456789012-sb-t"
    region        = "eu-west-2"
    app_role_arn  = "arn:aws:iam::111122223333:role/ci/deployer"
    force_destroy = true
    tags = {
      artifactweave-scenario = "single-bucket"
      instance               = "pr1234-123456789012-sb-t"
      expires-at             = "1767225600"
    }
  }

  assert {
    condition     = output.bucket == "artifactweave-awscn-pr1234-123456789012-sb-t-0a1b2c3d" && length(output.bucket) <= 63
    error_message = "the bucket must be named <name>-<suffix> and fit S3's 63 characters"
  }

  assert {
    condition     = output.expires_at == "1767225600"
    error_message = "expires_at must carry the tag reap reads from the state"
  }

  assert {
    condition     = output.storage_host == "artifactweave-awscn-pr1234-123456789012-sb-t-0a1b2c3d.s3.eu-west-2.amazonaws.com"
    error_message = "storage_host must be the bucket's virtual-hosted endpoint, which test/platform checks redirects against"
  }

  assert {
    condition     = output.region == "eu-west-2"
    error_message = "region must be the bucket's, which the conformance tests read"
  }
}

# The lab's conformance environment, as the Makefile applies it: it keeps
# the bucket it has had since before modules (moved.tf), and a local run
# writes under local/.
run "lab_run" {
  command = plan

  variables {
    name         = "artifactweave-acc"
    store_prefix = "local/"
  }

  assert {
    condition     = output.bucket == "artifactweave-acc-0a1b2c3d"
    error_message = "the conformance environment's bucket must keep its name, artifactweave-acc-<suffix>, or the moved blocks would replace it"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config).blob.s3.prefix == "local/"
    error_message = "store_prefix must reach the rendered configuration"
  }
}

run "name_must_leave_room_for_the_suffix" {
  command = plan

  variables {
    name = "artifactweave-a-name-that-is-far-too-long-for-the-bucket-limit"
  }

  expect_failures = [var.name]
}

run "app_role_arn_is_checked" {
  command = plan

  variables {
    app_role_arn = "artifactweave"
  }

  expect_failures = [var.app_role_arn]
}
