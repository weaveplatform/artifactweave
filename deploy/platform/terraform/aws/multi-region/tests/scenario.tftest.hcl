# How the scenario composes modules/aws/storage into a mirror, checked on a
# plan against mocked providers (decision 0030, S10). The buckets' own
# baseline is the module's tests.

mock_provider "aws" {}
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
  name         = "artifactweave-example"
  regions      = ["eu-west-2", "eu-west-1"]
  app_role_arn = "arn:aws:iam::111122223333:role/artifactweave"
}

# The README's example inputs ("Inputs to set") must render the README's
# example configuration, so the documentation cannot drift from the code.
run "readme_example" {
  command = plan

  variables {
    name         = "artifactweave-example"
    regions      = ["eu-west-2", "eu-west-1"]
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
    name          = "artifactweave-awscn-pr1234-123456789012-mr-t"
    regions       = ["eu-west-2", "eu-west-1"]
    app_role_arn  = "arn:aws:iam::111122223333:role/ci/deployer"
    force_destroy = true
    tags = {
      artifactweave-scenario = "multi-region"
      instance               = "pr1234-123456789012-mr-t"
      expires-at             = "1767225600"
    }
  }

  assert {
    condition = output.buckets == {
      eu-west-2 = "artifactweave-awscn-pr1234-123456789012-mr-t-0a1b2c3d-euw2"
      eu-west-1 = "artifactweave-awscn-pr1234-123456789012-mr-t-0a1b2c3d-euw1"
    }
    error_message = "each bucket must be named <name>-<suffix>-<region code>"
  }

  assert {
    condition     = alltrue([for b in values(output.buckets) : length(b) <= 63])
    error_message = "a CI run's bucket names must fit S3's 63 characters"
  }

  assert {
    condition     = output.expires_at == "1767225600"
    error_message = "expires_at must carry the tag reap reads from the state"
  }

  assert {
    condition = output.mirror_members == [
      { store = "s3_eu_west_2", zone = "eu-west-2", signed_url_prefix = "artifactweave-awscn-pr1234-123456789012-mr-t-0a1b2c3d-euw2.s3.eu-west-2.amazonaws.com/" },
      { store = "s3_eu_west_1", zone = "eu-west-1", signed_url_prefix = "artifactweave-awscn-pr1234-123456789012-mr-t-0a1b2c3d-euw1.s3.eu-west-1.amazonaws.com/" },
    ]
    error_message = "mirror_members must list the members in order, with the prefix each one's presigned URLs start with, which test/platform reads"
  }

  assert {
    condition     = output.storage_host == "artifactweave-awscn-pr1234-123456789012-mr-t-0a1b2c3d-euw2.s3.eu-west-2.amazonaws.com"
    error_message = "storage_host must be the first member's endpoint"
  }
}

run "region_codes_stay_distinct" {
  command = plan

  variables {
    regions = ["ap-south-1", "ap-southeast-1", "us-gov-west-1"]
  }

  assert {
    condition = output.buckets == {
      ap-south-1     = "artifactweave-example-0a1b2c3d-aps1"
      ap-southeast-1 = "artifactweave-example-0a1b2c3d-apse1"
      us-gov-west-1  = "artifactweave-example-0a1b2c3d-usgw1"
    }
    error_message = "a region code must keep a compound direction's initials, so neighbouring regions never share a bucket name"
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
      [for s in values(yamldecode(output.artifactweave_config).blob.stores) : s.s3.prefix] == ["artifacts/", "artifacts/"] &&
      yamldecode(output.artifactweave_config).blob.mirror.write_quorum == 2
    )
    error_message = "store_prefix must reach every store, and write_quorum the mirror"
  }
}

run "regions_must_differ" {
  command = plan

  variables {
    regions = ["eu-west-2", "eu-west-2"]
  }

  expect_failures = [var.regions]
}

run "write_quorum_cannot_exceed_the_members" {
  command = plan

  variables {
    write_quorum = 3
  }

  expect_failures = [var.write_quorum]
}

run "app_role_arn_is_checked" {
  command = plan

  variables {
    app_role_arn = "artifactweave"
  }

  expect_failures = [var.app_role_arn]
}
