# How the scenario composes two modules/aws/storage buckets and
# modules/aws/replication, checked on a plan against mocked providers
# (decision 0030, S10). The buckets' and the role's own baselines are the
# modules' tests.

mock_provider "aws" {}
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
  name           = "artifactweave-example"
  region         = "eu-west-2"
  replica_region = "eu-west-1"
  app_role_arn   = "arn:aws:iam::111122223333:role/artifactweave"
}

# The README's example inputs ("Inputs to set") must render the README's
# example configuration, so the documentation cannot drift from the code.
run "readme_example" {
  command = plan

  variables {
    name           = "artifactweave-example"
    region         = "eu-west-2"
    replica_region = "eu-west-1"
    app_role_arn   = "arn:aws:iam::111122223333:role/artifactweave"
  }

  assert {
    condition     = yamldecode(output.artifactweave_config) == yamldecode(one(regex("(?s)<!-- BEGIN_CONFIG_EXAMPLE -->\\n```yaml\\n(.*?)```\\n<!-- END_CONFIG_EXAMPLE -->", file("README.md"))))
    error_message = "the README's configuration example must be what its example inputs render"
  }
}

# A proof's instance as platform-scenarios.yml names it, longer than any it
# produces today (pr<number>-<run id>-<initials>-<step>), with
# scenario-ci.sh's NAME_PREFIX, the labels it sets and the boundary and role
# path ci-vars.sh passes.
run "ci_run" {
  command = plan

  variables {
    name                     = "artifactweave-awscn-pr1234-123456789012-gr-t"
    region                   = "eu-west-2"
    replica_region           = "eu-west-1"
    app_role_arn             = "arn:aws:iam::111122223333:role/ci/deployer"
    permissions_boundary_arn = "arn:aws:iam::111122223333:policy/ci/boundary"
    iam_role_path            = "/ci-scenarios/"
    force_destroy            = true
    tags = {
      artifactweave-scenario = "geo-replicated"
      instance               = "pr1234-123456789012-gr-t"
      expires-at             = "1767225600"
    }
  }

  assert {
    condition = [output.bucket, output.replica_bucket] == [
      "artifactweave-awscn-pr1234-123456789012-gr-t-0a1b2c3d", "artifactweave-awscn-pr1234-123456789012-gr-t-0a1b2c3d-r",
    ] && length(output.replica_bucket) <= 63
    error_message = "the buckets must be named <name>-<suffix> and <name>-<suffix>-r, and fit S3's 63 characters"
  }

  assert {
    condition     = module.replication.role_name == "artifactweave-awscn-pr1234-123456789012-0a1b2c3d-repl" && length(module.replication.role_name) <= 64
    error_message = "the replication role must be named after the scenario and fit IAM's 64 characters"
  }

  assert {
    condition     = [output.region, output.replica_region, output.replicated_prefix] == ["eu-west-2", "eu-west-1", "sha256/"]
    error_message = "the outputs test/platform reads must name the primary's and the replica's regions and the replicated prefix"
  }

  assert {
    condition     = output.expires_at == "1767225600"
    error_message = "expires_at must carry the tag reap reads from the state"
  }

  assert {
    condition     = output.storage_host == "artifactweave-awscn-pr1234-123456789012-gr-t-0a1b2c3d.s3.eu-west-2.amazonaws.com"
    error_message = "storage_host must be the primary's endpoint, which test/platform checks redirects against"
  }
}

run "committed_blobs_under_the_store_prefix_replicate" {
  command = plan

  variables {
    store_prefix = "/artifacts"
  }

  assert {
    condition     = output.replicated_prefix == "artifacts/sha256/"
    error_message = "the replicated prefix must be the committed blobs under store_prefix, normalised as the backend writes it"
  }

  assert {
    condition = (
      yamldecode(output.artifactweave_config).blob.s3 == { bucket = "artifactweave-example-0a1b2c3d", region = "eu-west-2", prefix = "/artifacts" } &&
      yamldecode(output.failover_artifactweave_config).blob.s3 == { bucket = "artifactweave-example-0a1b2c3d-r", region = "eu-west-1", prefix = "/artifacts" }
    )
    error_message = "the configuration must name the primary, and the failover configuration the replica, both with store_prefix"
  }
}

run "replica_region_must_differ" {
  command = plan

  variables {
    replica_region = "eu-west-2"
  }

  expect_failures = [var.replica_region]
}

run "name_must_leave_room_for_the_suffixes" {
  command = plan

  variables {
    name = "artifactweave-a-name-that-is-too-long-for-the-replica"
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
